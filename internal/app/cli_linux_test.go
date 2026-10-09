//go:build linux

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInvalidCLIFlagsNeverLaunchOrExecute(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(base, "lazyrun.yml"), []byte("version: 1\ntasks: {x: {command: touch must-not-exist}}"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--start", ""}, {"--stop", " "}, {"--restart", ""}, {"--logs", ""}, {"--logs", "x", "--run-id", ""},
		{"--start", "x", "--start", "x"}, {"--stop", "x", "--stop", "x"}, {"--restart", "x", "--restart", "x"},
		{"--logs", "x", "--logs", "x"}, {"--logs", "x", "--run-id", "one", "--run-id", "two"},
		{"--state", "--start", "x"}, {"--check", "--start", "x"}, {"--start", "x", "--stop", "x"},
		{"--logs", "x", "--tail", "-1"}, {"--logs", "x", "--after", ""}, {"--logs", "x", "--tail", "1", "--after", "0"},
	} {
		cmd := exec.Command(testExecutable, args...)
		cmd.Dir = base
		if output, err := cmd.CombinedOutput(); err == nil {
			t.Fatal("accepted ambiguous/invalid flags", args, string(output))
		}
	}
	for _, path := range []string{filepath.Join(base, "must-not-exist"), filepath.Join(runtimeDir, "lazyrun"), filepath.Join(base, "state")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("invalid CLI caused a side effect", path, err)
		}
	}
}
func TestCLIParserDiagnosticsEscapeTerminalControls(t *testing.T) {
	for _, args := range [][]string{{"--bad\x1b]52;c;payload\a"}, {"unexpected\u202e"}, {"--tail", "bad\x1b[2J"}} {
		output, err := exec.Command(testExecutable, args...).CombinedOutput()
		if err == nil || strings.ContainsAny(string(output), "\x1b\a\u202e") {
			t.Fatal(args, string(output), err)
		}
	}
}

func TestCLIErrorDiagnosticsDoNotExecuteTerminalControls(t *testing.T) {
	root := filepath.Join(t.TempDir(), "unsafe\x1b]52;c;payload\a")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "lazyrun.yml"), []byte("version: 999"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(testExecutable, "--check")
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err == nil || strings.ContainsAny(string(output), "\x1b\a") || !strings.Contains(string(output), "unsupported version") {
		t.Fatal(string(output), err)
	}
}
