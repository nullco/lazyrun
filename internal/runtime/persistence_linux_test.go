//go:build linux

package runtime

import (
	"errors"
	"strings"
	"testing"

	"github.com/nullco/lazyrun/internal/model"
)

func TestPostLaunchPersistenceFailureDoesNotLoseOwnershipOrCapture(t *testing.T) {
	calls := 0
	m := manager(t, map[string]string{"x": fixture(t, "service")}, Options{Persist: func(model.Run) error {
		calls++
		if calls > 1 {
			return errors.New("disk unavailable")
		}
		return nil
	}})
	r, err := m.Start("x", nil)
	if err == nil || r.Identity.PID == 0 || r.Lifecycle != model.Running || !strings.Contains(r.MetadataError, "disk unavailable") {
		t.Fatalf("lost accepted launch identity: %+v %v", r, err)
	}
	ready(t, m, "x", r)
	if _, err := m.Start("x", nil); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatal("metadata failure permitted duplicate", err)
	}
	stopped, err := m.Stop("x")
	if err == nil || !stopped.StopRequested {
		t.Fatal("metadata failure concealed graceful stop")
	}
	finished := wait(t, m, "x", r)
	if finished.Outcome == nil || finished.Outcome.Kind != model.Success || !strings.Contains(output(t, m, "x", r), "final") || finished.MetadataError == "" {
		t.Fatal("persistence failure stopped capture or concealed error", finished)
	}
}
