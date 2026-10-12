//go:build linux

package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/nullco/lazyrun/internal/config"
	"github.com/nullco/lazyrun/internal/logsearch"
	"github.com/nullco/lazyrun/internal/model"
	"github.com/nullco/lazyrun/internal/runtime"
	"github.com/nullco/lazyrun/internal/transport"
	"golang.org/x/sys/unix"
)

type startup struct {
	Error string `json:"error,omitempty"`
}

// Run starts the detached supervisor with the launcher's inherited descriptors.
// FD 3 is the ownership lock; FD 4 is a one-shot startup handshake pipe.
func Run(ctx context.Context, binaryVersion string) (err error) {
	owner := os.NewFile(3, "supervisor ownership")
	ready := os.NewFile(4, "supervisor readiness")
	// ExtraFiles intentionally survive the bootstrap exec. Restore CLOEXEC
	// before any managed command can launch, or children would retain flock
	// ownership after a supervisor crash and prevent conservative replacement.
	unix.CloseOnExec(3)
	unix.CloseOnExec(4)
	defer owner.Close()
	defer ready.Close()
	notified := false
	notify := func(err error) {
		response := startup{}
		if err != nil {
			response.Error = err.Error()
		}
		_ = json.NewEncoder(ready).Encode(response)
		ready.Close()
		notified = true
	}
	info, err := ready.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return errors.New("supervisor requires an inherited startup pipe")
	}
	defer func() {
		if !notified {
			notify(err)
		}
	}()
	session, err := unix.Getsid(0)
	if err != nil || session != os.Getpid() {
		return errors.New("supervisor must be detached into its own session")
	}
	paths, err := OpenSharedPaths()
	if err != nil {
		return err
	}
	defer paths.Close()
	// Validate the inherited lock by inode, owner/type/mode and link count before
	// using it. Never infer ownership from a stored PID or a socket pathname.
	expected, err := paths.Runtime.Stat(ownerName, unix.S_IFREG)
	if err != nil {
		return err
	}
	var actual unix.Stat_t
	if err := unix.Fstat(int(owner.Fd()), &actual); err != nil {
		return err
	}
	if actual.Dev != expected.Dev || actual.Ino != expected.Ino {
		return errors.New("invalid inherited supervisor ownership lock")
	}
	if err := unix.Flock(int(owner.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fmt.Errorf("supervisor ownership lock unavailable: %w", err)
	}
	// Also serialize the shared state directory: a different runtime directory
	// must not create a second daemon managing the same project state.
	stateOwner, err := lock(paths.State, "state.lock")
	if err != nil {
		return fmt.Errorf("project state is already owned or unsafe; keep XDG locations stable: %w", err)
	}
	defer stateOwner.Close()
	logger, err := setupDiagnostics(paths.State)
	if err != nil {
		return err
	}
	// Parent removed a proven stale socket before transferring the lock. Do not
	// remove anything here: unexpected paths or live listeners are startup errors.
	// Publish with mode 0600 at bind time. A chmod after bind alone exposes a
	// transient unsafe-mode socket to concurrent launchers' initial probes.
	// No managed execution exists during bootstrap; restore the original
	// process umask before accepting requests so commands don't inherit this.
	mask := unix.Umask(0177)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: paths.Runtime.ProcPath(socketName), Net: "unix"})
	unix.Umask(mask)
	if err != nil {
		return err
	}
	listener.SetUnlinkOnClose(false)
	defer listener.Close()
	if err := unix.Fchmodat(paths.Runtime.FD(), socketName, 0600, 0); err != nil {
		return err
	}
	socketStat, err := paths.Runtime.Stat(socketName, unix.S_IFSOCK)
	if err != nil {
		return err
	}
	defer func() {
		// Remove only the socket this instance published, while still holding lock.
		current, err := paths.Runtime.Stat(socketName, unix.S_IFSOCK)
		if err == nil && current.Ino == socketStat.Ino && current.Dev == socketStat.Dev {
			_ = paths.Runtime.RemoveSocket(socketName)
		}
	}()
	id, err := runtime.InspectProcess(os.Getpid())
	if err != nil {
		return err
	}
	server := &projectServer{projects: make(map[string]*projectSession), logger: logger,
		hello: transport.Hello{BinaryVersion: binaryVersion, Supervisor: id, StateRoot: paths.State.Path}}
	defer server.close()
	logger.Info("supervisor ready")
	notify(nil)
	return transport.Serve(ctx, listener, server.handle)
}

