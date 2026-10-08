//go:build linux

package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lazyrun/internal/model"
	"lazyrun/internal/supervisor"
)

func TestSlowSocketClientDoesNotBlockCaptureOrIndependentAliases(t *testing.T) {
	f := newIntegration(t, pulseConfig)
	r := f.start("pulse", nil)
	f.ready("pulse", r)
	before, err := f.connection.Client.Logs(context.Background(), "pulse", r.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Occupy an authenticated-user server worker without even completing a
	// frame. Output collection and other clients must remain independent.
	slow, err := net.Dial("unix", f.connection.Client.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	task := f.start("env", []string{"SNAPSHOT=independent"})
	f.finished("env", task)
	eventuallyIntegration(t, func() bool {
		read, err := f.connection.Client.Logs(context.Background(), "pulse", r.ID, before.Next, 0)
		return err == nil && len(read.Data) > 0
	})
	if _, err := f.connection.Client.Stop(context.Background(), "pulse"); err != nil {
		t.Fatal(err)
	}
	f.finished("pulse", r)
}

func TestUnsafeMetadataDestinationPreventsExecutionAndReportsFailure(t *testing.T) {
	f := newIntegration(t, pulseConfig)
	paths, err := supervisor.OpenPaths(model.ProjectID(f.root))
	if err != nil {
		t.Fatal(err)
	}
	defer paths.Close()
	sum := sha256.Sum256([]byte("pulse"))
	target := filepath.Join(paths.State.Path, fmt.Sprintf("%x.json", sum))
	if err := os.WriteFile(target, []byte("untrusted"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0644); err != nil {
		t.Fatal(err)
	}
	r, err := f.connection.Client.Start(context.Background(), "pulse", nil)
	f.remember(r)
	if err == nil || r.Identity.PID != 0 || r.Lifecycle != model.Exited || r.Outcome == nil || r.Outcome.Kind != model.LaunchFailed || !strings.Contains(r.MetadataError, "metadata write failed") {
		t.Fatalf("unsafe execution or concealed metadata failure: %+v %v", r, err)
	}
	b, err := os.ReadFile(target)
	if err != nil || string(b) != "untrusted" {
		t.Fatal("modified an unsafe metadata destination", err)
	}
}
