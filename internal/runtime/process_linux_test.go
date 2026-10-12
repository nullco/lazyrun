//go:build linux

package runtime

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/nullco/lazyrun/internal/model"
)

func TestSIGCHLDBroadcastCompletesConcurrentRuns(t *testing.T) {
	commands := make(map[string]string)
	for i := range 8 {
		commands[strconv.Itoa(i)] = "echo ready:; exec sleep 600"
	}
	m := manager(t, commands, Options{PollInterval: time.Hour})
	runs := make([]model.Run, 0, len(commands))
	for alias := range commands {
		r := start(t, m, alias)
		ready(t, m, alias, r)
		runs = append(runs, r)
	}
	// Hold the monitors while exits accumulate. Buffered SIGCHLD notifications
	// may coalesce, but every run must reconcile when its lock is released.
	func() {
		for _, r := range runs {
			s := m.slots[r.Definition.Alias]
			s.mu.Lock()
			defer s.mu.Unlock()
		}
		for _, r := range runs {
			if err := unix.Kill(-r.Identity.PGID, unix.SIGTERM); err != nil {
				t.Fatal(err)
			}
		}
		for _, r := range runs {
			eventually(t, func() bool {
				p, err := readStat(r.Identity.PID)
				return err == nil && p.state == 'Z'
			})
		}
	}()
	for _, r := range runs {
		finished := wait(t, m, r.Definition.Alias, r)
		if finished.Outcome == nil || finished.Outcome.Kind != model.Signaled {
			t.Fatalf("lost shell outcome: %+v", finished)
		}
	}
}

func TestSIGCHLDCompletesInstantExitsWithoutFallback(t *testing.T) {
	m := manager(t, map[string]string{"x": "exit 23"}, Options{PollInterval: time.Hour})
	for range 20 {
		r := wait(t, m, "x", start(t, m, "x"))
		if r.Outcome == nil || r.Outcome.ExitCode == nil || *r.Outcome.ExitCode != 23 {
			t.Fatalf("lost instant exit: %+v", r)
		}
	}
}

func TestSIGCHLDTracksDescendantAfterLeaderExit(t *testing.T) {
	m := manager(t, map[string]string{"x": fixture(t, "orphan")}, Options{PollInterval: time.Hour})
	r := start(t, m, "x")
	ready(t, m, "x", r)
	eventually(t, func() bool {
		p, err := readStat(r.Identity.PID)
		return err == nil && p.state == 'Z'
	})
	state, err := m.Snapshot("x")
	if err != nil || state.Lifecycle != model.Running {
		t.Fatalf("finished while descendant was alive: %+v %v", state, err)
	}
	if err := unix.Kill(-r.Identity.PGID, unix.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	finished := wait(t, m, "x", r)
	if finished.Outcome == nil || finished.Outcome.ExitCode == nil || *finished.Outcome.ExitCode != 7 {
		t.Fatalf("lost zombie leader's outcome: %+v", finished)
	}
}

func TestSIGCHLDReapsOrphansWhileLeaderLives(t *testing.T) {
	m := manager(t, map[string]string{"x": "sh -c 'sleep 0.2 & echo adopted:$!'; echo ready:; exec sleep 600"}, Options{PollInterval: time.Hour})
	r := start(t, m, "x")
	ready(t, m, "x", r)
	var child int
	for _, line := range strings.Split(output(t, m, "x", r), "\n") {
		if strings.HasPrefix(line, "adopted:") {
			child, _ = strconv.Atoi(strings.TrimPrefix(line, "adopted:"))
		}
	}
	if child == 0 {
		t.Fatal("missing adopted child PID")
	}
	eventually(t, func() bool {
		_, err := readStat(child)
		return os.IsNotExist(err)
	})
	state, err := m.Snapshot("x")
	if err != nil || state.Lifecycle != model.Running {
		t.Fatalf("leader stopped during orphan reaping: %+v %v", state, err)
	}
}

// Use an unsubscribed channel to control notifications without changing the
// process-wide signal disposition. The actual child still has an owned PGID.
func controlledMonitor(t *testing.T, interval time.Duration) (*slot, chan os.Signal, <-chan model.Run) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "exec sleep 600")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	monitorStarted := false
	t.Cleanup(func() {
		if !monitorStarted {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}
	id, err := identity(cmd.Process.Pid, strings.TrimSpace(string(boot)))
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	writer.Close()
	t.Cleanup(func() { reader.Close() })
	captureDone := make(chan error, 1)
	captureDone <- nil
	exits := make(chan os.Signal, 1)
	transitions := make(chan model.Run, 16)
	m := &Manager{opts: Options{PollInterval: interval, DrainTimeout: time.Second, Persist: func(r model.Run) error {
		transitions <- r
		return nil
	}}}
	s := &slot{cmd: cmd, done: make(chan struct{}), run: model.Run{ID: "controlled", Identity: id, Lifecycle: model.Unknown}}
	t.Cleanup(func() {
		s.mu.Lock()
		if s.run.Lifecycle.Active() {
			// Restore any identity deliberately corrupted by a test before cleanup.
			s.run.Identity = id
			if verifyLeader(id) == nil {
				_ = unix.Kill(-id.PGID, unix.SIGKILL)
			}
			select {
			case exits <- syscall.SIGCHLD:
			default:
			}
		}
		s.mu.Unlock()
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
			t.Error("controlled monitor leaked")
		}
	})
	monitorStarted = true
	go m.monitor(s, reader, captureDone, exits)
	select {
	case r := <-transitions:
		if r.Lifecycle != model.Running {
			t.Fatalf("initial reconciliation failed: %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("initial reconciliation did not run")
	}
	return s, exits, transitions
}

func TestMonitorSleepsUntilNotification(t *testing.T) {
	s, exits, transitions := controlledMonitor(t, time.Hour)
	s.mu.Lock()
	s.run.Identity.StartTicks++
	s.mu.Unlock()
	select {
	case r := <-transitions:
		t.Fatalf("idle monitor checked without a notification: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
	exits <- syscall.SIGCHLD
	select {
	case r := <-transitions:
		if r.Lifecycle != model.Unknown || r.Error == "" {
			t.Fatalf("signal bypassed identity verification: %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("notification did not wake monitor")
	}
}

func TestMonitorFallbackCompletesWithoutNotification(t *testing.T) {
	s, _, _ := controlledMonitor(t, 50*time.Millisecond)
	if err := s.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("fallback did not reconcile a lost notification")
	}
	if s.run.Lifecycle != model.Exited || s.run.Outcome == nil || s.run.Outcome.Kind != model.Signaled {
		t.Fatalf("lost fallback outcome: %+v", s.run)
	}
}
