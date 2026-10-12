//go:build linux

package transport

import (
	"context"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nullco/lazyrun/internal/model"
)

func serveTestClient(t *testing.T, handle func(Request) Response) *Client {
	t.Helper()
	endpoint := filepath.Join(t.TempDir(), "control.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: endpoint, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, listener, handle) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return &Client{Endpoint: endpoint, ProjectID: "project"}
}

func TestRestartPreflightFailureNeverSendsMutation(t *testing.T) {
	var mutations atomic.Int32
	c := serveTestClient(t, func(req Request) Response {
		switch req.Operation {
		case Handshake:
			return Reply(req, Hello{ProjectID: req.ProjectID}, nil)
		case ListState:
			return Reply(req, nil, &Error{Code: CodeRuntime, Message: "state unavailable"})
		default:
			mutations.Add(1)
			return Reply(req, nil, nil)
		}
	})
	_, err := c.Restart(context.Background(), "worker", nil)
	if err == nil || mutations.Load() != 0 {
		t.Fatal("failed preflight sent a lifecycle request", err, mutations.Load())
	}
}

func TestRestartPreservesCallerDeadline(t *testing.T) {
	accepted := make(chan struct{}, 1)
	release := make(chan struct{})
	c := serveTestClient(t, func(req Request) Response {
		switch req.Operation {
		case Handshake:
			return Reply(req, Hello{ProjectID: req.ProjectID}, nil)
		case ListState:
			d := model.Definition{Alias: "worker", RestartTimeout: 2 * time.Minute}
			return Reply(req, model.State{Commands: []model.CommandState{{Definition: &d, Run: model.Run{Definition: d}}}}, nil)
		case Restart:
			accepted <- struct{}{}
			<-release
			return Reply(req, model.Run{}, nil)
		default:
			return Reply(req, nil, &Error{Code: CodeInvalid})
		}
	})
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.Restart(ctx, "worker", nil); done <- err }()
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("restart request was not accepted")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("caller deadline was ignored")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("configured wait overrode the caller's deadline")
	}
}
