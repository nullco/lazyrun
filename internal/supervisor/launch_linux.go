//go:build linux

package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"lazyrun/internal/model"
	"lazyrun/internal/transport"
)

const InternalMode = "--internal-supervisor"
const StartupTimeout = 10 * time.Second

type LaunchOptions struct{ Executable string }
type Connection struct {
	Client *transport.Client
	Hello  transport.Hello
	paths  *Paths
}

func (c *Connection) Close() { c.paths.Close() }

func staleConnection(err error) bool {
	return errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ECONNREFUSED)
}

func probe(ctx context.Context, paths *Paths, c *transport.Client) (transport.Hello, error) {
	if _, err := paths.Runtime.Stat(socketName, unix.S_IFSOCK); err != nil {
		return transport.Hello{}, err
	}
	return c.Hello(ctx)
}

// Ensure serializes launchers separately from the long-lived ownership lock.
// A live/incompatible/unresponsive listener is never treated as a stale socket.
func Ensure(ctx context.Context, p model.Project, opts LaunchOptions) (_ *Connection, err error) {
	ctx, cancel := context.WithTimeout(ctx, StartupTimeout)
	defer cancel()
	paths, err := OpenPaths(p.ID)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			paths.Close()
		}
	}()
	client := &transport.Client{Endpoint: paths.Runtime.ProcPath(socketName), ProjectID: p.ID}
	finish := func(hello transport.Hello) (*Connection, error) {
		success = true
		return &Connection{Client: client, Hello: hello, paths: paths}, nil
	}
	if hello, err := probe(ctx, paths, client); err == nil {
		return finish(hello)
	} else if !staleConnection(err) {
		return nil, err
	}
	var launch *os.File
	for {
		launch, err = lock(paths.Runtime, "launch.lock", true)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			return nil, err
		}
		if err := pause(ctx); err != nil {
			return nil, err
		}
	}
	defer launch.Close()
	if hello, err := probe(ctx, paths, client); err == nil {
		return finish(hello)
	} else if !staleConnection(err) {
		return nil, err
	}
	var owner *os.File
	for {
		owner, err = lock(paths.Runtime, ownerName, true)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			return nil, err
		}
		// An owner may be initializing. Poll connectivity, never unlink its socket
		// or spawn a second owner just because the socket is temporarily unavailable.
		if hello, err := probe(ctx, paths, client); err == nil {
			return finish(hello)
		} else if !staleConnection(err) {
			return nil, err
		}
		if err := pause(ctx); err != nil {
			return nil, fmt.Errorf("supervisor holds ownership but is unreachable: %w", err)
		}
	}
	defer owner.Close()
	// Probe again under ownership before deleting only a verified stale socket.
	if hello, err := probe(ctx, paths, client); err == nil {
		return finish(hello)
	} else if !staleConnection(err) {
		return nil, err
	}
	if _, err := paths.Runtime.Stat(socketName, unix.S_IFSOCK); err == nil {
		if err := paths.Runtime.RemoveSocket(socketName); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return nil, err
	}
	executable := opts.Executable
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			return nil, err
		}
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	defer writer.Close()
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer null.Close()
	cmd := exec.Command(executable, InternalMode, p.Root)
	cmd.Dir = "/"
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, null
	cmd.ExtraFiles = []*os.File{owner, writer}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// Bootstrap storage settings only. Managed commands get their environment
	// exclusively from each start/restart request, not from this process.
	for _, key := range []string{"HOME", "XDG_RUNTIME_DIR", "XDG_STATE_HOME"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	if cmd.Env == nil {
		cmd.Env = []string{}
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("launch supervisor: %w", err)
	}
	cmd.Env = nil
	writer.Close()
	owner.Close()                  // The inherited open file description retains its flock.
	go func() { _ = cmd.Wait() }() // Reap on eventual exit; never tie it to ctx.
	ready := make(chan error, 1)
	go func() {
		var result startup
		data, err := io.ReadAll(io.LimitReader(reader, 4097))
		if err == nil && len(data) > 4096 {
			err = errors.New("invalid startup handshake size")
		}
		if err == nil {
			err = json.Unmarshal(data, &result)
		}
		if err == nil && result.Error != "" {
			err = errors.New(result.Error)
		}
		ready <- err
	}()
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("supervisor startup handshake: %w", ctx.Err())
	case err := <-ready:
		if err != nil {
			return nil, fmt.Errorf("supervisor startup: %w", err)
		}
	}
	hello, err := probe(ctx, paths, client)
	if err != nil {
		return nil, err
	}
	return finish(hello)
}

func pause(ctx context.Context) error {
	timer := time.NewTimer(20 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
