//go:build linux

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nullco/lazyrun/internal/model"
)

func TestDashboardReloadKeyUpdatesConfigWithoutChangingRunningCommands(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	f := newIntegration(t, pulseConfig)
	run := f.start("pulse", nil)
	f.ready("pulse", run)
	cmd, master := terminalClient(t, f.root)
	out := watchTerminal(t, master)
	eventuallyIntegration(t, func() bool { return out.contains("Services") })

	// Added and changed definitions appear without reopening the dashboard.
	changed := "name: reloaded-project\n" + pulseConfig + "  added:\n    command: echo added\n"
	changed = strings.Replace(changed, "echo ready", "echo changed", 1)
	f.write(changed)
	keys(t, master, "R") // global action from the initial Project pane
	eventuallyIntegration(t, func() bool {
		s := f.state()
		return s.Project.Name == "reloaded-project" && findRun(s, "added").Definition.Alias == "added" && out.contains("Config reloaded")
	})
	current := findRun(f.state(), "pulse")
	if current.ID != run.ID || current.Identity != run.Identity || current.StopRequested || !strings.Contains(current.Definition.Command, "echo ready") {
		t.Fatal("reload altered running command", current)
	}
	if findRun(f.state(), "added").ID != "" {
		t.Fatal("reload started an added task")
	}
	keys(t, master, "3")
	eventuallyIntegration(t, func() bool { return out.contains("added") })

	// Validation failures leave the last valid project available.
	out.clear()
	f.write("version: [")
	keys(t, master, "R") // reload also works in Tasks
	eventuallyIntegration(t, func() bool { return out.contains("Config reload failed") })
	if f.state().Project.Name != "reloaded-project" {
		t.Fatal("invalid config replaced valid state")
	}

	// Missing config must not silently load a parent project's file.
	if err := os.WriteFile(filepath.Join(filepath.Dir(f.root), "lazyrun.yml"), []byte("version: 1\nname: parent\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.root, "lazyrun.yml")); err != nil {
		t.Fatal(err)
	}
	out.clear()
	keys(t, master, "R")
	eventuallyIntegration(t, func() bool { return out.contains("Config reload failed") })
	if f.state().Project.Name != "reloaded-project" {
		t.Fatal("missing config changed project")
	}

	// Removed running services stay visible and can still be stopped.
	f.write("version: 1\nname: removed-project\n")
	out.clear()
	keys(t, master, "2R")
	eventuallyIntegration(t, func() bool {
		s := f.state()
		return s.Project.Name == "removed-project" && len(s.Commands) == 1 && s.Commands[0].Removed && out.contains("Config reloaded")
	})
	current = findRun(f.state(), "pulse")
	if current.ID != run.ID || current.Identity != run.Identity || current.Lifecycle != model.Running || current.StopRequested {
		t.Fatal("removing definition stopped command", current)
	}
	keys(t, master, "s")
	eventuallyIntegration(t, func() bool { return len(f.state().Commands) == 0 })
	keys(t, master, "q")
	if err := waitClient(t, cmd); err != nil {
		t.Fatal(err)
	}
}
