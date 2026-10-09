//go:build linux

package runtime

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/nullco/lazyrun/internal/model"
	"golang.org/x/sys/unix"
)

// ReconcileRecorded only observes. It never terminates/reaps/adopts a recorded
// PID. Signal zero is an existence probe, not permission for a future signal.
func ReconcileRecorded(saved model.Run) (model.Run, error) {
	r := saved.Clone()
	if !r.Lifecycle.Active() {
		return r, nil
	}
	r.Outcome = nil
	r.EndedAt = nil
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return r, err
	}
	unknown := func(reason string) (model.Run, error) {
		r.Lifecycle = model.Unknown
		r.Error = reason + "; " + ErrUnmanaged.Error()
		return r, nil
	}
	gone := func(reason string) (model.Run, error) {
		r.Lifecycle = model.Exited
		r.Error = reason + "; actual exit outcome unavailable after supervisor loss"
		return r, nil
	}
	if r.Identity.BootID != "" && r.Identity.BootID != strings.TrimSpace(string(boot)) {
		return gone("record belongs to a previous boot")
	}
	if r.Identity.PID <= 1 || r.Identity.PID > math.MaxInt32 || r.Identity.PGID != r.Identity.PID || r.Identity.StartTicks == 0 || r.Identity.BootID == "" {
		return unknown("launch identity was not durably recorded")
	}
	// Do not infer absence from /proc enumeration: an unowned process can fork
	// between enumeration and inspection, and PID 1 may reap the old member.
	// The kernel probe is atomic and deliberately includes zombies. Reused groups
	// and permission errors also block; none become ownership/signaling authority.
	err = unix.Kill(-r.Identity.PGID, 0)
	if errors.Is(err, unix.ESRCH) {
		return gone("recorded process group no longer exists")
	}
	if err != nil && !errors.Is(err, unix.EPERM) {
		return unknown(fmt.Sprintf("cannot verify group: %v", err))
	}
	return unknown("recorded process group is occupied or not verifiable")
}
