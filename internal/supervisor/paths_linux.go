//go:build linux

package supervisor

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nullco/lazyrun/internal/securefs"
	"golang.org/x/sys/unix"
)

const socketName = "control.sock"
const ownerName = "owner.lock"

type Paths struct{ Runtime, State *securefs.Dir }

// OpenPaths shares the control directory but keeps command state per project.
func OpenPaths(projectID string) (*Paths, error) {
	b, err := hex.DecodeString(projectID)
	if err != nil || len(b) != 32 {
		return nil, errors.New("invalid project identity")
	}
	return openPaths(projectID)
}

func OpenSharedPaths() (*Paths, error) { return openPaths("shared") }

func openPaths(stateID string) (*Paths, error) {
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
	runtimeDir, err := securefs.Open(filepath.Join(base, "shared"), true)
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
	state, err := securefs.Open(filepath.Join(stateApp, stateID), true)
	if err != nil {
		runtimeDir.Close()
		return nil, err
	}
	return &Paths{Runtime: runtimeDir, State: state}, nil
}

func (p *Paths) Close() { p.Runtime.Close(); p.State.Close() }

func lock(dir *securefs.Dir, name string) (*os.File, error) {
	f, err := dir.File(name, unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
