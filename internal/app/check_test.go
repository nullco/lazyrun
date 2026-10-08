package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
