package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadFileBytesUsesExactPathAndBoundedValidation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, Filename), []byte("version: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(nested, Filename)
	if _, _, err := LoadFileBytes(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing exact file must not fall back to parent config", err)
	}
	if _, _, err := LoadFileBytes(nested); err == nil || !strings.Contains(err.Error(), "regular") {
		t.Fatal("accepted directory", err)
	}
	for _, test := range []struct {
		data string
		want string
	}{
		{"version: 1\ntasks: {x: {command: echo hi}}\n", ""},
		{"version: [", "decode YAML"},
		{strings.Repeat(" ", MaxConfigBytes+1), "exceeds"},
	} {
		if err := os.WriteFile(path, []byte(test.data), 0600); err != nil {
			t.Fatal(err)
		}
		p, data, err := LoadFileBytes(path)
		if test.want != "" {
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatal("expected validation failure", test.want, err)
			}
			continue
		}
		if err != nil || string(data) != test.data || p.Root != nested || p.ConfigPath != path {
			t.Fatal("exact source or project path lost", p, err)
		}
	}
}
