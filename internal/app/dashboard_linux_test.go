//go:build linux

package app

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nullco/lazyrun/internal/model"
	"golang.org/x/sys/unix"
)

// Keep draining a real terminal so rendering cannot be blocked by test readers.
// The observation buffer is independently bounded; it is not a terminal emulator.
type terminalOutput struct {
	mu   sync.Mutex
	data []byte
}

func watchTerminal(t *testing.T, master *os.File) *terminalOutput {
	t.Helper()
	out := &terminalOutput{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		b := make([]byte, 8192)
		for {
			n, err := master.Read(b)
			if n > 0 {
				out.mu.Lock()
				out.data = append(out.data, b[:n]...)
				if len(out.data) > 512*1024 {
					out.data = append([]byte(nil), out.data[len(out.data)-512*1024:]...)
				}
				out.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		master.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("terminal reader did not stop")
		}
	})
	return out
}
func (o *terminalOutput) contains(text string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return strings.Contains(string(o.data), text)
}
func (o *terminalOutput) clear() { o.mu.Lock(); o.data = nil; o.mu.Unlock() }
func keys(t *testing.T, master *os.File, text string) {
	t.Helper()
	if _, err := master.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
}
func waitClient(t *testing.T, cmd *exec.Cmd) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(8 * time.Second):
		cmd.Process.Kill()
		<-done
		t.Fatal("dashboard failed to quit promptly")
		return nil
	}
}
func TestDashboardKeyboardActionsReconnectResizeAndQuit(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("SNAPSHOT", "old-supervisor-environment")
	f := newIntegration(t, pulseConfig)
	t.Setenv("SNAPSHOT", "dashboard")
	cmd, master := terminalClient(t, f.root)
	out := watchTerminal(t, master)
	eventuallyIntegration(t, func() bool { return out.contains("Services") && out.contains("Tasks") })
	for _, item := range f.state().Commands {
		if item.Run.ID != "" {
			t.Fatal("opening dashboard auto-started", item)
		}
	}
	keys(t, master, "S")
	eventuallyIntegration(t, func() bool { return out.contains("Select a service or task") })
	keys(t, master, "2?S")
	eventuallyIntegration(t, func() bool { return out.contains("Keybindings") })
	if findRun(f.state(), "pulse").ID != "" {
		t.Fatal("help popup started command")
	}
	keys(t, master, "?S")
	var run model.Run
	eventuallyIntegration(t, func() bool { run = findRun(f.state(), "pulse"); return run.Lifecycle == model.Running })
	f.remember(run)
	f.ready("pulse", run)
	eventuallyIntegration(t, func() bool { return out.contains("ready") })
	keys(t, master, "3S")
	var task model.Run
	eventuallyIntegration(t, func() bool { task = findRun(f.state(), "env"); return task.ID != "" && task.Lifecycle == model.Exited })
	f.remember(task)
	if got := f.logs("env", task); got != "SNAPSHOT=dashboard OVERRIDE=configured" {
		t.Fatal(got)
	}
	eventuallyIntegration(t, func() bool { return out.contains("completed") && out.contains("SNAPSHOT=dashboard") })
	keys(t, master, "2\rkr") // focus logs, pause, and explicitly restart selected service
	eventuallyIntegration(t, func() bool {
		current := findRun(f.state(), "pulse")
		return current.ID == run.ID && current.StopRequested
	})
	var replacement model.Run
	eventuallyIntegration(t, func() bool {
		replacement = findRun(f.state(), "pulse")
		return replacement.ID != run.ID && replacement.Lifecycle == model.Running
	})
	f.remember(replacement)
	f.ready("pulse", replacement)
	out.clear()
	keys(t, master, "k")
	eventuallyIntegration(t, func() bool { return out.contains("PAUSED") })
	out.clear()
	keys(t, master, "G")
	eventuallyIntegration(t, func() bool { return out.contains("following") })
	out.clear()
	if err := unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 9, Col: 39}); err != nil {
		t.Fatal(err)
	}
	eventuallyIntegration(t, func() bool { return out.contains("Terminal too small") })
	keys(t, master, "s") // minimum-size mode must not accidentally signal commands
	if err := unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 30, Col: 120}); err != nil {
		t.Fatal(err)
	}
	eventuallyIntegration(t, func() bool { return out.contains("Services") })
	keys(t, master, "q")
	if err := waitClient(t, cmd); err != nil {
		t.Fatal(err)
	}
	current := findRun(f.state(), "pulse")
	if current.ID != replacement.ID || current.Identity != replacement.Identity || current.StopRequested {
		t.Fatal("resize/quit changed managed command", current)
	}
	before, err := f.connection.Client.Logs(context.Background(), "pulse", current.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	eventuallyIntegration(t, func() bool {
		r, e := f.connection.Client.Logs(context.Background(), "pulse", current.ID, before.Next, 0)
		return e == nil && len(r.Data) > 0
	})
	reopened, terminal := terminalClient(t, f.root)
	again := watchTerminal(t, terminal)
	eventuallyIntegration(t, func() bool { return again.contains("Services") })
	if findRun(f.state(), "pulse").Identity != replacement.Identity {
		t.Fatal("reopening replaced command")
	}
	keys(t, terminal, "2s")
	f.finished("pulse", replacement)
	keys(t, terminal, "\x03")
	if err := waitClient(t, reopened); err != nil {
		t.Fatal(err)
	}
}
func TestDashboardQuitDuringAcceptedRestartDoesNotCancelReplacement(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	f := newIntegration(t, pulseConfig)
	run := f.start("pulse", nil)
	f.ready("pulse", run)
	cmd, master := terminalClient(t, f.root)
	out := watchTerminal(t, master)
	eventuallyIntegration(t, func() bool { return out.contains("Services") })
	keys(t, master, "2r")
	eventuallyIntegration(t, func() bool {
		current := findRun(f.state(), "pulse")
		return current.ID == run.ID && current.StopRequested
	})
	keys(t, master, "q")
	if err := waitClient(t, cmd); err != nil {
		t.Fatal(err)
	}
	var replacement model.Run
	eventuallyIntegration(t, func() bool {
		replacement = findRun(f.state(), "pulse")
		return replacement.ID != run.ID && replacement.Lifecycle == model.Running
	})
	f.remember(replacement)
	f.ready("pulse", replacement)
}

