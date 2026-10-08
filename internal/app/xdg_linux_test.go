//go:build linux

package app

import (
	"context"
	"os"
	"strings"
	"testing"

	"lazyrun/internal/supervisor"
)

func TestChangedRuntimeDirectoryCannotCreateSecondStateOwner(t *testing.T) {
	f := newIntegration(t, pulseConfig)
	second := t.TempDir()
	if err := os.Chmod(second, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", second)
	c, err := Connect(context.Background(), f.root, supervisor.LaunchOptions{Executable: testExecutable})
	if c != nil {
		c.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "state is already owned") {
		t.Fatal("created a second authority through a different runtime directory", err)
	}
	for _, item := range f.state().Commands {
		if item.Run.ID != "" {
			t.Fatal("changing runtime directory executed a command")
		}
	}
}
