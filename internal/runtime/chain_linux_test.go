//go:build linux

package runtime

import (
	"strings"
	"testing"
	"time"

	"lazyrun/internal/model"
)

func TestRapidOrphanChainCannotDisappearBetweenScans(t *testing.T) {
	m := manager(t, map[string]string{"x": fixture(t, "chain")}, Options{PollInterval: time.Millisecond})
	r, err := m.Start("x", []string{"CHAIN_DEPTH=0"})
	if err != nil {
		t.Fatal(err)
	}
	ready(t, m, "x", r)
	eventually(t, func() bool { p, err := readStat(r.Identity.PID); return err == nil && p.state == 'Z' })
	state, _ := m.Snapshot("x")
	if state.Lifecycle != model.Running {
		t.Fatalf("prematurely finished a live descendant chain: %+v", state)
	}
	if _, err := m.Stop("x"); err != nil {
		t.Fatal(err)
	}
	finished := wait(t, m, "x", r)
	text := output(t, m, "x", r)
	if strings.Count(text, "generation:") != 26 || !strings.Contains(text, "chain-final") || finished.Outcome.Kind != model.Success {
		t.Fatalf("lost chain/outcome: %s %+v", text, finished)
	}
}
