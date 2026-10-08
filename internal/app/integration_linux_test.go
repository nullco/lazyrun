//go:build linux

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"lazyrun/internal/model"
	"lazyrun/internal/runtime"
	"lazyrun/internal/supervisor"
	"lazyrun/internal/transport"
)

var testExecutable string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "lazyrun-integration-")
	if err != nil {
		panic(err)
	}
	testExecutable = filepath.Join(dir, "lazyrun")
	args := []string{"build", "-o", testExecutable, "../../cmd/lazyrun"}
	if integrationRace {
		args = append([]string{"build", "-race"}, args[1:]...)
	}
	command := exec.Command("go", args...)
	if b, err := command.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		fmt.Fprintf(os.Stderr, "build integration binary: %v\n%s", err, b)
		os.Exit(1)
	}
	// Own orphaned fixture descendants after deliberate supervisor crashes so
	// tests can reap them without leaving zombies under the machine's PID 1.
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

const pulseConfig = `version: 1
services:
  pulse:
    command: |
      trap 'echo terminating; /bin/sleep 0.5; exit 0' TERM
      echo ready
      while :; do echo pulse; /bin/sleep 0.05; done
tasks:
  env:
    command: printf 'SNAPSHOT=%s OVERRIDE=%s' "$SNAPSHOT" "$OVERRIDE"
    env: {OVERRIDE: configured}
`

type integration struct {
	t          *testing.T
	root       string
	connection *supervisor.Connection
	servers    []model.ProcessIdentity
	runs       map[string]model.Run
	mu         sync.Mutex
}

func newIntegration(t *testing.T, data string) *integration {
	t.Helper()
	base := t.TempDir()
	if err := os.Chmod(base, 0700); err != nil {
		t.Fatal(err)
	}
	runtimeDir := filepath.Join(base, "runtime")
	if err := os.Mkdir(runtimeDir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "state"))
	root := filepath.Join(base, "project")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	f := &integration{t: t, root: root, runs: make(map[string]model.Run)}
	f.write(data)
	f.connection = f.connect(root)
	t.Cleanup(f.cleanup)
	return f
}