func TestDashboardCrashHangupAndSignalDoNotStopCommands(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	f := newIntegration(t, pulseConfig)
	run := f.start("pulse", nil)
	f.ready("pulse", run)
	for _, mode := range []string{"crash", "hangup", "signal"} {
		cmd, master := terminalClient(t, f.root)
		out := watchTerminal(t, master)
		eventuallyIntegration(t, func() bool { return out.contains("Services") })
		keys(t, master, "2")
		switch mode {
		case "crash":
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
		case "hangup":
			master.Close()
		case "signal":
			if err := cmd.Process.Signal(unix.SIGTERM); err != nil {
				t.Fatal(err)
			}
		}
		err := waitClient(t, cmd)
		if mode == "signal" && err != nil {
			t.Fatal("signal did not gracefully close dashboard", err)
		}
		current := findRun(f.state(), "pulse")
		if current.ID != run.ID || current.Identity != run.Identity || current.Lifecycle != model.Running {
			t.Fatal(mode, current)
		}
	}
}
func TestDefaultDashboardRequiresTerminalWithoutLaunchingSupervisor(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", base)
	t.Setenv("XDG_STATE_HOME", base+"/state")
	cmd := exec.Command(testExecutable)
	cmd.Dir = base
	b, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(b), "use --state") {
		t.Fatal(string(b), err)
	}
	if _, err := os.Stat(base + "/lazyrun"); !os.IsNotExist(err) {
		t.Fatal("non-terminal default launched supervisor", err)
	}
}
