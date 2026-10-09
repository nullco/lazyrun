//go:build linux

package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/nullco/lazyrun/internal/model"
)

type Client struct{ Endpoint, ProjectID string }

func (c *Client) exchange(conn *net.UnixConn, op Operation, payload, out any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	id := hex.EncodeToString(random[:])
	if err := writeFrame(conn, Request{Version: ProtocolVersion, ID: id, Operation: op, Payload: b}); err != nil {
		return err
	}
	var resp Response
	if err := readFrame(conn, &resp); err != nil {
		return err
	}
	if resp.Version != ProtocolVersion {
		return &Error{Code: CodeIncompatible, Message: "response protocol version mismatch"}
	}
	if resp.ID != id {
		return errors.New("IPC response identity mismatch")
	}
	if out != nil && len(resp.Payload) > 0 {
		if err := json.Unmarshal(resp.Payload, out); err != nil {
			return err
		}
	}
	if resp.Error != nil {
		return resp.Error
	}
	return nil
}

// Every request uses a fresh, authenticated connection. There are no implicit
// retries: a failed response may follow an accepted mutation. Query state first.
func (c *Client) call(ctx context.Context, op Operation, payload, out any) error {
	dialer := net.Dialer{Timeout: IOTimeout}
	raw, err := dialer.DialContext(ctx, "unix", c.Endpoint)
	if err != nil {
		return err
	}
	conn := raw.(*net.UnixConn)
	defer conn.Close()
	deadline := time.Now().Add(IOTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn.SetDeadline(deadline)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	if err := checkPeer(conn); err != nil {
		return err
	}
	var hello Hello
	if err := c.exchange(conn, Handshake, HandshakePayload{ProjectID: c.ProjectID}, &hello); err != nil {
		return err
	}
	if hello.ProjectID != c.ProjectID {
		return fmt.Errorf("supervisor project mismatch: %s", hello.ProjectID)
	}
	if op == Handshake {
		if out != nil {
			*(out.(*Hello)) = hello
		}
		return nil
	}
	return c.exchange(conn, op, payload, out)
}

func (c *Client) Hello(ctx context.Context) (Hello, error) {
	var hello Hello
	err := c.call(ctx, Handshake, nil, &hello)
	return hello, err
}
func (c *Client) Sync(ctx context.Context, config []byte) error {
	return c.call(ctx, SyncConfig, SyncPayload{Config: config}, nil)
}
func (c *Client) State(ctx context.Context) (model.State, error) {
	var state model.State
	err := c.call(ctx, ListState, struct{}{}, &state)
	return state, err
}
func (c *Client) Start(ctx context.Context, alias string, env []string) (model.Run, error) {
	var run model.Run
	err := c.call(ctx, Start, startPayload(alias, env), &run)
	return run, err
}
func startPayload(alias string, env []string) StartPayload {
	values := make([][]byte, len(env))
	for i, value := range env {
		values[i] = []byte(value)
	}
	return StartPayload{Alias: alias, Environment: values}
}

func (c *Client) Stop(ctx context.Context, alias string) (model.Run, error) {
	var run model.Run
	err := c.call(ctx, Stop, AliasPayload{Alias: alias}, &run)
	return run, err
}
func (c *Client) Restart(ctx context.Context, alias string, env []string) (model.Run, error) {
	var run model.Run
	err := c.call(ctx, Restart, startPayload(alias, env), &run)
	return run, err
}
func (c *Client) TailLogs(ctx context.Context, alias, runID string, lines, limit int) (model.LogRead, error) {
	var result model.LogRead
	err := c.call(ctx, ReadLogs, ReadLogsPayload{Alias: alias, RunID: runID, Tail: &lines, Limit: limit}, &result)
	return result, err
}

func (c *Client) Logs(ctx context.Context, alias, runID string, after uint64, limit int) (model.LogRead, error) {
	var result model.LogRead
	err := c.call(ctx, ReadLogs, ReadLogsPayload{Alias: alias, RunID: runID, After: after, Limit: limit}, &result)
	return result, err
}
