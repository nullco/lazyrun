//go:build linux

package runtime

import (
	"errors"
	"testing"

	"github.com/nullco/lazyrun/internal/model"
)

func TestRecoveredRunClearsOnLifecycleRequestsAfterCleanup(t *testing.T) {
	for _, action := range []string{"start", "restart", "stop", "removed"} {
		t.Run(action, func(t *testing.T) {
			owned := manager(t, map[string]string{"x": fixture(t, "service")}, Options{})
			old := start(t, owned, "x")
			ready(t, owned, "x", old)
			p := owned.State().Project
			p.Tasks[0].Command = "printf replacement"
			recovered, err := New(p, Options{LogStore: testLogStore(t)})
			if err != nil {
				t.Fatal(err)
			}
			if err := recovered.Restore([]model.Run{old}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { recovered.slots["x"].diskOutput.Release() })
			if action == "removed" {
				p.Tasks = nil
				if err := recovered.Sync(p); err != nil {
					t.Fatal(err)
				}
				state := recovered.State()
				if len(state.Commands) != 1 || !state.Commands[0].Removed || state.Commands[0].Run.Lifecycle != model.Unknown {
					t.Fatal("removed live recovered run disappeared", state)
				}
			}
			if _, err := owned.Stop("x"); err != nil {
				t.Fatal(err)
			}
			wait(t, owned, "x", old)
			// Do not poll State before the action: headless clients must not
			// require a dashboard poll to clear the stale blocker.
			switch action {
			case "start", "restart":
				var next model.Run
				if action == "start" {
					next, err = recovered.Start("x", nil)
				} else {
					next, err = recovered.Restart("x", nil)
				}
				if err != nil || next.ID == old.ID {
					t.Fatal("manual cleanup did not unblock the action", next, err)
				}
				wait(t, recovered, "x", next)
				if output(t, recovered, "x", next) != "replacement" {
					t.Fatal("replacement ignored the current configuration")
				}
			case "stop":
				cleared, err := recovered.Stop("x")
				if !errors.Is(err, ErrNoActiveRun) || cleared.Lifecycle != model.Exited || cleared.Outcome != nil || cleared.EndedAt != nil {
					t.Fatal("stop retained a stale blocker or fabricated an outcome", cleared, err)
				}
			case "removed":
				if state := recovered.State(); len(state.Commands) != 0 {
					t.Fatal("removed recovered run remained visible after cleanup", state)
				}
			}
		})
	}
}
