//go:build linux

package app

import (
	"context"
	"testing"

	"github.com/nullco/lazyrun/internal/model"
	"golang.org/x/sys/unix"
)

func TestDashboardResponsiveLayoutsMouseSearchAndResize(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	f := newIntegration(t, `version: 1
services:
  alpha: {command: sleep 3600}
  beta: {command: "printf 'COMPACT-READY\n'; sleep 3600"}
tasks:
  one: {command: "printf 'TASK-ONE\n'"}
  two: {command: "printf 'TASK-TWO\n'"}
`)
	cmd, master := terminalClient(t, f.root)
	out := watchTerminal(t, master)
	eventuallyIntegration(t, func() bool { return out.contains("Services") && out.contains("Tasks") })
	resize := func(width, height int) {
		t.Helper()
		out.clear()
		if err := unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: uint16(height), Col: uint16(width)}); err != nil {
			t.Fatal(err)
		}
	}
	resize(40, 12)
	eventuallyIntegration(t, func() bool { return out.contains("Project") && out.contains("q: quit") })
	out.clear()
	terminalMouse(t, master, 0, 4, 4) // Project expanded; Services is a one-row header.
	eventuallyIntegration(t, func() bool { return out.contains("S: start") })
	out.clear()
	terminalMouse(t, master, 0, 4, 3) // Expanded Services: select beta, the second row.
	eventuallyIntegration(t, func() bool { return out.contains("beta:") })
	keys(t, master, "S")
	var run model.Run
	eventuallyIntegration(t, func() bool {
		run = findRun(f.state(), "beta")
		return run.Lifecycle == model.Running
	})
	f.remember(run)
	eventuallyIntegration(t, func() bool { return out.contains("running") && out.contains("COMPACT-READY") })
	out.clear()
	keys(t, master, "\r") // Enlarge the always-visible Logs pane, preserving beta as owner.
	eventuallyIntegration(t, func() bool { return out.contains("COMPACT-READY") && out.contains("/: search") })
	out.clear()
	keys(t, master, "/COMPACT-READY")
	eventuallyIntegration(t, func() bool { return out.contains("Filter:") && out.contains("COMPACT-READY") })
	// Reflow an open editor into the short-screen sidebar layout.
	resize(100, 12)
	eventuallyIntegration(t, func() bool { return out.contains("Filter:") && out.contains("COMPACT-READY") })
	keys(t, master, "\r")
	eventuallyIntegration(t, func() bool { return out.contains("n/N: jump") })
	resize(100, 24)
	eventuallyIntegration(t, func() bool { return out.contains("n/N: jump") && out.contains("COMPACT-READY") })
	resize(40, 10)
	eventuallyIntegration(t, func() bool { return out.contains("n/N: jump") && out.contains("COMPACT-READY") })
	resize(40, 12) // Leave room for both task rows while retaining the output preview.
	eventuallyIntegration(t, func() bool { return out.contains("COMPACT-READY") })
	out.clear()
	terminalMouse(t, master, 0, 4, 2) // With Logs expanded, Tasks is the third header.
	eventuallyIntegration(t, func() bool { return out.contains("one:") })
	out.clear()
	terminalMouse(t, master, 0, 4, 4) // Expanded Tasks: second row is two.
	keys(t, master, "S")
	var task model.Run
	eventuallyIntegration(t, func() bool {
		task = findRun(f.state(), "two")
		return task.ID != "" && task.Lifecycle == model.Exited
	})
	f.remember(task)
	eventuallyIntegration(t, func() bool { return out.contains("completed") && out.contains("TASK-TWO") })
	out.clear()
	terminalMouse(t, master, 0, 4, 7) // Click the visible preview to enlarge Logs/Details.
	eventuallyIntegration(t, func() bool { return out.contains("TASK-TWO") })
	keys(t, master, "q")
	if err := waitClient(t, cmd); err != nil {
		t.Fatal(err)
	}
	current := findRun(f.state(), "beta")
	if current.ID != run.ID || current.Identity != run.Identity || current.Lifecycle != model.Running {
		t.Fatal("responsive navigation changed the running service", current)
	}
	if _, err := f.connection.Client.Stop(context.Background(), "beta"); err != nil {
		t.Fatal(err)
	}
	f.finished("beta", run)
}
