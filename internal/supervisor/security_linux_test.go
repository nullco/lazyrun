//go:build linux

package supervisor

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"lazyrun/internal/config"
	"lazyrun/internal/model"
	"lazyrun/internal/transport"
)

func testPaths(t *testing.T) (model.Project, *Paths) {
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
	p, err := config.Parse([]byte("version: 1"), base)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := OpenPaths(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(paths.Close)
	return p, paths
}

func TestRefuseSocketSymlinksAndLockHardlinks(t *testing.T) {
	p, paths := testPaths(t)
	target := filepath.Join(t.TempDir(), "unrelated")
	if err := os.WriteFile(target, []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(paths.Runtime.Path, socketName)); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(context.Background(), p, LaunchOptions{Executable: "/must/not/launch"}); err == nil {
		t.Fatal("followed socket symlink")
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "safe" {
		t.Fatal("modified unrelated target")
	}
	if err := os.Remove(filepath.Join(paths.Runtime.Path, socketName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, filepath.Join(paths.Runtime.Path, ownerName)); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(context.Background(), p, LaunchOptions{Executable: "/must/not/launch"}); err == nil {
		t.Fatal("accepted hardlinked ownership lock")
	}
}

func TestUnreachableOwnerNeverPermitsSecondLaunch(t *testing.T) {
	p, paths := testPaths(t)
	owner, err := lock(paths.Runtime, ownerName, true)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = Ensure(ctx, p, LaunchOptions{Executable: "/must/not/launch"})
	if err == nil || !strings.Contains(err.Error(), "holds ownership") {
		t.Fatal("did not fail closed on unavailable owner", err)
	}
}

func TestIncompatibleListenerIsNotRemovedOrReplaced(t *testing.T) {
	p, paths := testPaths(t)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: paths.Runtime.ProcPath(socketName), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	defer listener.Close()
	if err := unix.Fchmodat(paths.Runtime.FD(), socketName, 0600, 0); err != nil {
		t.Fatal(err)
	}
	before, err := paths.Runtime.Stat(socketName, unix.S_IFSOCK)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			return
		}
		defer conn.Close()
		var header [4]byte
		if _, err := io.ReadFull(conn, header[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(header[:])
		if n > transport.MaxFrameBytes {
			return
		}
		data := make([]byte, int(n))
		if _, err := io.ReadFull(conn, data); err != nil {
			return
		}
		var request transport.Request
		if json.Unmarshal(data, &request) != nil {
			return
		}
		response, _ := json.Marshal(transport.Response{Version: transport.ProtocolVersion + 1, ID: request.ID, Error: &transport.Error{Code: transport.CodeIncompatible, Message: "different protocol"}})
		binary.BigEndian.PutUint32(header[:], uint32(len(response)))
		_, _ = conn.Write(append(header[:], response...))
	}()
	_, err = Ensure(context.Background(), p, LaunchOptions{Executable: "/must/not/launch"})
	var remote *transport.Error
	if !errors.As(err, &remote) || remote.Code != transport.CodeIncompatible {
		t.Fatal("incompatible server prompted relaunch", err)
	}
	after, err := paths.Runtime.Stat(socketName, unix.S_IFSOCK)
	if err != nil || before.Ino != after.Ino {
		t.Fatal("removed incompatible socket", err)
	}
}

func TestStoreBoundsAtomicMetadataAndRejectsCorruption(t *testing.T) {
	p, paths := testPaths(t)
	s := store{dir: paths.State, projectID: p.ID}
	r := model.Run{ID: "run", ProjectID: p.ID, Lifecycle: model.Running, Definition: model.Definition{Alias: "../odd/alias", Kind: model.Service, Env: map[string]string{"SECRET": "do-not-persist"}}}
	if err := s.Save(r); err != nil {
		t.Fatal(err)
	}
	name := metadataName(r.Definition.Alias)
	b, err := paths.State.Read(name, maxMetadataBytes)
	if err != nil || strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "do-not-persist") {
		t.Fatal("environment leaked", err)
	}
	runs, err := s.Load()
	if err != nil || len(runs) != 1 || runs[0].Definition.Alias != r.Definition.Alias {
		t.Fatal(runs, err)
	}
	if err := paths.State.AtomicWrite(name, []byte(`{"version":999}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("accepted incompatible metadata")
	}
}

func TestDiagnosticsRemainBounded(t *testing.T) {
	_, paths := testPaths(t)
	file, err := paths.State.File("diagnostic.log", unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	b := boundedDiagnostic{file: file}
	for range 10 {
		if _, err := b.Write([]byte(strings.Repeat("x", diagnosticLimit/3))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.Write([]byte(strings.Repeat("y", diagnosticLimit+10))); err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil || info.Size() > diagnosticLimit {
		t.Fatal("unbounded diagnostics", info, err)
	}
}
