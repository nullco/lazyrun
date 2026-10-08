//go:build linux

package runtime

import (
	"lazyrun/internal/model"
	"os"
	"strings"
)

// InspectProcess reports diagnostic identity only. It grants no runtime ownership.
func InspectProcess(pid int) (model.ProcessIdentity, error) {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return model.ProcessIdentity{}, err
	}
	p, err := readStat(pid)
	if err != nil {
		return model.ProcessIdentity{}, err
	}
	return model.ProcessIdentity{PID: p.pid, PGID: p.group, StartTicks: p.start, BootID: strings.TrimSpace(string(boot))}, nil
}
