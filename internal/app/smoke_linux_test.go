//go:build linux

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nullco/lazyrun/internal/model"
	"github.com/nullco/lazyrun/internal/runtime"
	"github.com/nullco/lazyrun/internal/transport"
	"golang.org/x/sys/unix"
)

// Opt-in because regular Go tests must not install Python or contact a broker.
// make smoke refuses an unset interpreter instead of silently skipping this gate.
func smokePython(t *testing.T) string {
	t.Helper()
	python := os.Getenv("LAZYRUN_SMOKE_PYTHON")
	if python == "" {
		t.Skip("set LAZYRUN_SMOKE_PYTHON to a venv interpreter; see examples/smoke")
	}
	if !filepath.IsAbs(python) {
		t.Fatal("LAZYRUN_SMOKE_PYTHON must be absolute")
	}
	cmd := exec.Command(python, "-c", "from importlib.metadata import version; print('Flask=' + version('Flask'), 'Werkzeug=' + version('Werkzeug'), 'Celery=' + version('celery'), 'billiard=' + version('billiard'))")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("smoke dependencies unavailable: %v\n%s", err, output)
	}
	t.Log(strings.TrimSpace(string(output)))
	return python
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
func smokeProject(t *testing.T, python string) *integration {
	t.Helper()
	p := shellQuote(python)
	f := newIntegration(t, fmt.Sprintf(`version: 1
logs: {tail: 500, maxBytes: 1048576}
services:
  api:
    command: %q
  worker:
    command: %q
tasks:
  enqueue:
    command: %q
`, p+" -u smoke_app.py serve", p+" -m celery -A smoke_app.celery worker --pool=prefork --concurrency=2 --loglevel=INFO --without-gossip --without-mingle --without-heartbeat", p+" -u smoke_app.py enqueue --seconds 1 --token initial"))
	data, err := os.ReadFile("../../examples/smoke/smoke_app.py")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "smoke_app.py"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SMOKE_ROOT", f.root)
	t.Setenv("SMOKE_PORT", "0")
	for _, item := range f.state().Commands {
		if item.Run.ID != "" {
			t.Fatal("opening executed a real application", item)
		}
	}
	return f
}
func smokeEventually(t *testing.T, f *integration, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, item := range f.state().Commands {
		t.Logf("%s: %+v", item.Run.Definition.Alias, item.Run)
		if item.Run.ID != "" {
			t.Logf("%s logs: %s", item.Run.Definition.Alias, f.logs(item.Run.Definition.Alias, item.Run))
			if item.Run.Identity.PGID > 1 {
				for _, id := range smokeGroup(t, item.Run.Identity.PGID) {
					cmdline, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", id.PID))
					wchan, _ := os.ReadFile(fmt.Sprintf("/proc/%d/wchan", id.PID))
					t.Logf("group member %+v: cmdline=%q wchan=%q", id, cmdline, wchan)
				}
			}
		}
	}
	t.Fatal("real smoke timed out:", description)
}
func smokeGroup(t *testing.T, pgid int) []model.ProcessIdentity {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	var members []model.ProcessIdentity
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		id, err := runtime.InspectProcess(pid)
		if err == nil && id.PGID == pgid {
			members = append(members, id)
		}
	}
	return members
}
func assertSmokeFinished(t *testing.T, f *integration, alias string, run model.Run) model.Run {
	t.Helper()
	var final model.Run
	smokeEventually(t, f, alias+" graceful group completion", func() bool {
		final = findRun(f.state(), alias)
		return final.ID == run.ID && final.Lifecycle == model.Exited
	})
	if final.Outcome == nil || !final.StopRequested || final.Error != "" || final.LogError != "" || final.MetadataError != "" {
		t.Fatal("incomplete/failed graceful collection", final)
	}
	if !errors.Is(unix.Kill(-run.Identity.PGID, 0), unix.ESRCH) {
		t.Fatal("ordinary reloader/prefork descendants survived", smokeGroup(t, run.Identity.PGID))
	}
	return final
}

