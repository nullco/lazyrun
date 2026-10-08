//go:build linux

package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
	"lazyrun/internal/model"
	"lazyrun/internal/runtime"
	"lazyrun/internal/supervisor"
	"lazyrun/internal/transport"
)

func injectRecord(t *testing.T, root string, r model.Run, version int) {
	t.Helper()
	paths, err := supervisor.OpenPaths(model.ProjectID(root))
	if err != nil {
		t.Fatal(err)
	}
	defer paths.Close()
	sum := sha256.Sum256([]byte(r.Definition.Alias))
	b, err := json.Marshal(struct {
		Version int       `json:"version"`
		Run     model.Run `json:"run"`
	}{Version: version, Run: r})
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.State.AtomicWrite(fmt.Sprintf("%x.json", sum), b); err != nil {
		t.Fatal(err)
	}
}
func killServer(t *testing.T, f *integration) {
	t.Helper()
	old := f.connection.Hello.Supervisor
	if err := unix.Kill(old.PID, unix.SIGKILL); err != nil {
		t.Fatal(err)
	}
	eventuallyIntegration(t, func() bool { return !sameProcess(old) })
	f.connection.Close()
}

func TestReusedOrFakeRecordedPIDNeverGrantsSignalingAuthority(t *testing.T) {
	f := newIntegration(t, "version: 1\ntasks: {x: {command: printf real-command}}")
	killServer(t, f)
	unrelated := exec.Command("/bin/sleep", "60")
	unrelated.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unrelated.Process.Kill(); _ = unrelated.Wait() })
	id, err := runtime.InspectProcess(unrelated.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	fake := model.Run{ID: "fake-old-run", ProjectID: model.ProjectID(f.root), Lifecycle: model.Running, Identity: id, Definition: model.Definition{Alias: "x", Kind: model.Task, Command: "old-command", Cwd: f.root}}
	fake.Identity.StartTicks-- // A PID reused from an earlier launch, not ownership.
	injectRecord(t, f.root, fake, 1)
	f.connection = f.connect(f.root)
	if _, err := f.connection.Client.Stop(context.Background(), "x"); !remoteCode(err, transport.CodeUnmanaged) {
		t.Fatal("recorded PID granted stop authority", err)
	}
	if _, err := f.connection.Client.Start(context.Background(), "x", nil); !remoteCode(err, transport.CodeUnmanaged) {
		t.Fatal("potential survivor did not block start", err)
	}
	if !sameProcess(id) {
		t.Fatal("reconciliation killed unrelated process")
	}
}

func TestPreviousBootMetadataHasNoFabricatedExitOutcome(t *testing.T) {
	f := newIntegration(t, "version: 1\ntasks: {x: {command: printf fresh}}")
	killServer(t, f)
	r := model.Run{ID: "previous-boot-run", ProjectID: model.ProjectID(f.root), Lifecycle: model.Running, Identity: model.ProcessIdentity{PID: 123, PGID: 123, StartTicks: 1, BootID: "previous-boot"}, Definition: model.Definition{Alias: "x", Kind: model.Task, Cwd: f.root}}
	injectRecord(t, f.root, r, 1)
	f.connection = f.connect(f.root)
	historical := findRun(f.state(), "x")
	if historical.Lifecycle != model.Exited || historical.Outcome != nil || historical.EndedAt != nil {
		t.Fatal("fabricated previous-boot outcome", historical)
	}
	fresh := f.start("x", nil)
	f.finished("x", fresh)
	if fresh.ID == historical.ID || f.logs("x", fresh) != "fresh" {
		t.Fatal("stale boot prevented safe future start")
	}
}

func TestIncompatibleMetadataFailsStartupBeforeCommandsCanRun(t *testing.T) {
	f := newIntegration(t, "version: 1\ntasks: {x: {command: printf must-not-run}}")
	killServer(t, f)
	r := model.Run{ID: "invalid", ProjectID: model.ProjectID(f.root), Lifecycle: model.Running, Definition: model.Definition{Alias: "x", Kind: model.Task}}
	injectRecord(t, f.root, r, 999)
	c, err := Connect(context.Background(), f.root, supervisor.LaunchOptions{Executable: testExecutable})
	if c != nil {
		c.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Fatal("unsafe startup accepted incompatible metadata", err)
	}
	// No socket is published and no command has been executed. The old fixture
	// connection is deliberately closed; cleanup cannot adopt fabricated records.
	f.connection = nil
}

func TestIncompleteLaunchIntentFailsClosed(t *testing.T) {
	f := newIntegration(t, "version: 1\ntasks: {x: {command: printf must-not-run}}")
	killServer(t, f)
	identity, err := runtime.InspectProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	r := model.Run{ID: "incomplete-intent", ProjectID: model.ProjectID(f.root), Lifecycle: model.Starting, Identity: model.ProcessIdentity{BootID: identity.BootID}, Definition: model.Definition{Alias: "x", Kind: model.Task}}
	injectRecord(t, f.root, r, 1)
	f.connection = f.connect(f.root)
	if _, err := f.connection.Client.Start(context.Background(), "x", nil); !remoteCode(err, transport.CodeUnmanaged) {
		t.Fatal("unrecorded launch identity allowed a duplicate", err)
	}
}