type backend struct {
	manager *runtime.Manager
	root    string
}

func invalid(req transport.Request, err error) transport.Response {
	return transport.Reply(req, nil, &transport.Error{Code: transport.CodeInvalid, Message: err.Error()})
}

func (b *backend) handle(req transport.Request) transport.Response {
	switch req.Operation {
	case transport.SyncConfig:
		var payload transport.SyncPayload
		if err := transport.Decode(req.Payload, &payload); err != nil {
			return invalid(req, err)
		}
		p, err := config.Parse(payload.Config, b.root)
		if err != nil {
			return invalid(req, err)
		}
		if p.ID != req.ProjectID {
			return invalid(req, errors.New("project identity mismatch"))
		}
		if err := b.manager.Sync(p); err != nil {
			return invalid(req, err)
		}
		return transport.Reply(req, struct{}{}, nil)
	case transport.ListState:
		var payload struct{}
		if err := transport.Decode(req.Payload, &payload); err != nil {
			return invalid(req, err)
		}
		return transport.Reply(req, b.manager.State(), nil)
	case transport.Start, transport.Restart:
		var payload transport.StartPayload
		if err := transport.Decode(req.Payload, &payload); err != nil {
			return invalid(req, err)
		}
		env := make([]string, len(payload.Environment))
		for i, value := range payload.Environment {
			env[i] = string(value)
		}
		var run model.Run
		var err error
		if req.Operation == transport.Start {
			run, err = b.manager.Start(payload.Alias, env)
		} else {
			run, err = b.manager.Restart(payload.Alias, env)
		}
		return transport.Reply(req, run, runtimeError(err))
	case transport.Stop:
		var payload transport.AliasPayload
		if err := transport.Decode(req.Payload, &payload); err != nil {
			return invalid(req, err)
		}
		run, err := b.manager.Stop(payload.Alias)
		return transport.Reply(req, run, runtimeError(err))
	case transport.WindowLogs:
		var payload transport.WindowLogsPayload
		if err := transport.Decode(req.Payload, &payload); err != nil {
			return invalid(req, err)
		}
		read, err := b.manager.WindowOutput(payload.Alias, payload.RunID, payload.Anchor, payload.Before, payload.Limit)
		return transport.Reply(req, read, runtimeError(err))
	case transport.SearchLogs:
		var payload transport.SearchLogsPayload
		if err := transport.Decode(req.Payload, &payload); err != nil {
			return invalid(req, err)
		}
		read, err := b.manager.SearchOutput(payload.Alias, payload.RunID, payload.Search)
		return transport.Reply(req, read, runtimeError(err))
	case transport.ReadLogs:
		var payload transport.ReadLogsPayload
		if err := transport.Decode(req.Payload, &payload); err != nil {
			return invalid(req, err)
		}
		var read model.LogRead
		var err error
		if payload.Tail != nil {
			if payload.After != 0 {
				return invalid(req, errors.New("tail and after are mutually exclusive"))
			}
			read, err = b.manager.TailOutput(payload.Alias, payload.RunID, *payload.Tail, payload.Limit)
		} else {
			read, err = b.manager.ReadOutput(payload.Alias, payload.RunID, payload.After, payload.Limit)
		}
		return transport.Reply(req, read, runtimeError(err))
	default:
		return invalid(req, errors.New("unsupported operation"))
	}
}

func runtimeError(err error) *transport.Error {
	if err == nil {
		return nil
	}
	code := transport.CodeRuntime
	for _, pair := range []struct {
		err  error
		code string
	}{
		{runtime.ErrAlreadyRunning, transport.CodeAlreadyRunning}, {runtime.ErrUnmanaged, transport.CodeUnmanaged},
		{runtime.ErrRemoved, transport.CodeRemoved}, {runtime.ErrRestartPending, transport.CodeRestartPending},
		{runtime.ErrRestartBlocked, transport.CodeRestartBlocked}, {runtime.ErrUnknownAlias, transport.CodeUnknownAlias},
		{runtime.ErrRunChanged, transport.CodeRunChanged}, {runtime.ErrCursor, transport.CodeInvalid},
		{logsearch.ErrInvalid, transport.CodeInvalid},
	} {
		if errors.Is(err, pair.err) {
			code = pair.code
			break
		}
	}
	return &transport.Error{Code: code, Message: err.Error()}
}