type smokeIdentity struct {
	PID  int `json:"pid"`
	PGID int `json:"pgid"`
}

var smokeAddress = regexp.MustCompile(`http://127\.0\.0\.1:[0-9]+`)

func smokeHealth(address string) (smokeIdentity, bool) {
	client := http.Client{Timeout: time.Second}
	response, err := client.Get(address + "/health")
	if err != nil {
		return smokeIdentity{}, false
	}
	defer response.Body.Close()
	var id smokeIdentity
	err = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&id)
	return id, err == nil && response.StatusCode == http.StatusOK && id.PID > 1
}
func TestSmokeFlaskRealReloaderReconnectRestartAndStop(t *testing.T) {
	f := smokeProject(t, smokePython(t))
	run := f.start("api", os.Environ())
	var address string
	var first smokeIdentity
	smokeEventually(t, f, "Flask reloader HTTP readiness", func() bool {
		address = smokeAddress.FindString(f.logs("api", run))
		var ok bool
		first, ok = smokeHealth(address)
		return ok
	})
	if first.PGID != run.Identity.PGID || first.PID == run.Identity.PID || len(smokeGroup(t, first.PGID)) < 3 {
		t.Fatal("Flask did not form an ordinary reloader group", first, smokeGroup(t, first.PGID))
	}
	f.connection.Close()
	f.connection = nil // real commands/log capture continue without any client
	if _, ok := smokeHealth(address); !ok {
		t.Fatal("Flask depended on its client")
	}
	f.connection = f.connect(f.root)
	if current := findRun(f.state(), "api"); current.ID != run.ID || current.Identity != run.Identity {
		t.Fatal("reconnect replaced Flask", current)
	}
	path := filepath.Join(f.root, "smoke_app.py")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, []byte("\n# trigger the real Werkzeug reloader\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	var reloaded smokeIdentity
	smokeEventually(t, f, "Werkzeug child replacement", func() bool { var ok bool; reloaded, ok = smokeHealth(address); return ok && reloaded.PID != first.PID })
	if reloaded.PGID != first.PGID || findRun(f.state(), "api").ID != run.ID {
		t.Fatal("reloading changed the managed run", reloaded)
	}
	next, err := f.connection.Client.Restart(context.Background(), "api", os.Environ())
	if err != nil {
		t.Fatal("Flask restart", err)
	}
	f.remember(next)
	if next.ID == run.ID || !errors.Is(unix.Kill(-run.Identity.PGID, 0), unix.ESRCH) {
		t.Fatal("overlapping Flask replacement")
	}
	smokeEventually(t, f, "restarted Flask HTTP readiness", func() bool {
		address = smokeAddress.FindString(f.logs("api", next))
		_, ok := smokeHealth(address)
		return ok
	})
	if _, err := f.connection.Client.Stop(context.Background(), "api"); err != nil {
		t.Fatal(err)
	}
	assertSmokeFinished(t, f, "api", next)
	if text := f.logs("api", next); !strings.Contains(text, "Restarting with stat") {
		t.Fatal("final reloader output missing", text)
	}
}

func enqueueSmoke(t *testing.T, f *integration, python, pool string, seconds int, token string) {
	t.Helper()
	f.write(fmt.Sprintf("version: 1\nlogs: {maxBytes: 1048576}\nservices:\n  worker:\n    command: %q\ntasks:\n  enqueue:\n    command: %q\n", shellQuote(python)+" -m celery -A smoke_app.celery worker --pool="+pool+" --concurrency=2 --loglevel=INFO --without-gossip --without-mingle --without-heartbeat", shellQuote(python)+fmt.Sprintf(" -u smoke_app.py enqueue --seconds %d --token %s", seconds, token)))
	c := f.connect(f.root)
	c.Close()
	task := f.start("enqueue", os.Environ())
	final := f.finished("enqueue", task)
	if final.Outcome == nil || final.Outcome.Kind != model.Success {
		t.Fatal("real task publication failed", final, f.logs("enqueue", task))
	}
}
func smokeIdle(t *testing.T, f *integration, python string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.root, "lazyrun.yml"))
	if err != nil {
		t.Fatal(err)
	}
	f.write(string(data) + fmt.Sprintf("  idle:\n    command: %q\n", shellQuote(python)+" -u smoke_app.py wait-idle"))
	c := f.connect(f.root)
	c.Close()
	probe := f.start("idle", os.Environ())
	var final model.Run
	smokeEventually(t, f, "parent-side Celery idle acknowledgement", func() bool {
		final = findRun(f.state(), "idle")
		return final.ID == probe.ID && final.Lifecycle == model.Exited
	})
	if final.Outcome == nil || final.Outcome.Kind != model.Success || !strings.Contains(f.logs("idle", probe), "worker idle") {
		t.Fatal("Celery idle probe failed", final, f.logs("idle", probe))
	}
}
func smokeTaskIdentity(root, kind, token string) (smokeIdentity, bool) {
	data, err := os.ReadFile(filepath.Join(root, kind+"-"+token+".json"))
	if err != nil {
		return smokeIdentity{}, false
	}
	var id smokeIdentity
	err = json.Unmarshal(data, &id)
	return id, err == nil && id.PID > 1
}
func TestSmokeCeleryPreforkAndThreadsGracefulShutdown(t *testing.T) {
	python := smokePython(t)
	f := smokeProject(t, python)
	run := f.start("worker", os.Environ())
	smokeEventually(t, f, "real Celery prefork readiness", func() bool { return strings.Contains(f.logs("worker", run), "ready.") })
	if len(smokeGroup(t, run.Identity.PGID)) < 4 {
		t.Fatal("Celery prefork pool missing", smokeGroup(t, run.Identity.PGID))
	}
	enqueueSmoke(t, f, python, "prefork", 1, "warm")
	var running smokeIdentity
	smokeEventually(t, f, "prefork task start", func() bool { var ok bool; running, ok = smokeTaskIdentity(f.root, "started", "warm"); return ok })
	if running.PGID != run.Identity.PGID || running.PID == run.Identity.PID {
		t.Fatal("task did not execute in an ordinary pool child", running)
	}
	smokeEventually(t, f, "prefork task completion before idle stop", func() bool { done, ok := smokeTaskIdentity(f.root, "done", "warm"); return ok && done == running })
	smokeIdle(t, f, python)
	if _, err := f.connection.Client.Stop(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	assertSmokeFinished(t, f, "worker", run)
	if text := f.logs("worker", run); !strings.Contains(text, "Warm shutdown") || !strings.Contains(text, "smoke task warm finished") {
		t.Fatal("final worker output missing", text)
	}
	// Real Celery threads-pool tasks stay inside the worker process. Unlike
	// prefork children, they are not separately terminated by group SIGTERM.
	enqueueSmoke(t, f, python, "threads", 6, "blocked")
	run = f.start("worker", os.Environ())
	smokeEventually(t, f, "threads Celery readiness", func() bool { return strings.Contains(f.logs("worker", run), "ready.") })
	smokeEventually(t, f, "long threads task start", func() bool { _, ok := smokeTaskIdentity(f.root, "started", "blocked"); return ok })
	blocked, err := f.connection.Client.Restart(context.Background(), "worker", os.Environ())
	if !remoteCode(err, transport.CodeRestartBlocked) || blocked.ID != run.ID {
		t.Fatal("warm shutdown timeout did not cancel replacement", blocked, err)
	}
	if _, err := f.connection.Client.Start(context.Background(), "worker", os.Environ()); !remoteCode(err, transport.CodeAlreadyRunning) {
		t.Fatal("overlap admitted while pool task was alive", err)
	}
	assertSmokeFinished(t, f, "worker", run)
	if _, ok := smokeTaskIdentity(f.root, "done", "blocked"); !ok {
		t.Fatal("blocked restart force-killed the task")
	}
	time.Sleep(300 * time.Millisecond)
	if current := findRun(f.state(), "worker"); current.ID != run.ID || current.Lifecycle != model.Exited {
		t.Fatal("canceled replacement launched later", current)
	}
}
