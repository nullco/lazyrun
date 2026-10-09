//go:build linux

package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"lazyrun/internal/model"
)

func terminalClient(t *testing.T, root string, args ...string) (*exec.Cmd, *os.File) {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("M3 terminal-closure gate requires a Linux PTY: %v", err)
	}
	master := os.NewFile(uintptr(fd), "PTY master")
	if err := unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 100}); err != nil {
		master.Close()
		t.Fatal(err)
	}
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		master.Close()
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		master.Close()
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR, 0)
	if err != nil {
		master.Close()
		t.Fatal(err)
	}
	cmd := exec.Command(testExecutable, args...)
	cmd.Dir = root
	cmd.Env = os.Environ()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		master.Close()
		slave.Close()
		t.Fatal(err)
	}
	slave.Close()
	t.Cleanup(func() {
		master.Close()
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return cmd, master
}

func TestTerminalClosureAndClientCrashDoNotOwnCommandLifetimes(t *testing.T) {
	f := newIntegration(t, pulseConfig)
	// Launch the replacement supervisor from the controlling terminal itself,
	// not from a pre-existing non-terminal test client.
	oldServer := f.connection.Hello.Supervisor
	if err := unix.Kill(oldServer.PID, unix.SIGKILL); err != nil {
		t.Fatal(err)
	}
	eventuallyIntegration(t, func() bool { return !sameProcess(oldServer) })
	cmd, master := terminalClient(t, f.root, "--start", "pulse")
	line := make(chan string, 1)
	go func() { text, _ := bufio.NewReader(master).ReadString('\n'); line <- text }()
	var run model.Run
	select {
	case text := <-line:
		if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &run); err != nil {
			t.Fatalf("terminal client response %q: %v", text, err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("terminal client did not start a command")
	}
	hello, err := f.connection.Client.Hello(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.connection.Hello = hello
	f.servers = append(f.servers, hello.Supervisor)
	f.remember(run)
	f.ready("pulse", run)
	before, err := f.connection.Client.Logs(context.Background(), "pulse", run.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Closing the actual controlling terminal cannot reach the detached server.
	master.Close()
	_ = cmd.Wait()
	if f.connection.Hello.Supervisor.PGID != f.connection.Hello.Supervisor.PID {
		t.Fatal("supervisor not detached into its own session/group")
	}
	state := findRun(f.state(), "pulse")
	if state.ID != run.ID || state.Identity != run.Identity || state.Lifecycle != model.Running {
		t.Fatal("terminal closure changed the managed run", state)
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", run.Identity.PID))
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(stat)[strings.LastIndexByte(string(stat), ')')+1:])
	if len(fields) < 5 || fields[4] != "0" {
		t.Fatal("managed command retained a controlling terminal")
	}
	descriptors, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", run.Identity.PID))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range descriptors {
		target, _ := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", run.Identity.PID, entry.Name()))
		if strings.Contains(target, "owner.lock") || strings.Contains(target, "state.lock") || strings.Contains(target, "/dev/pts/") {
			t.Fatal("managed process inherited supervisor ownership/terminal descriptor", target)
		}
	}
	eventuallyIntegration(t, func() bool {
		next, err := f.connection.Client.Logs(context.Background(), "pulse", run.ID, before.Next, 0)
		return err == nil && len(next.Data) > 0
	})
	// Kill the client by terminal hangup after the server has accepted restart.
	// Its replacement must still occur, using a runtime-owned operation.
	restart, terminal := terminalClient(t, f.root, "--restart", "pulse")
	eventuallyIntegration(t, func() bool { old := findRun(f.state(), "pulse"); return old.ID == run.ID && old.StopRequested })
	terminal.Close()
	if err := restart.Wait(); err == nil {
		t.Fatal("restart client finished before terminal hangup; crash gate not exercised")
	}
	var replacement model.Run
	eventuallyIntegration(t, func() bool {
		replacement = findRun(f.state(), "pulse")
		return replacement.ID != "" && replacement.ID != run.ID && replacement.Lifecycle == model.Running
	})
	f.remember(replacement)
	f.ready("pulse", replacement)
	if _, err := f.connection.Client.Stop(context.Background(), "pulse"); err != nil {
		t.Fatal(err)
	}
	f.finished("pulse", replacement)
}
