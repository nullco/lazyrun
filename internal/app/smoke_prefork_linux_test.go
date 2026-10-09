//go:build linux

package app

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/nullco/lazyrun/internal/transport"
)

// This deliberately tests default prefork behavior rather than monkey-patching
// its child signal handlers to promise a parent-only warm shutdown. Group-wide
// SIGTERM may abort tasks; lazyrun never signals just a selected parent PID.
func TestSmokeCeleryBusyPreforkGroupTERM(t *testing.T) {
	python := smokePython(t)
	f := smokeProject(t, python)
	run := f.start("worker", os.Environ())
	smokeEventually(t, f, "busy prefork readiness", func() bool { return strings.Contains(f.logs("worker", run), "ready.") })
	enqueueSmoke(t, f, python, "prefork", 6, "prefork-term")
	smokeEventually(t, f, "busy prefork child", func() bool { _, ok := smokeTaskIdentity(f.root, "started", "prefork-term"); return ok })
	blocked, err := f.connection.Client.Restart(context.Background(), "worker", os.Environ())
	if !remoteCode(err, transport.CodeRestartBlocked) || blocked.ID != run.ID {
		t.Fatal("prefork shutdown did not retain its old group while reporting worker loss", blocked, err)
	}
	assertSmokeFinished(t, f, "worker", run)
	logs := f.logs("worker", run)
	if !strings.Contains(logs, "Warm shutdown") || !strings.Contains(logs, "signal 15 (SIGTERM)") || !strings.Contains(logs, "ForkPoolWorker") {
		t.Fatal("expected Celery's own child SIGTERM diagnosis, not a fabricated lazyrun outcome", logs)
	}
	if _, done := smokeTaskIdentity(f.root, "done", "prefork-term"); done {
		t.Fatal("pinned default prefork signal behavior changed; review shutdown guidance")
	}
	t.Log("Verified default Celery prefork child abort on group SIGTERM; parent-only warm shutdown is not promised")
}
