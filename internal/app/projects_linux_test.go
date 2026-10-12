//go:build linux

package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nullco/lazyrun/internal/model"
	"github.com/nullco/lazyrun/internal/supervisor"
	"github.com/nullco/lazyrun/internal/transport"
	"golang.org/x/sys/unix"
)

func projectDirectory(t *testing.T, base, name, data string) string {
	t.Helper()
	root := filepath.Join(base, name)
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "lazyrun.yml"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestSharedProjectsRegistrationRoutingAndLifetime(t *testing.T) {
	f := newIntegration(t, "version: 1\nname: first\ntasks: {same: {command: 'printf first'}}\n")
	ctx := context.Background()
	second := projectDirectory(t, filepath.Dir(f.root), "second", "version: 1\nname: second\ntasks: {same: {command: 'printf second'}}\n")
	c := f.connect(second)
	defer c.Close()
	if c.Hello.Supervisor != f.connection.Hello.Supervisor {
		t.Fatal("different supervisors")
	}
	projects, err := c.Client.Projects(ctx)
	if err != nil || len(projects) != 2 || projects[0].ID != model.ProjectID(f.root) || projects[1].ID != model.ProjectID(second) {
		t.Fatal(projects, err)
	}
	for _, client := range []*transport.Client{f.connection.Client, c.Client} {
		state, err := client.State(ctx)
		if err != nil || findRun(state, "same").ID != "" {
			t.Fatal("registration started commands", state, err)
		}
	}
	firstRun := f.start("same", nil)
	f.finished("same", firstRun)
	secondRun, err := c.Client.Start(ctx, "same", nil)
	if err != nil {
		t.Fatal(err)
	}
	f.remember(secondRun)
	eventuallyIntegration(t, func() bool {
		state, err := c.Client.State(ctx)
		return err == nil && findRun(state, "same").Lifecycle == model.Exited
	})
	firstLogs, err := f.connection.Client.Logs(ctx, "same", firstRun.ID, 0, 0)
	if err != nil || string(firstLogs.Data) != "first" {
		t.Fatal(firstLogs, err)
	}
	secondLogs, err := c.Client.Logs(ctx, "same", secondRun.ID, 0, 0)
	if err != nil || string(secondLogs.Data) != "second" {
		t.Fatal(secondLogs, err)
	}
	if _, err := c.Client.Logs(ctx, "same", firstRun.ID, 0, 0); !remoteCode(err, transport.CodeRunChanged) {
		t.Fatal("cross-project log request accepted", err)
	}
	if _, err := c.Client.ForProject(model.ProjectID("/unregistered")).Start(ctx, "same", nil); !remoteCode(err, transport.CodeInvalid) {
		t.Fatal("unregistered project accepted", err)
	}
	// Closing every local connection does not unregister projects.
	c.Close()
	f.connection.Close()
	outsider := t.TempDir()
	dashboard, err := ConnectDashboard(ctx, outsider, supervisor.LaunchOptions{Executable: "/must/not/launch"})
	if err != nil {
		t.Fatal(err)
	}
	defer dashboard.Close()
	projects, err = dashboard.Client.Projects(ctx)
	if err != nil || len(projects) != 2 {
		t.Fatal("lost registrations on disconnect", projects, err)
	}
	old := dashboard.Hello.Supervisor
	if err := unix.Kill(old.PID, unix.SIGKILL); err != nil {
		t.Fatal(err)
	}
	eventuallyIntegration(t, func() bool { return !sameProcess(old) })
	// Only the newly opened project is registered after replacement.
	f.connection = f.connect(second)
	projects, err = f.connection.Client.Projects(ctx)
	if err != nil || len(projects) != 1 || projects[0].ID != model.ProjectID(second) {
		t.Fatal("persisted project registration", projects, err)
	}
	if run := findRun(f.state(), "same"); run.ID != secondRun.ID {
		t.Fatal("lost durable run", run)
	}
	reopened := f.connect(f.root)
	defer reopened.Close()
	restored, err := reopened.Client.State(ctx)
	if err != nil || findRun(restored, "same").ID != firstRun.ID {
		t.Fatal("lost first project's durable state", restored, err)
	}
	projects, err = reopened.Client.Projects(ctx)
	if err != nil || len(projects) != 2 {
		t.Fatal(projects, err)
	}
}

func TestConcurrentDifferentProjectBootstrap(t *testing.T) {
	f := newIntegration(t, "version: 1")
	old := f.connection.Hello.Supervisor
	if err := unix.Kill(old.PID, unix.SIGKILL); err != nil {
		t.Fatal(err)
	}
	eventuallyIntegration(t, func() bool { return !sameProcess(old) })
	f.connection.Close()
	const count = 8
	roots := []string{f.root}
	for i := 1; i < count; i++ {
		roots = append(roots, projectDirectory(t, filepath.Dir(f.root), string(rune('a'+i)), "version: 1\ntasks: {idle: {command: 'true'}}"))
	}
	results := make(chan *supervisor.Connection, count)
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for _, root := range roots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := Connect(context.Background(), root, supervisor.LaunchOptions{Executable: testExecutable})
			if err != nil {
				errs <- err
			} else {
				results <- c
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	var pid int
	f.connection = nil
	for c := range results {
		if pid == 0 {
			pid = c.Hello.Supervisor.PID
			f.connection = c
			f.servers = append(f.servers, c.Hello.Supervisor)
		} else {
			if c.Hello.Supervisor.PID != pid {
				t.Error("duplicate shared supervisor")
			}
			c.Close()
		}
	}
	if f.connection == nil {
		t.Fatal("no successful connections")
	}
	projects, err := f.connection.Client.Projects(context.Background())
	if err != nil || len(projects) != count {
		t.Fatal("lost concurrent registration", projects, err)
	}
}

func TestRegistrationValidationAndStableStateDirectory(t *testing.T) {
	f := newIntegration(t, "version: 1")
	ctx := context.Background()
	other := projectDirectory(t, filepath.Dir(f.root), "invalid", "version: 999")
	if c, err := ConnectDashboard(ctx, other, supervisor.LaunchOptions{Executable: testExecutable}); err == nil {
		c.Close()
		t.Fatal("invalid config fell back to another project")
	}
	if err := f.connection.Client.Register(ctx, other); err == nil {
		t.Fatal("server accepted invalid config")
	}
	if err := f.connection.Client.Register(ctx, filepath.Dir(f.root)); err == nil {
		t.Fatal("server accepted mismatched root")
	}
	projects, err := f.connection.Client.Projects(ctx)
	if err != nil || len(projects) != 1 {
		t.Fatal("failed registration changed list", projects, err)
	}
	if err := f.connection.Client.Sync(ctx, []byte("version: 1\nname: renamed")); err != nil {
		t.Fatal(err)
	}
	projects, err = f.connection.Client.Projects(ctx)
	if err != nil || len(projects) != 1 || projects[0].Name != "renamed" {
		t.Fatal("project list did not reflect config reload", projects, err)
	}
	t.Setenv("XDG_STATE_HOME", filepath.Join(filepath.Dir(f.root), "different-state"))
	if c, err := Connect(ctx, f.root, supervisor.LaunchOptions{Executable: testExecutable}); err == nil {
		c.Close()
		t.Fatal("accepted different state directory")
	} else if !strings.Contains(err.Error(), "different state directory") {
		t.Fatal(err)
	}
}

func TestDashboardDiscoversAndSwitchesLiveProjects(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	f := newIntegration(t, "version: 1\nname: first-project\ntasks: {first-task: {command: 'printf first-output'}}")
	cmd, master := terminalClient(t, f.root)
	out := watchTerminal(t, master)
	eventuallyIntegration(t, func() bool { return out.contains("first-task") })
	other := projectDirectory(t, filepath.Dir(f.root), "second", "version: 1\nname: second-project\ntasks: {second-task: {command: 'printf second-output'}}")
	c := f.connect(other)
	defer c.Close()
	eventuallyIntegration(t, func() bool { return out.contains("second-project") })
	out.clear()
	keys(t, master, "1j")
	eventuallyIntegration(t, func() bool { return out.contains("second-task") })
	keys(t, master, "3S")
	var secondRun model.Run
	eventuallyIntegration(t, func() bool {
		state, err := c.Client.State(context.Background())
		secondRun = findRun(state, "second-task")
		return err == nil && secondRun.ID != "" && secondRun.Lifecycle == model.Exited
	})
	f.remember(secondRun)
	if findRun(f.state(), "first-task").ID != "" {
		t.Fatal("switch started the wrong project")
	}
	eventuallyIntegration(t, func() bool { return out.contains("second-output") })
	out.clear()
	terminalMouse(t, master, 0, 4, 1) // Focus the compact selected-project display.
	eventuallyIntegration(t, func() bool { return out.contains("first-project") })
	terminalMouse(t, master, 0, 4, 1) // Now select the first project in the expanded list.
	eventuallyIntegration(t, func() bool { return out.contains("first-task") })
	keys(t, master, "3S")
	var firstRun model.Run
	eventuallyIntegration(t, func() bool {
		firstRun = findRun(f.state(), "first-task")
		return firstRun.ID != "" && firstRun.Lifecycle == model.Exited
	})
	f.remember(firstRun)
	eventuallyIntegration(t, func() bool { return out.contains("first-output") })
	keys(t, master, "q")
	if err := waitClient(t, cmd); err != nil {
		t.Fatal(err)
	}
	// The real CLI also opens from outside any project without registering it.
	outside, terminal := terminalClient(t, t.TempDir())
	outsideOutput := watchTerminal(t, terminal)
	eventuallyIntegration(t, func() bool { return outsideOutput.contains("second-project") && outsideOutput.contains("first-task") })
	keys(t, terminal, "q")
	if err := waitClient(t, outside); err != nil {
		t.Fatal(err)
	}
}

func TestDashboardWithoutRegisteredProjectsDoesNotLaunch(t *testing.T) {
	base := t.TempDir()
	if err := os.Chmod(base, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", base)
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "state"))
	if c, err := ConnectDashboard(context.Background(), base, supervisor.LaunchOptions{Executable: "/must/not/launch"}); err == nil {
		c.Close()
		t.Fatal("dashboard without projects connected")
	} else if !strings.Contains(err.Error(), "inside a project first") {
		t.Fatal(err)
	}
}
