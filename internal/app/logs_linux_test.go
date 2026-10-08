//go:build linux

package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
	"lazyrun/internal/model"
	"lazyrun/internal/runtime"
	"lazyrun/internal/supervisor"
	"lazyrun/internal/transport"
)

const burstConfig = `version: 1
logs: {maxBytes: 4096, tail: 3}
tasks:
  burst:
    command: |
      i=0
      while [ "$i" -lt 50000 ]; do printf '%064d' "$i"; i=$((i+1)); done
      printf '\377\376FINAL'
  independent:
    command: printf independent
`

func TestDiskLogsStayBoundedWithoutClientsAndSurviveReplacement(t *testing.T) {
	f := newIntegration(t, burstConfig)
	run := f.start("burst", nil)
	f.connection.Close()
	f.connection = nil
	// No connected dashboard or log reader participates in collection.
	eventuallyIntegration(t, func() bool { return !sameProcess(run.Identity) })
	f.connection = f.connect(f.root)
	done := f.finished("burst", run)
	if done.Outcome == nil || done.Outcome.Kind != model.Success {
		t.Fatal(done)
	}
	read, err := f.connection.Client.Logs(context.Background(), "burst", run.ID, 0, 0)
	if err != nil || read.Unavailable || !read.Truncated || len(read.Data) > 4096 || !bytes.HasSuffix(read.Data, []byte("\xff\xfeFINAL")) || len(read.Records) == 0 {
		t.Fatal(read, err)
	}
	paths, err := supervisor.OpenPaths(model.ProjectID(f.root))
	if err != nil {
		t.Fatal(err)
	}
	defer paths.Close()
	logs, err := paths.State.Child("logs", false)
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	alias, err := logs.Child(fmt.Sprintf("%x", sha256.Sum256([]byte("burst"))), false)
	if err != nil {
		t.Fatal(err)
	}
	defer alias.Close()
	names, err := alias.Names()
	if err != nil || len(names) != 4 {
		t.Fatal(names, err)
	}
	var total int64
	for _, name := range names {
		stat, err := alias.Stat(name, unix.S_IFREG)
		if err != nil {
			t.Fatal(err)
		}
		total += stat.Size
	}
	if total > 4096+336 {
		t.Fatal("disk ring exceeded budget", total)
	}
	killServer(t, f)
	f.connection = f.connect(f.root)
	restored, err := f.connection.Client.Logs(context.Background(), "burst", run.ID, 0, 0)
	if err != nil || !bytes.Equal(restored.Data, read.Data) || restored.Next != read.Next || restored.Unavailable || !restored.Truncated {
		t.Fatal(restored, err)
	}
	if len(restored.Records) != len(read.Records) {
		t.Fatal("lost capture-time records")
	}
	for i := range read.Records {
		if read.Records[i].Cursor != restored.Records[i].Cursor || !read.Records[i].Time.Equal(restored.Records[i].Time) {
			t.Fatal("changed historical records")
		}
	}
	// A rerun is a fresh latest run, not appended history.
	next := f.start("burst", nil)
	f.finished("burst", next)
	if next.ID == run.ID {
		t.Fatal("reused run ID")
	}
	if _, err := f.connection.Client.Logs(context.Background(), "burst", run.ID, 0, 0); !remoteCode(err, transport.CodeRunChanged) {
		t.Fatal("served old run", err)
	}
}

func TestConcurrentBoundedReadersDoNotOwnBurstCapture(t *testing.T) {
	f := newIntegration(t, burstConfig)
	run := f.start("burst", nil)
	var wg sync.WaitGroup
	failures := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var cursor uint64
			for range 12 {
				read, err := f.connection.Client.Logs(context.Background(), "burst", run.ID, cursor, 128)
				if err != nil {
					failures <- err
					return
				}
				if len(read.Data) > 128 || read.Next < cursor || read.RunID != run.ID {
					failures <- fmt.Errorf("unbounded or regressing read")
					return
				}
				cursor = read.Next
			}
		}()
	}
	task := f.start("independent", nil)
	f.finished("independent", task)
	if f.logs("independent", task) != "independent" {
		t.Fatal("log readers blocked independent alias")
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	f.finished("burst", run)
	tail, err := f.connection.Client.TailLogs(context.Background(), "burst", run.ID, 0, 128)
	if err != nil || len(tail.Data) > 128 || !bytes.HasSuffix(tail.Data, []byte("FINAL")) {
		t.Fatal(tail, err)
	}
}

