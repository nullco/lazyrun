//go:build linux

package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"lazyrun/internal/model"
	"lazyrun/internal/testutil"
)

func TestProcessFixture(t *testing.T) { testutil.RunFixture() }

func fixture(t *testing.T, mode string) string {
	t.Helper()
	cmd, err := testutil.FixtureCommand(mode)
	if err != nil {
		t.Fatal(err)
	}
	return cmd
}

func manager(t *testing.T, commands map[string]string, opts Options) *Manager {
	t.Helper()
	root := t.TempDir()
	p := model.Project{ID: model.ProjectID(root), Root: root, Shell: "/bin/sh", Logs: model.DefaultLogSettings()}
	for alias, cmd := range commands {
		p.Tasks = append(p.Tasks, model.Definition{Alias: alias, Kind: model.Task, Command: cmd, Cwd: root})
	}
	m, err := New(p, opts)
	if err != nil {
		t.Fatal(err)
	}
	// Forceful cleanup belongs only to this test harness, never product Stop.
	t.Cleanup(func() {
		for _, s := range m.slots {
			s.mu.Lock()
			if s.cmd != nil && s.run.Lifecycle.Active() && verifyLeader(s.run.Identity) == nil {
				_ = unix.Kill(-s.run.Identity.PGID, unix.SIGKILL)
			}
			id, done := s.run.ID, s.done
			s.mu.Unlock()
			if id != "" {
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Errorf("run %s leaked", id)
				}
			}
		}
	})
	return m
}

