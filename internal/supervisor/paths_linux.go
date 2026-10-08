//go:build linux

package supervisor

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
	"lazyrun/internal/securefs"
)

const socketName = "control.sock"
const ownerName = "owner.lock"

type Paths struct{ Runtime, State *securefs.Dir }

func OpenPaths(projectID string) (*Paths, error) {
	b, err := hex.DecodeString(projectID)
	if err != nil || len(b) != 32 {
		return nil, errors.New("invalid project identity")
	}
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base != "" {
		d, err := securefs.Open(base, false)
		if err != nil {
			return nil, fmt.Errorf("XDG_RUNTIME_DIR: %w", err)
		}
		d.Close()
		base = filepath.Join(base, "lazyrun")
	} else {
		base = filepath.Join("/tmp", fmt.Sprintf("lazyrun-%d", os.Geteuid()))
	}
	privateBase, err := securefs.Open(base, true)
	if err != nil {
		return nil, err
	}
	privateBase.Close()
	runtimeDir, err := securefs.Open(filepath.Join(base, projectID), true)
	if err != nil {
		return nil, err
	}
	stateBase := os.Getenv("XDG_STATE_HOME")
	if stateBase == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			runtimeDir.Close()
			return nil, err
		}
		stateBase = filepath.Join(home, ".local", "state")
	}
	stateApp := filepath.Join(stateBase, "lazyrun")
	privateState, err := securefs.Open(stateApp, true)
	if err != nil {
		runtimeDir.Close()
		return nil, err
	}
	privateState.Close()
	state, err := securefs.Open(filepath.Join(stateApp, projectID), true)
	if err != nil {
		runtimeDir.Close()
		return nil, err
	}
	return &Paths{Runtime: runtimeDir, State: state}, nil
}

func (p *Paths) Close() { p.Runtime.Close(); p.State.Close() }

func lock(dir *securefs.Dir, name string, nonblock bool) (*os.File, error) {
	f, err := dir.File(name, unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return nil, err
	}
	flags := unix.LOCK_EX
	if nonblock {
		flags |= unix.LOCK_NB
	}
	if err := unix.Flock(int(f.Fd()), flags); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
