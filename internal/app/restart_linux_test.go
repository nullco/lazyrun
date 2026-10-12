//go:build linux

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nullco/lazyrun/internal/model"
	"github.com/nullco/lazyrun/internal/testutil"
)

func TestCLIRestartWaitsBeyondOrdinaryIPCDeadlineAfterReload(t *testing.T) {
	command, err := testutil.FixtureCommand("slow-service")
	if err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf("version: 1\nservices:\n  slow:\n    command: %q\n    restart_timeout: 1s\n    env: {SHUTDOWN_DELAY: 12s}\n", command)
	f := newIntegration(t, data)
	old := f.start("slow", nil)
	f.ready("slow", old)
	data = strings.Replace(data, "restart_timeout: 1s", "restart_timeout: 15s", 1)
	f.write(data)
	if err := f.connection.Client.Sync(context.Background(), []byte(data)); err != nil {
		t.Fatal(err)
	}
	state := f.state().Commands[0]
	if state.Run.Definition.RestartTimeout != time.Second || state.Definition.RestartTimeout != 15*time.Second {
		t.Fatal("reload lost the distinction between run and current configuration", state)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, testExecutable, "--restart", "slow")
	cmd.Dir = f.root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal("restart hit a fixed client deadline", err, string(output))
	}
	var next model.Run
	if err := json.Unmarshal(output, &next); err != nil {
		t.Fatal(err, string(output))
	}
	f.remember(next)
	if next.ID == old.ID || next.Definition.RestartTimeout != 15*time.Second || sameProcess(old.Identity) {
		t.Fatal("restart returned before graceful completion or ignored reloaded timeout", next)
	}
	f.ready("slow", next)
}
