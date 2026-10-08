//go:build linux

package transport

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const MaxClients = 32
const IOTimeout = 10 * time.Second

func checkPeer(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var peer *unix.Ucred
	var sockErr error
	if err := raw.Control(func(fd uintptr) { peer, sockErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil {
		return err
	}
	if sockErr != nil {
		return sockErr
	}
	if peer.Uid != uint32(os.Geteuid()) {
		return errors.New("Unix socket peer belongs to a different user")
	}
	return nil
}

// Serve bounds connections and I/O, but never cancels accepted lifecycle work
// on disconnection. The handler must not derive managed lifetimes from ctx.
func Serve(ctx context.Context, listener *net.UnixListener, handle func(Request) Response) error {
	var mu sync.Mutex
	clients := make(map[*net.UnixConn]bool)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
		case <-stop:
			return
		}
		listener.Close()
		mu.Lock()
		defer mu.Unlock()
		for c := range clients {
			c.Close()
		}
	}()
	for {
		c, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		mu.Lock()
		if len(clients) >= MaxClients {
			mu.Unlock()
			c.Close()
			continue
		}
		clients[c] = true
		mu.Unlock()
		go func() {
			defer func() { c.Close(); mu.Lock(); delete(clients, c); mu.Unlock() }()
			if checkPeer(c) != nil {
				return
			}
			authenticated := false
			for {
				c.SetReadDeadline(time.Now().Add(IOTimeout))
				var req Request
				if readFrame(c, &req) != nil {
					return
				}
				var resp Response
				switch {
				case req.Version != ProtocolVersion:
					resp = Reply(req, nil, &Error{Code: CodeIncompatible, Message: "protocol version mismatch"})
				case !authenticated && req.Operation != Handshake:
					resp = Reply(req, nil, &Error{Code: CodeInvalid, Message: "handshake required before operations"})
				default:
					resp = handle(req)
					if req.Operation == Handshake && resp.Error == nil {
						authenticated = true
					}
				}
				c.SetWriteDeadline(time.Now().Add(IOTimeout))
				if writeFrame(c, resp) != nil {
					return
				}
				if !authenticated {
					return
				}
			}
		}()
	}
}
