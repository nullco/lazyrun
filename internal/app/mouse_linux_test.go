//go:build linux

package app

import (
	"fmt"
	"os"
	"testing"

	"github.com/nullco/lazyrun/internal/model"
)

// SGR mouse reports use one-based screen coordinates. Send real terminal input
// through tcell/gocui rather than directly invoking the dashboard's handlers.
func terminalMouse(t *testing.T, master *os.File, button, x, y int) {
	t.Helper()
	report := fmt.Sprintf("\x1b[<%d;%d;%dM", button, x+1, y+1)
	if button == 0 {
		report += fmt.Sprintf("\x1b[<0;%d;%dm", x+1, y+1)
	}
	keys(t, master, report)
}

func TestDashboardMouseFocusSelectionWheelAndModal(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	f := newIntegration(t, `version: 1
services:
  alpha: {command: sleep 3600}
  beta: {command: sleep 3600}
tasks:
  success: {command: "printf 'mouse success\\n'"}
  failure: {command: "printf 'mouse failure\\n'; exit 4"}
`)
	cmd, master := terminalClient(t, f.root) // fixed 100×24 terminal
	out := watchTerminal(t, master)
	eventuallyIntegration(t, func() bool { return out.contains("Services") && out.contains("Tasks") })
	out.clear()
	terminalMouse(t, master, 0, 4, 16) // second task row: failure
	eventuallyIntegration(t, func() bool { return out.contains("failure:") })
	for _, item := range f.state().Commands {
		if item.Run.ID != "" {
			t.Fatal("click auto-started a command", item)
		}
	}
	keys(t, master, "S")
	var failed model.Run
	eventuallyIntegration(t, func() bool { failed = findRun(f.state(), "failure"); return failed.Lifecycle == model.Exited })
	f.remember(failed)
	eventuallyIntegration(t, func() bool { return out.contains("failed (4)") })
	if failed.Outcome == nil || failed.Outcome.Kind != model.NonzeroExit || failed.Outcome.ExitCode == nil || *failed.Outcome.ExitCode != 4 || findRun(f.state(), "success").ID != "" {
		t.Fatal("keyboard action did not use mouse-selected alias", failed)
	}
	out.clear()
	terminalMouse(t, master, 0, 4, 8) // second service row: beta
	eventuallyIntegration(t, func() bool { return out.contains("beta:") })
	out.clear()
	terminalMouse(t, master, 64, 4, 8) // wheel up targets the hovered list
	eventuallyIntegration(t, func() bool { return out.contains("alpha:") })
	out.clear()
	terminalMouse(t, master, 0, 40, 2)  // focus Logs; preserve alpha as owner
	terminalMouse(t, master, 65, 40, 2) // wheel down pauses following
	eventuallyIntegration(t, func() bool { return out.contains("PAUSED") })
	keys(t, master, "?")
	eventuallyIntegration(t, func() bool { return out.contains("Navigation") })
	out.clear()
	terminalMouse(t, master, 65, 40, 4) // wheel scrolls the modal itself
	eventuallyIntegration(t, func() bool { return out.contains("q / Ctrl-C") })
	terminalMouse(t, master, 0, 4, 16) // popup must block underlying task selection
	keys(t, master, "?")
	out.clear()
	keys(t, master, "S") // owner must still be alpha, not failure
	var running model.Run
	eventuallyIntegration(t, func() bool { running = findRun(f.state(), "alpha"); return running.Lifecycle == model.Running })
	f.remember(running)
	eventuallyIntegration(t, func() bool { return out.contains("running") })
	if findRun(f.state(), "beta").ID != "" || findRun(f.state(), "failure").ID != failed.ID {
		t.Fatal("modal click changed the action target")
	}
	keys(t, master, "s")
	f.finished("alpha", running)
	eventuallyIntegration(t, func() bool { return out.contains("stopped") })
	out.clear()
	terminalMouse(t, master, 0, 4, 15) // first task row: success
	eventuallyIntegration(t, func() bool { return out.contains("success:") })
	terminalMouse(t, master, 0, 34, 8) // blank column between panes: no focus change
	keys(t, master, "S")
	var success model.Run
	eventuallyIntegration(t, func() bool { success = findRun(f.state(), "success"); return success.Lifecycle == model.Exited })
	f.remember(success)
	if success.Outcome == nil || success.Outcome.Kind != model.Success {
		t.Fatal("gap click changed the selected command", success)
	}
	keys(t, master, "q")
	if err := waitClient(t, cmd); err != nil {
		t.Fatal(err)
	}
}