func TestUnsafeLogDestinationReportsErrorButCommandStillDrainsAndStops(t *testing.T) {
	f := newIntegration(t, pulseConfig)
	paths, err := supervisor.OpenPaths(model.ProjectID(f.root))
	if err != nil {
		t.Fatal(err)
	}
	defer paths.Close()
	logs, err := paths.State.Child("logs", true)
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	alias, err := logs.Child(fmt.Sprintf("%x", sha256.Sum256([]byte("pulse"))), true)
	if err != nil {
		t.Fatal(err)
	}
	defer alias.Close()
	target := alias.ProcPath("0.log")
	if err := os.WriteFile(target, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0644); err != nil {
		t.Fatal(err)
	}
	run := f.start("pulse", nil)
	eventuallyIntegration(t, func() bool { return findRun(f.state(), "pulse").LogError != "" })
	read, err := f.connection.Client.Logs(context.Background(), "pulse", run.ID, 0, 0)
	if err != nil || read.Error == "" {
		t.Fatal("concealed retention failure", read, err)
	}
	if !sameProcess(run.Identity) {
		t.Fatal("log failure stopped the command")
	}
	task := f.start("env", []string{"SNAPSHOT=healthy"})
	f.finished("env", task)
	if !strings.Contains(f.logs("env", task), "healthy") {
		t.Fatal("failure affected another alias")
	}
	if _, err := f.connection.Client.Stop(context.Background(), "pulse"); err != nil {
		t.Fatal(err)
	}
	done := f.finished("pulse", run)
	if done.LogError == "" || done.Outcome == nil || done.LogEnd == 0 {
		t.Fatal("lost disk error or stopped draining", done)
	}
	b, err := os.ReadFile(target)
	if err != nil || string(b) != "untouched" {
		t.Fatal("modified unsafe file", err)
	}
}

func TestLogBudgetChangesApplyOnlyToNextRun(t *testing.T) {
	f := newIntegration(t, "version: 1\nlogs: {maxBytes: 4096}\ntasks: {x: {command: printf first}}")
	first := f.start("x", nil)
	f.finished("x", first)
	f.write("version: 1\nlogs: {maxBytes: 1}\ntasks: {x: {command: printf second}}")
	c := f.connect(f.root)
	defer c.Close()
	if f.logs("x", first) != "first" || findRun(f.state(), "x").LogMaxBytes != 4096 {
		t.Fatal("config rewrote retained run")
	}
	second := f.start("x", nil)
	f.finished("x", second)
	read, err := f.connection.Client.Logs(context.Background(), "x", second.ID, 0, 0)
	if err != nil || string(read.Data) != "d" || !read.Truncated || findRun(f.state(), "x").LogMaxBytes != 1 {
		t.Fatal(read, err)
	}
}

func TestCLIInitialTailAndExplicitCursorReads(t *testing.T) {
	f := newIntegration(t, "version: 1\nlogs: {tail: 2}\ntasks: {x: {command: \"printf 'one\\ntwo\\nthree\\n'\"}}")
	run := f.start("x", nil)
	f.finished("x", run)
	for _, test := range []struct {
		args []string
		want string
	}{
		{nil, "two\nthree\n"}, {[]string{"--after", "0"}, "one\ntwo\nthree\n"}, {[]string{"--tail", "1"}, "three\n"}, {[]string{"--tail", "0"}, "one\ntwo\nthree\n"},
	} {
		cmd := exec.Command(testExecutable, append([]string{"--logs", "x", "--run-id", run.ID}, test.args...)...)
		cmd.Dir = f.root
		b, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		var read model.LogRead
		if err := json.Unmarshal(b, &read); err != nil {
			t.Fatal(err)
		}
		if string(read.Data) != test.want || read.Next != 14 || len(read.Records) == 0 {
			t.Fatal(read)
		}
	}
	for _, args := range [][]string{
		{"--logs", "x", "--tail", "-1"},
		{"--logs", "x", "--after", ""},
		{"--logs", "x", "--tail", "1", "--after", "0"},
		{"--logs", "x", "--tail", "1", "--tail", "2"},
		{"--after", "0"},
	} {
		cmd := exec.Command(testExecutable, args...)
		cmd.Dir = f.root
		if err := cmd.Run(); err == nil {
			t.Fatal("accepted invalid log options", args)
		}
	}
	if _, err := f.connection.Client.TailLogs(context.Background(), "x", run.ID, -1, 0); !remoteCode(err, transport.CodeInvalid) {
		t.Fatal(err)
	}
	if _, err := f.connection.Client.Logs(context.Background(), "x", run.ID, 0, runtime.MaxLogRead+1); !remoteCode(err, transport.CodeInvalid) {
		t.Fatal(err)
	}
}
