//go:build linux

package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSmokeCeleryThreadsRestartAllowsLongerGracefulShutdown(t *testing.T) {
	python := smokePython(t)
	f := smokeProject(t, python)
	enqueueSmoke(t, f, python, "threads", 6, "restart")
	old := f.start("worker", os.Environ())
	smokeEventually(t, f, "threads worker readiness", func() bool { return strings.Contains(f.logs("worker", old), "ready.") })
	var running smokeIdentity
	smokeEventually(t, f, "threads task start", func() bool {
		var ok bool
		running, ok = smokeTaskIdentity(f.root, "started", "restart")
		return ok
	})
	data, err := os.ReadFile(filepath.Join(f.root, "lazyrun.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// The running worker keeps its original 3s definition, but the next
	// restart must use the newly configured grace period.
	data = []byte(strings.Replace(string(data), "restart_timeout: 3s", "restart_timeout: 15s", 1))
	f.write(string(data))
	if err := f.connection.Client.Sync(context.Background(), data); err != nil {
		t.Fatal(err)
	}
	next, err := f.connection.Client.Restart(context.Background(), "worker", os.Environ())
	if err != nil {
		t.Fatal("configured grace period did not allow task completion", err)
	}
	f.remember(next)
	if next.ID == old.ID || unix.Kill(-old.Identity.PGID, 0) != unix.ESRCH {
		t.Fatal("replacement overlapped the old worker", next, old)
	}
	if done, ok := smokeTaskIdentity(f.root, "done", "restart"); !ok || done != running {
		t.Fatal("restart interrupted or reran the old worker's task", done, running)
	}
	smokeEventually(t, f, "replacement worker readiness", func() bool { return strings.Contains(f.logs("worker", next), "ready.") })
	if _, err := f.connection.Client.Stop(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	assertSmokeFinished(t, f, "worker", next)
}