func (f *integration) write(data string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.root, "lazyrun.yml"), []byte(data), 0600); err != nil {
		f.t.Fatal(err)
	}
}
func (f *integration) connect(dir string) *supervisor.Connection {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := Connect(ctx, dir, supervisor.LaunchOptions{Executable: testExecutable})
	if err != nil {
		f.t.Fatal(err)
	}
	f.servers = append(f.servers, c.Hello.Supervisor)
	return c
}
func (f *integration) remember(r model.Run) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.ID != "" {
		f.runs[r.ID] = r
	}
}
func (f *integration) start(alias string, env []string) model.Run {
	f.t.Helper()
	r, err := f.connection.Client.Start(context.Background(), alias, env)
	if err != nil {
		f.t.Fatal(err)
	}
	f.remember(r)
	return r
}
func eventuallyIntegration(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("integration condition not met")
}
func (f *integration) state() model.State {
	f.t.Helper()
	state, err := f.connection.Client.State(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	return state
}
func findRun(state model.State, alias string) model.Run {
	for _, item := range state.Commands {
		if item.Run.Definition.Alias == alias {
			return item.Run
		}
	}
	return model.Run{}
}
func (f *integration) logs(alias string, r model.Run) string {
	f.t.Helper()
	read, err := f.connection.Client.Logs(context.Background(), alias, r.ID, 0, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	return string(read.Data)
}
func (f *integration) ready(alias string, r model.Run) {
	f.t.Helper()
	eventuallyIntegration(f.t, func() bool { return strings.Contains(f.logs(alias, r), "ready") })
}
func (f *integration) finished(alias string, r model.Run) model.Run {
	f.t.Helper()
	var finished model.Run
	eventuallyIntegration(f.t, func() bool {
		finished = findRun(f.state(), alias)
		return finished.ID == r.ID && finished.Lifecycle == model.Exited
	})
	return finished
}
func remoteCode(err error, code string) bool {
	var remote *transport.Error
	return errors.As(err, &remote) && remote.Code == code
}

func sameProcess(id model.ProcessIdentity) bool {
	actual, err := runtime.InspectProcess(id.PID)
	return err == nil && actual == id
}
func (f *integration) cleanup() {
	if f.connection != nil {
		if state, err := f.connection.Client.State(context.Background()); err == nil {
			for _, item := range state.Commands {
				f.mu.Lock()
				_, owned := f.runs[item.Run.ID]
				f.mu.Unlock()
				if owned || !strings.Contains(item.Run.Error, runtime.ErrUnmanaged.Error()) {
					f.remember(item.Run)
				}
			}
		}
	}
	// Test-only cleanup: only runs started here, with verified identities, are
	// force-killed. Product stop/recovery never sends this signal.
	for _, r := range f.runs {
		if r.Identity.PGID <= 1 {
			continue
		}
		if sameProcess(r.Identity) {
			_ = unix.Kill(-r.Identity.PGID, unix.SIGKILL)
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			var status unix.WaitStatus
			_, _ = unix.Wait4(-r.Identity.PGID, &status, unix.WNOHANG, nil)
			if r.Identity.PGID <= 1 || errors.Is(unix.Kill(-r.Identity.PGID, 0), unix.ESRCH) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	for _, id := range f.servers {
		if sameProcess(id) {
			_ = unix.Kill(id.PID, unix.SIGKILL)
		}
	}
	if f.connection != nil {
		f.connection.Close()
	}
	// The launcher's Cmd.Wait goroutine reaps supervisor leaders.
	time.Sleep(30 * time.Millisecond)
	paths, err := supervisor.OpenPaths(model.ProjectID(f.root))
	if err == nil {
		defer paths.Close()
		if b, err := paths.State.Read("diagnostic.log", 2*1024*1024); err == nil && strings.Contains(string(b), "DATA RACE") {
			f.t.Error("race detected in detached supervisor")
		}
	}
}

func TestSupervisorReconnectConcurrencyAndEnvironment(t *testing.T) {
	f := newIntegration(t, pulseConfig)
	for _, item := range f.state().Commands {
		if item.Run.ID != "" {
			t.Fatal("opening started a command")
		}
	}
	const clients = 8
	var wg sync.WaitGroup
	results := make(chan error, clients)
	identities := make(chan int, clients)
	for range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := Connect(context.Background(), f.root, supervisor.LaunchOptions{Executable: testExecutable})
			if err != nil {
				results <- err
				return
			}
			defer c.Close()
			identities <- c.Hello.Supervisor.PID
			r, err := c.Client.Start(context.Background(), "pulse", []string{"SNAPSHOT=fresh"})
			f.remember(r)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	close(identities)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else if !remoteCode(err, transport.CodeAlreadyRunning) {
			t.Fatal(err)
		}
	}
	if accepted != 1 {
		t.Fatal("duplicate starts accepted", accepted)
	}
	for pid := range identities {
		if pid != f.connection.Hello.Supervisor.PID {
			t.Fatal("multiple supervisors")
		}
	}
	r := findRun(f.state(), "pulse")
	f.ready("pulse", r)
	first, err := f.connection.Client.Logs(context.Background(), "pulse", r.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	// A new client through a subdirectory/symlink sees the same authority/run.
	sub := filepath.Join(f.root, "sub")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(f.root), "link")
	if err := os.Symlink(f.root, link); err != nil {
		t.Fatal(err)
	}
	reconnect := f.connect(filepath.Join(link, "sub"))
	defer reconnect.Close()
	state, err := reconnect.Client.State(context.Background())
	if err != nil || findRun(state, "pulse").ID != r.ID || findRun(state, "pulse").Identity != r.Identity {
		t.Fatal("reconnect changed identity", state, err)
	}
	eventuallyIntegration(t, func() bool {
		next, err := reconnect.Client.Logs(context.Background(), "pulse", r.ID, first.Next, 0)
		return err == nil && len(next.Data) > 0
	})
	for _, value := range []string{"first", "new-virtualenv", string([]byte{0xff, 0xfe})} {
		task := f.start("env", []string{"SNAPSHOT=" + value, "OVERRIDE=request", "INHERITED_SECRET=never-store-this-secret"})
		f.finished("env", task)
		if text := f.logs("env", task); text != "SNAPSHOT="+value+" OVERRIDE=configured" {
			t.Fatal(text)
		}
	}
	paths, err := supervisor.OpenPaths(model.ProjectID(f.root))
	if err != nil {
		t.Fatal(err)
	}
	defer paths.Close()
	names, _ := paths.State.Names()
	for _, name := range names {
		b, err := paths.State.Read(name, 8*1024*1024)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "never-store-this-secret") || strings.Contains(string(b), "INHERITED_SECRET") {
			t.Fatal("persisted inherited environment")
		}
	}
	if _, err := f.connection.Client.Stop(context.Background(), "pulse"); err != nil {
		t.Fatal(err)
	}
	f.finished("pulse", r)
}

func TestConfigSynchronizationAndProjectIsolation(t *testing.T) {
	f := newIntegration(t, pulseConfig)
	r := f.start("pulse", nil)
	f.ready("pulse", r)
	f.write("version: 1\ntasks:\n  pulse:\n    command: printf replacement\n")
	c := f.connect(f.root)
	defer c.Close()
	state := f.state()
	if len(state.Commands) != 1 || state.Commands[0].Run.Definition.Kind != model.Service || state.Commands[0].Definition.Kind != model.Task || state.Commands[0].Run.ID != r.ID {
		t.Fatal("active run reclassified/restarted")
	}
	next, err := f.connection.Client.Restart(context.Background(), "pulse", nil)
	if err != nil {
		t.Fatal(err)
	}
	f.remember(next)
	f.finished("pulse", next)
	if next.Definition.Kind != model.Task || f.logs("pulse", next) != "replacement" {
		t.Fatal("replacement ignored current configuration")
	}
	f.write(pulseConfig)
	c2 := f.connect(f.root)
	defer c2.Close()
	active := f.start("pulse", nil)
	f.ready("pulse", active)
	f.write("version: 1\nservices: {}\ntasks: {}\n")
	c3 := f.connect(f.root)
	defer c3.Close()
	state = f.state()
	if len(state.Commands) != 1 || !state.Commands[0].Removed || state.Commands[0].Run.ID != active.ID {
		t.Fatal("removed run became inaccessible")
	}
	if _, err := f.connection.Client.Stop(context.Background(), "pulse"); err != nil {
		t.Fatal(err)
	}
	eventuallyIntegration(t, func() bool { return len(f.state().Commands) == 0 })
	other := filepath.Join(filepath.Dir(f.root), "other")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "lazyrun.yml"), []byte("version: 1\ntasks: {pulse: {command: printf other-project}}"), 0600); err != nil {
		t.Fatal(err)
	}
	otherClient := f.connect(other)
	defer otherClient.Close()
	if otherClient.Hello.Supervisor.PID == f.connection.Hello.Supervisor.PID {
		t.Fatal("projects shared supervisor")
	}
	otherRun, err := otherClient.Client.Start(context.Background(), "pulse", nil)
	if err != nil {
		t.Fatal(err)
	}
	f.remember(otherRun)
	eventuallyIntegration(t, func() bool {
		s, err := otherClient.Client.State(context.Background())
		return err == nil && findRun(s, "pulse").Lifecycle == model.Exited
	})
	if len(f.state().Commands) != 0 {
		t.Fatal("other project leaked into state")
	}
}