func start(t *testing.T, m *Manager, alias string) model.Run {
	t.Helper()
	r, err := m.Start(alias, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func wait(t *testing.T, m *Manager, alias string, r model.Run) model.Run {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := m.Wait(ctx, alias, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func output(t *testing.T, m *Manager, alias string, r model.Run) string {
	t.Helper()
	b, _, err := m.Output(alias, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func eventually(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func ready(t *testing.T, m *Manager, alias string, r model.Run) {
	t.Helper()
	eventually(t, func() bool { return strings.Contains(output(t, m, alias, r), "ready:") })
}

func TestOneShotOutcomesAndCapture(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		kind          model.OutcomeKind
		code, signal  int
	}{
		{"success", fixture(t, "output"), model.Success, 0, 0},
		{"failure", "printf failure; exit 23", model.NonzeroExit, 23, 0},
		{"shell error", "command_that_does_not_exist_lazyrun", model.NonzeroExit, 127, 0},
		{"signal", "kill -TERM $$", model.Signaled, 0, int(unix.SIGTERM)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := manager(t, map[string]string{"x": tc.command}, Options{})
			r := wait(t, m, "x", start(t, m, "x"))
			if r.Lifecycle != model.Exited || r.Outcome == nil || r.Outcome.Kind != tc.kind || r.EndedAt == nil {
				t.Fatalf("%+v", r)
			}
			if tc.signal != 0 {
				if r.Outcome.Signal != tc.signal || r.Outcome.ExitCode != nil {
					t.Fatalf("%+v", r.Outcome)
				}
			} else if r.Outcome.ExitCode == nil || *r.Outcome.ExitCode != tc.code {
				t.Fatalf("%+v", r.Outcome)
			}
			if tc.name == "success" {
				text := output(t, m, "x", r)
				for _, part := range []string{"stdout\n", "stderr\n", "partial"} {
					if !strings.Contains(text, part) {
						t.Fatal(text)
					}
				}
			}
		})
	}
}

func TestOSLaunchFailure(t *testing.T) {
	m := manager(t, map[string]string{"x": "echo hello"}, Options{})
	m.slots["x"].shell = "/does/not/exist/lazyrun"
	r, err := m.Start("x", nil)
	if err == nil || r.Lifecycle != model.Exited || r.Outcome.Kind != model.LaunchFailed || r.Outcome.ExitCode != nil || r.Identity.PID != 0 {
		t.Fatalf("%+v, %v", r, err)
	}
	if text := output(t, m, "x", r); text != "" {
		t.Fatal(text)
	}
	if _, err := m.Wait(context.Background(), "x", r.ID); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentStartAndIndependentAliases(t *testing.T) {
	m := manager(t, map[string]string{"a": fixture(t, "service"), "b": fixture(t, "service")}, Options{})
	var wg sync.WaitGroup
	results := make(chan error, 20)
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := m.Start("a", os.Environ()); results <- err }()
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrAlreadyRunning) {
			t.Fatal(err)
		}
	}
	if accepted != 1 {
		t.Fatal("accepted starts:", accepted)
	}
	a, _ := m.Snapshot("a")
	b := start(t, m, "b")
	ready(t, m, "a", a)
	ready(t, m, "b", b)
	if a.Identity.PID == b.Identity.PID {
		t.Fatal("independent aliases not concurrent")
	}
	for alias, r := range map[string]model.Run{"a": a, "b": b} {
		if _, err := m.Stop(alias); err != nil {
			t.Fatal(err)
		}
		finished := wait(t, m, alias, r)
		if !finished.StopRequested || !strings.Contains(output(t, m, alias, r), "final") {
			t.Fatal("lost final output/intent")
		}
	}
}

func TestTermReachesChildrenAndGroupFinishes(t *testing.T) {
	m := manager(t, map[string]string{"x": fixture(t, "parent")}, Options{})
	r := start(t, m, "x")
	ready(t, m, "x", r)
	if r.Identity.PID != r.Identity.PGID || r.Identity.StartTicks == 0 || r.Identity.BootID == "" {
		t.Fatalf("missing identity %+v", r.Identity)
	}
	if _, err := m.Stop("x"); err != nil {
		t.Fatal(err)
	}
	r = wait(t, m, "x", r)
	text := output(t, m, "x", r)
	if !strings.Contains(text, "parent-final") || !strings.Contains(text, "final") || r.Outcome.Kind != model.Success {
		t.Fatalf("%s %+v", text, r)
	}
}

func TestRestartUsesNewRunAndRequestEnvironment(t *testing.T) {
	m := manager(t, map[string]string{"x": fixture(t, "service")}, Options{})
	first := start(t, m, "x")
	ready(t, m, "x", first)
	second, err := m.Restart("x", os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID || second.Identity.PID == first.Identity.PID {
		t.Fatal("restart reused run identity")
	}
	ready(t, m, "x", second)
	if _, _, err := m.Output("x", first.ID); !errors.Is(err, ErrRunChanged) {
		t.Fatal("old output served", err)
	}
	if text := output(t, m, "x", second); strings.Contains(text, "final") {
		t.Fatal("previous logs retained")
	}
	if _, err := m.Stop("x"); err != nil {
		t.Fatal(err)
	}
	wait(t, m, "x", second)
}

func TestRequestEnvironmentAndOverrides(t *testing.T) {
	t.Setenv("SNAPSHOT", "supervisor-must-not-leak")
	m := manager(t, map[string]string{"x": fixture(t, "env")}, Options{})
	m.slots["x"].definition.Env = map[string]string{"OVERRIDE": "configured"}
	for _, value := range []string{"first", "new-virtualenv"} {
		r, err := m.Start("x", []string{"SNAPSHOT=" + value, "OVERRIDE=request"})
		if err != nil {
			t.Fatal(err)
		}
		wait(t, m, "x", r)
		if got := output(t, m, "x", r); got != "SNAPSHOT="+value+" OVERRIDE=configured" {
			t.Fatal(got)
		}
	}
	r, err := m.Restart("x", []string{"SNAPSHOT=restart"})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, m, "x", r)
	if got := output(t, m, "x", r); got != "SNAPSHOT=restart OVERRIDE=configured" {
		t.Fatal(got)
	}
	if _, err := m.Start("x", []string{"INVALID"}); err == nil {
		t.Fatal("bad environment accepted")
	}
}

func TestBlockedRestartCancelsReplacementAndDoesNotQueue(t *testing.T) {
	m := manager(t, map[string]string{"x": fixture(t, "ignore")}, Options{RestartTimeout: 300 * time.Millisecond})
	r := start(t, m, "x")
	ready(t, m, "x", r)
	result := make(chan error, 1)
	go func() { _, err := m.Restart("x", os.Environ()); result <- err }()
	eventually(t, func() bool { s := m.slots["x"]; s.mu.Lock(); defer s.mu.Unlock(); return s.pending })
	if _, err := m.Restart("x", nil); !errors.Is(err, ErrRestartPending) {
		t.Fatal(err)
	}
	if _, err := m.Start("x", nil); !errors.Is(err, ErrRestartPending) {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrRestartBlocked) {
		t.Fatal(err)
	}
	state, _ := m.Snapshot("x")
	if state.ID != r.ID || state.Lifecycle != model.Stopping {
		t.Fatalf("unexpected replacement/state: %+v", state)
	}
	if _, err := m.Start("x", nil); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatal(err)
	}
	// Fixture-only exit signal; product never escalates beyond SIGTERM.
	if err := unix.Kill(-r.Identity.PGID, unix.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	finished := wait(t, m, "x", r)
	time.Sleep(2 * m.opts.PollInterval)
	state, _ = m.Snapshot("x")
	if state.ID != finished.ID || state.Lifecycle != model.Exited {
		t.Fatal("canceled replacement launched later")
	}
}

func TestShellExitDoesNotPermitOverlapWhileChildLives(t *testing.T) {
	// The runtime's subreaper must adopt the orphan and reap it only after
	// stable process-group completion.
	m := manager(t, map[string]string{"x": fixture(t, "orphan")}, Options{RestartTimeout: 100 * time.Millisecond})
	r := start(t, m, "x")
	ready(t, m, "x", r)
	var childPID int
	for _, line := range strings.Split(output(t, m, "x", r), "\n") {
		if strings.HasPrefix(line, "child:") {
			childPID, _ = strconv.Atoi(strings.TrimPrefix(line, "child:"))
		}
	}
	if childPID == 0 {
		t.Fatal("missing child PID")
	}
	childIdentity, err := readStat(childPID)
	if err != nil {
		t.Fatal(err)
	}
	// Failure-only cleanup verifies identity before touching an explicit PID.
	t.Cleanup(func() {
		if childPID > 0 {
			p, err := readStat(childPID)
			if err == nil && p.start == childIdentity.start {
				_ = unix.Kill(childPID, unix.SIGKILL)
				var status unix.WaitStatus
				_, _ = unix.Wait4(childPID, &status, 0, nil)
			}
		}
	})
	eventually(t, func() bool { p, err := readStat(r.Identity.PID); return err == nil && p.state == 'Z' })
	if _, err := m.Start("x", nil); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatal("shell exit permitted overlap", err)
	}
	if _, err := m.Restart("x", nil); !errors.Is(err, ErrRestartBlocked) {
		t.Fatal("child did not block restart", err)
	}
	if err := unix.Kill(-r.Identity.PGID, unix.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	finished := wait(t, m, "x", r)
	if finished.Outcome.Kind != model.NonzeroExit || *finished.Outcome.ExitCode != 7 {
		t.Fatalf("invented outcome: %+v", finished.Outcome)
	}
	var status unix.WaitStatus
	if _, err := unix.Wait4(childPID, &status, unix.WNOHANG, nil); !errors.Is(err, unix.ECHILD) {
		t.Fatalf("runtime did not reap adopted descendant: %v", err)
	}
	childPID = 0 // Cleanup must not signal a reaped/reusable PID.
}

func TestCanceledWaitDoesNotStopRunAndStdinIsNull(t *testing.T) {
	m := manager(t, map[string]string{"x": fixture(t, "service"), "stdin": fixture(t, "stdin")}, Options{})
	r := start(t, m, "x")
	ready(t, m, "x", r)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Wait(ctx, "x", r.ID); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	state, _ := m.Snapshot("x")
	if state.Lifecycle != model.Running || state.StopRequested {
		t.Fatal("view cancellation stopped run")
	}
	if _, err := m.Stop("x"); err != nil {
		t.Fatal(err)
	}
	wait(t, m, "x", r)
	inputRun := start(t, m, "stdin")
	wait(t, m, "stdin", inputRun)
	if got := output(t, m, "stdin", inputRun); got != "stdin=0 err=<nil>" {
		t.Fatal(got)
	}
}

func TestHighVolumeOutputBoundedAndFinalBytesRetained(t *testing.T) {
	m := manager(t, map[string]string{"x": fixture(t, "volume")}, Options{})
	r := wait(t, m, "x", start(t, m, "x"))
	b, truncated, err := m.Output("x", r.ID)
	if err != nil || !truncated || len(b) > MaxMemoryOutput || !strings.HasSuffix(string(b), "END") || r.Outcome.Kind != model.Success {
		t.Fatalf("bytes=%d truncated=%v outcome=%+v err=%v", len(b), truncated, r.Outcome, err)
	}
	if cap(m.slots["x"].output.data) > MaxMemoryOutput {
		t.Fatal("backing allocation exceeded bound")
	}
}

func TestRefuseFakeIdentityAndUnknownAliases(t *testing.T) {
	m := manager(t, map[string]string{"x": fixture(t, "service")}, Options{})
	r := start(t, m, "x")
	ready(t, m, "x", r)
	fake := r.Identity
	fake.BootID = "another-boot"
	if err := terminateGroup(fake); err == nil {
		t.Fatal("signaled a stale boot identity")
	}
	fake = r.Identity
	fake.StartTicks++
	if err := terminateGroup(fake); err == nil {
		t.Fatal("signaled an identity mismatch")
	}
	fake.PGID = os.Getpid()
	if err := terminateGroup(fake); err == nil {
		t.Fatal("signaled a fake group")
	}
	if _, err := m.Start("missing", nil); err == nil {
		t.Fatal("unknown alias started")
	}
	if _, err := m.Stop("missing"); err == nil {
		t.Fatal("unknown alias stopped")
	}
	if _, err := m.Stop("x"); err != nil {
		t.Fatal(err)
	}
	wait(t, m, "x", r)
}

func TestParseStatWithTrickyComm(t *testing.T) {
	fields := []string{"S", "1", "42", "1", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "1", "0", "12345"}
	p, err := parseStat([]byte("42 (a tricky ) process) " + strings.Join(fields, " ")))
	if err != nil || p.pid != 42 || p.group != 42 || p.start != 12345 || p.state != 'S' {
		t.Fatalf("%+v %v", p, err)
	}
	for _, str := range []string{"", "1 no parentheses", "1 (x) S", "x (x) " + strings.Join(fields, " ")} {
		if _, err := parseStat([]byte(str)); err == nil {
			t.Fatal(fmt.Sprintf("accepted %q", str))
		}
	}
}
