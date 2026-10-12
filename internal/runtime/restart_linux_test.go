//go:build linux

package runtime

import (
	"errors"
	"testing"
	"time"

	"github.com/nullco/lazyrun/internal/model"
)

func TestRestartUsesCurrentServiceTimeout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout time.Duration
		blocked bool
	}{{"short", 100 * time.Millisecond, true}, {"long", 3 * time.Second, false}} {
		t.Run(tc.name, func(t *testing.T) {
			m := manager(t, map[string]string{"x": fixture(t, "slow-service")}, Options{})
			p := m.State().Project
			p.Services, p.Tasks = p.Tasks, nil
			p.Services[0].Kind = model.Service
			p.Services[0].RestartTimeout = 10 * time.Second
			p.Services[0].Env = map[string]string{"SHUTDOWN_DELAY": "350ms"}
			if err := m.Sync(p); err != nil {
				t.Fatal(err)
			}
			old := start(t, m, "x")
			ready(t, m, "x", old)
			p.Services[0].RestartTimeout = tc.timeout
			if err := m.Sync(p); err != nil {
				t.Fatal(err)
			}
			state := m.State().Commands[0]
			if state.Run.Definition.RestartTimeout != 10*time.Second || state.Definition.RestartTimeout != tc.timeout {
				t.Fatal("reload rewrote the run's immutable timeout", state)
			}
			next, err := m.Restart("x", nil)
			if tc.blocked {
				if !errors.Is(err, ErrRestartBlocked) || next.ID != old.ID || next.Lifecycle != model.Stopping {
					t.Fatal("short timeout did not cancel replacement", next, err)
				}
				wait(t, m, "x", old)
				if current, _ := snapshot(m, "x"); current.ID != old.ID || current.Lifecycle != model.Exited {
					t.Fatal("canceled replacement started later", current)
				}
				return
			}
			if err != nil || next.ID == old.ID || next.Definition.RestartTimeout != tc.timeout {
				t.Fatal("long timeout did not allow graceful restart", next, err)
			}
			ready(t, m, "x", next)
			stopped, err := m.Stop("x")
			if err != nil || stopped.Lifecycle != model.Stopping || stopped.Outcome != nil {
				t.Fatal("stop unexpectedly waited for shutdown", stopped, err)
			}
			wait(t, m, "x", next)
		})
	}
}

func TestSyncRejectsNegativeRestartTimeout(t *testing.T) {
	m := manager(t, map[string]string{"x": "echo ok"}, Options{})
	p := m.State().Project
	p.Tasks[0].RestartTimeout = -time.Second
	if err := m.Sync(p); err == nil {
		t.Fatal("negative restart timeout accepted")
	}
	if m.State().Commands[0].Definition.RestartTimeout < 0 {
		t.Fatal("invalid sync changed the current definition")
	}
}