func TestSupervisorLossBlocksUnmanagedRunAndNeverSignalsIt(t *testing.T) {
	f := newIntegration(t, `version: 1
services:
  stubborn:
    command: |
      trap '' TERM
      echo ready
      while :; do /bin/sleep 1; done
`)
	r := f.start("stubborn", nil)
	f.ready("stubborn", r)
	oldServer := f.connection.Hello.Supervisor
	if err := unix.Kill(oldServer.PID, unix.SIGKILL); err != nil {
		t.Fatal(err)
	}
	eventuallyIntegration(t, func() bool { return !sameProcess(oldServer) })
	f.connection.Close()
	f.connection = f.connect(f.root)
	if f.connection.Hello.Supervisor.PID == oldServer.PID {
		t.Fatal("did not replace dead supervisor")
	}
	state := findRun(f.state(), "stubborn")
	if state.Lifecycle != model.Unknown || state.ID != r.ID || state.Outcome != nil {
		t.Fatalf("lost/fabricated run: %+v", state)
	}
	for _, action := range []func() (model.Run, error){
		func() (model.Run, error) { return f.connection.Client.Start(context.Background(), "stubborn", nil) },
		func() (model.Run, error) { return f.connection.Client.Stop(context.Background(), "stubborn") },
		func() (model.Run, error) { return f.connection.Client.Restart(context.Background(), "stubborn", nil) },
	} {
		if _, err := action(); !remoteCode(err, transport.CodeUnmanaged) {
			t.Fatal("unsafe recovery action", err)
		}
	}
	if !sameProcess(r.Identity) {
		t.Fatal("recovery killed/replaced the unmanaged process")
	}
}

func TestFinishedRunMetadataSurvivesSupervisorReplacement(t *testing.T) {
	f := newIntegration(t, "version: 1\ntasks: {done: {command: 'printf final; exit 9'}}")
	r := f.start("done", nil)
	finished := f.finished("done", r)
	old := f.connection.Hello.Supervisor
	if err := unix.Kill(old.PID, unix.SIGKILL); err != nil {
		t.Fatal(err)
	}
	eventuallyIntegration(t, func() bool { return !sameProcess(old) })
	f.connection.Close()
	f.connection = f.connect(f.root)
	restored := findRun(f.state(), "done")
	if restored.ID != finished.ID || restored.Outcome == nil || restored.Outcome.ExitCode == nil || *restored.Outcome.ExitCode != 9 {
		t.Fatal("lost final metadata", restored)
	}
	read, err := f.connection.Client.Logs(context.Background(), "done", restored.ID, 0, 0)
	if err != nil || !read.Unavailable {
		t.Fatal("silently claimed historical memory output remained available", read, err)
	}
}

func TestMalformedSyncAndRequestsExecuteNothing(t *testing.T) {
	f := newIntegration(t, pulseConfig)
	err := f.connection.Client.Sync(context.Background(), []byte("version: 1\nservices: {x: {command: echo a}}\ntasks: {x: {command: echo b}}"))
	if !remoteCode(err, transport.CodeInvalid) {
		t.Fatal(err)
	}
	for _, item := range f.state().Commands {
		if item.Run.ID != "" {
			t.Fatal("malformed sync executed a command")
		}
	}
	if _, err := f.connection.Client.Start(context.Background(), "pulse", []string{"INVALID"}); err == nil {
		t.Fatal("accepted invalid environment")
	}
}

func TestStateJSONKeepsConfigurationEnvironmentPrivate(t *testing.T) {
	f := newIntegration(t, pulseConfig)
	b, err := json.Marshal(f.state())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "configured") || strings.Contains(string(b), `"env":`) {
		t.Fatal("exposed configured environment in display state")
	}
}
