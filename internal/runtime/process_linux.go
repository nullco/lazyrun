//go:build linux

package runtime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"lazyrun/internal/model"
)

type procStat struct {
	pid    int
	parent int
	group  int
	state  byte
	start  uint64
}

// /proc/PID/stat's comm field can contain spaces and parentheses; split after
// the final ')' rather than treating the whole file as whitespace-delimited.
func parseStat(data []byte) (procStat, error) {
	str := string(data)
	open, close := strings.IndexByte(str, '('), strings.LastIndexByte(str, ')')
	if open < 1 || close <= open {
		return procStat{}, errors.New("malformed process stat")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(str[:open]))
	if err != nil {
		return procStat{}, err
	}
	f := strings.Fields(str[close+1:]) // Starts at field 3 (state).
	if len(f) < 20 || len(f[0]) != 1 {
		return procStat{}, errors.New("short process stat")
	}
	parent, err := strconv.Atoi(f[1]) // field 4 (ppid)
	if err != nil {
		return procStat{}, err
	}
	group, err := strconv.Atoi(f[2]) // field 5 (pgrp)
	if err != nil {
		return procStat{}, err
	}
	start, err := strconv.ParseUint(f[19], 10, 64) // field 22 (starttime)
	if err != nil {
		return procStat{}, err
	}
	return procStat{pid: pid, parent: parent, group: group, state: f[0][0], start: start}, nil
}

func readStat(pid int) (procStat, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return procStat{}, err
	}
	return parseStat(data)
}

func identity(pid int, bootID string) (model.ProcessIdentity, error) {
	p, err := readStat(pid)
	if err != nil {
		return model.ProcessIdentity{}, err
	}
	if p.group != pid {
		return model.ProcessIdentity{}, fmt.Errorf("process %d has unexpected group %d", pid, p.group)
	}
	return model.ProcessIdentity{PID: pid, PGID: p.group, StartTicks: p.start, BootID: bootID}, nil
}

func verifyLeader(id model.ProcessIdentity) error {
	if id.PID <= 0 || id.PGID != id.PID || id.StartTicks == 0 || id.BootID == "" {
		return errors.New("invalid owned process identity")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return fmt.Errorf("verify boot identity: %w", err)
	}
	if strings.TrimSpace(string(boot)) != id.BootID {
		return errors.New("boot identity changed; refusing to manage recorded PID")
	}
	p, err := readStat(id.PID)
	if err != nil {
		return fmt.Errorf("verify owned group leader: %w", err)
	}
	if p.start != id.StartTicks || p.group != id.PGID {
		return errors.New("process identity changed; refusing to signal or replace run")
	}
	return nil
}

// The shell remains an unreaped child until all ordinary group members have
// stopped executing. Its PID holds the PGID namespace reservation, even as a
// zombie, so a new unrelated process cannot reuse our group ID before Wait.
// Zombies cannot execute, hold pipe descriptors, or fork and are not live.
// New enables child-subreaping, so orphaned descendants' zombies also remain
// owned and unreaped. Two equal zombie-only scans are required: a single /proc
// directory snapshot can miss a child born just before its parent exits.
func groupAlive(id model.ProcessIdentity) (bool, error) {
	first, alive, err := scanGroup(id)
	if err != nil {
		return false, err
	}
	if alive {
		// Avoid accumulating orphan zombies during long-running services.
		// No reaping happens during the zombie-only completion scans below.
		return true, reapZombies(id, first)
	}
	second, alive, err := scanGroup(id)
	if err != nil || alive {
		return alive, err
	}
	if len(first) != len(second) {
		return true, nil // Membership is still settling; leave the identity pinned.
	}
	for pid, p := range first {
		if next, ok := second[pid]; !ok || next != p {
			return true, nil
		}
	}
	return false, nil
}

func scanGroup(id model.ProcessIdentity) (map[int]procStat, bool, error) {
	if err := verifyLeader(id); err != nil {
		return nil, false, err
	}
	members := make(map[int]procStat)
	alive := false
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, false, err
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || !entry.IsDir() {
			continue
		}
		p, err := readStat(pid)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ESRCH) {
			continue
		}
		if err != nil {
			return nil, false, fmt.Errorf("inspect process %d: %w", pid, err)
		}
		if p.group == id.PGID {
			if p.start < id.StartTicks {
				return nil, false, errors.New("group contains a process older than its owned leader")
			}
			members[pid] = p
			if p.state != 'Z' && p.state != 'X' && p.state != 'x' {
				alive = true
			}
		}
	}
	return members, alive, nil
}

// Reap only explicit, identity-verified group descendants after stable completion.
// Never use Wait4(-1): it would race exec.Cmd.Wait for other managed aliases.
func reapDescendants(id model.ProcessIdentity) error {
	members, alive, err := scanGroup(id)
	if err != nil {
		return err
	}
	if alive {
		return errors.New("group unexpectedly became live during reaping")
	}
	return reapZombies(id, members)
}

func reapZombies(id model.ProcessIdentity, members map[int]procStat) error {
	for pid, p := range members {
		// Only our direct (including adopted) zombies are stable wait targets.
		// Another live parent may reap its own child; never race that parent.
		if pid == id.PID || p.parent != os.Getpid() || p.state != 'Z' {
			continue
		}
		current, err := readStat(pid)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if current != p {
			return errors.New("descendant identity changed before reaping")
		}
		var status unix.WaitStatus
		_, err = unix.Wait4(pid, &status, unix.WNOHANG, nil)
		if err != nil && !errors.Is(err, unix.ECHILD) {
			return fmt.Errorf("reap descendant %d: %w", pid, err)
		}
	}
	return nil
}

func terminateGroup(id model.ProcessIdentity) error {
	if err := verifyLeader(id); err != nil {
		return err
	}
	if err := unix.Kill(-id.PGID, unix.SIGTERM); err != nil && !errors.Is(err, unix.ESRCH) {
		return fmt.Errorf("terminate process group: %w", err)
	}
	return nil
}
