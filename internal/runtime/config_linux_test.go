//go:build linux

package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nullco/lazyrun/internal/model"
)

func TestSyncPreservesActiveSnapshotMovesAndRemovedRuns(t *testing.T) {
	m := manager(t, map[string]string{"a": fixture(t, "service"), "removed": fixture(t, "service")}, Options{})
	a := start(t, m, "a")
	old := start(t, m, "removed")
	ready(t, m, "a", a)
	ready(t, m, "removed", old)
	p := m.State().Project
	p.Tasks = nil
	p.Services = []model.Definition{{Alias: "a", Kind: model.Service, Command: "printf new-definition", Cwd: p.Root}}
	if err := m.Sync(p); err != nil {
		t.Fatal(err)
	}
	state := m.State()
	if len(state.Commands) != 2 {
		t.Fatal(state)
	}
	moved := state.Commands[0]
	if moved.Definition.Kind != model.Service || moved.Run.Definition.Kind != model.Task || moved.DisplayKind() != model.Task || moved.Run.ID != a.ID {
		t.Fatalf("active definition rewritten: %+v", moved)
	}
	if !state.Commands[1].Removed || state.Commands[1].Run.ID != old.ID {
		t.Fatal("lost removed active alias")
	}
	if _, err := m.Restart("removed", nil); !errors.Is(err, ErrRemoved) {
		t.Fatal(err)
	}
	if _, err := m.Stop("removed"); err != nil {
		t.Fatal(err)
	}
	wait(t, m, "removed", old)
	if len(m.State().Commands) != 1 {
		t.Fatal("finished removed alias remained in list")
	}
	next, err := m.Restart("a", os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	wait(t, m, "a", next)
	if next.Definition.Kind != model.Service || output(t, m, "a", next) != "new-definition" {
		t.Fatal("next run ignored synchronized definition")
	}
}

func TestDurableIntentPrecedesExecutionAndErrorsRemainVisible(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "must-not-exist")
	m := manager(t, map[string]string{"x": "touch '" + marker + "'"}, Options{Persist: func(model.Run) error { return errors.New("disk full") }})
	r, err := m.Start("x", nil)
	if err == nil || r.Outcome.Kind != model.LaunchFailed || !strings.Contains(r.MetadataError, "disk full") {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("executed without durable launch intent")
	}
	var mu sync.Mutex
	var states []model.Run
	m = manager(t, map[string]string{"x": fixture(t, "service")}, Options{Persist: func(r model.Run) error { mu.Lock(); defer mu.Unlock(); states = append(states, r); return nil }})
	r = start(t, m, "x")
	ready(t, m, "x", r)
	if _, err := m.Stop("x"); err != nil {
		t.Fatal(err)
	}
	wait(t, m, "x", r)
	mu.Lock()
	defer mu.Unlock()
	if len(states) < 4 || states[0].Lifecycle != model.Starting || states[0].Identity.PID != 0 || states[0].Identity.BootID == "" || states[1].Identity.PID == 0 || states[len(states)-1].Lifecycle != model.Exited {
		t.Fatalf("non-durable lifecycle: %+v", states)
	}
}

func TestRestoreDoesNotOwnOrSignalRecordedProcesses(t *testing.T) {
	owned := manager(t, map[string]string{"x": fixture(t, "service")}, Options{})
	r := start(t, owned, "x")
	ready(t, owned, "x", r)
	p := owned.State().Project
	recovered, err := New(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.Restore([]model.Run{r}); err != nil {
		t.Fatal(err)
	}
	state, _ := recovered.Snapshot("x")
	if state.Lifecycle != model.Unknown || state.Outcome != nil {
		t.Fatal(state)
	}
	for _, action := range []func() (model.Run, error){func() (model.Run, error) { return recovered.Start("x", nil) }, func() (model.Run, error) { return recovered.Stop("x") }, func() (model.Run, error) { return recovered.Restart("x", nil) }} {
		if _, err := action(); !errors.Is(err, ErrUnmanaged) {
			t.Fatal("restored PID granted ownership", err)
		}
	}
	current, _ := owned.Snapshot("x")
	if current.StopRequested {
		t.Fatal("recovery signaled old run")
	}
	if _, err := owned.Stop("x"); err != nil {
		t.Fatal(err)
	}
	wait(t, owned, "x", r)
	reconciled, err := ReconcileRecorded(r)
	if err != nil || reconciled.Lifecycle != model.Exited || reconciled.Outcome != nil || reconciled.EndedAt != nil {
		t.Fatalf("fabricated outcome or retained stale execution: %+v %v", reconciled, err)
	}
	r.Identity.BootID = "previous-boot"
	reconciled, err = ReconcileRecorded(r)
	if err != nil || reconciled.Lifecycle != model.Exited {
		t.Fatal(reconciled, err)
	}
	r.Identity = model.ProcessIdentity{}
	reconciled, err = ReconcileRecorded(r)
	if err != nil || reconciled.Lifecycle != model.Unknown {
		t.Fatal("incomplete intent did not block", reconciled, err)
	}
}

func TestLogCursorsAreRunScopedAndBounded(t *testing.T) {
	m := manager(t, map[string]string{"x": fixture(t, "volume")}, Options{})
	r := wait(t, m, "x", start(t, m, "x"))
	read, err := m.ReadOutput("x", r.ID, 0, 10)
	if err != nil || !read.Truncated || len(read.Data) != 10 || read.Next <= 10 || read.RunID != r.ID {
		t.Fatal(read, err)
	}
	next, err := m.ReadOutput("x", r.ID, read.Next, 10)
	if err != nil || next.Truncated || next.Next != read.Next+10 {
		t.Fatal(next, err)
	}
	if _, err := m.ReadOutput("x", r.ID, 0, MaxLogRead+1); !errors.Is(err, ErrCursor) {
		t.Fatal(err)
	}
	if _, err := m.ReadOutput("x", "wrong-run", 0, 10); !errors.Is(err, ErrRunChanged) {
		t.Fatal(err)
	}
	if _, err := m.Wait(context.Background(), "x", "wrong-run"); !errors.Is(err, ErrRunChanged) {
		t.Fatal(err)
	}
}
