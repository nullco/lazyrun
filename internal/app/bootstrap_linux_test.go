//go:build linux

package app

import (
	"context"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
	"lazyrun/internal/supervisor"
)

func TestConcurrentBootstrapReplacesOnlyOneStaleSocketOwner(t *testing.T) {
	f := newIntegration(t, "version: 1\ntasks: {x: {command: printf idle}}")
	old := f.connection.Hello.Supervisor
	if err := unix.Kill(old.PID, unix.SIGKILL); err != nil {
		t.Fatal(err)
	}
	eventuallyIntegration(t, func() bool { return !sameProcess(old) })
	f.connection.Close()
	const count = 12
	results := make(chan *supervisor.Connection, count)
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := Connect(context.Background(), f.root, supervisor.LaunchOptions{Executable: testExecutable})
			if err != nil {
				errs <- err
				return
			}
			results <- c
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
				t.Error("concurrent bootstrap launched multiple supervisors")
			}
			c.Close()
		}
	}
	if f.connection == nil {
		t.Fatal("no launcher connected")
	}
	if pid == old.PID {
		t.Fatal("stale owner reused")
	}
	for _, item := range f.state().Commands {
		if item.Run.ID != "" {
			t.Fatal("bootstrap executed a command")
		}
	}
}
