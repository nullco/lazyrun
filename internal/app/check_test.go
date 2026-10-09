package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckEscapesTerminalControls(t *testing.T) {
	root := t.TempDir()
	data := "version: 1\nname: \"bad\\e]52;c;payload\\a\"\ntasks: {x: {command: echo hi}}"
	if err := os.WriteFile(filepath.Join(root, "lazyrun.yml"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Check(root, &out); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out.String(), "\x1b\a") || !strings.Contains(out.String(), "\\x1b]52;c;payload\\a") {
		t.Fatal(out.String())
	}
}

func TestCheck(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "lazyrun.yml"), []byte("version: 1\nname: example\ntasks: {tests: {command: echo hi}}"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Check(root, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Project: example", "Services: 0", "Tasks: 1", "task tests: not started"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
}
