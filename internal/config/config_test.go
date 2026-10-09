package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nullco/lazyrun/internal/model"
)

func TestValidConfigOrderDefaultsAndPaths(t *testing.T) {
	root := t.TempDir()
	p, err := Parse([]byte(`version: 1
services:
  zebra:
    command: echo hello
    env: {EMPTY: "", NUMBER: "42"}
  alpha:
    command: exec sleep 60
    cwd: sub/../work
tasks:
  tests:
    command: printf done
`), root)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != filepath.Base(root) || p.Root != root || p.ID != model.ProjectID(root) || p.Shell != "/bin/sh" {
		t.Fatalf("unexpected project: %+v", p)
	}
	if p.Logs != model.DefaultLogSettings() {
		t.Fatalf("logs: %+v", p.Logs)
	}
	if got := []string{p.Services[0].Alias, p.Services[1].Alias, p.Tasks[0].Alias}; !reflect.DeepEqual(got, []string{"zebra", "alpha", "tests"}) {
		t.Fatal(got)
	}
	if p.Services[0].Kind != model.Service || p.Tasks[0].Kind != model.Task {
		t.Fatal("wrong kinds")
	}
	if p.Services[1].Cwd != filepath.Join(root, "work") || p.Tasks[0].Cwd != root {
		t.Fatal("cwd not rooted at project")
	}
	if p.Services[0].Env["NUMBER"] != "42" {
		t.Fatal("env lost")
	}
}

func TestSettingsAndEmptySections(t *testing.T) {
	p, err := Parse([]byte("version: 1\nname: custom\nshell: /bin/bash\nservices: {}\nlogs: {timestamps: true, tail: 0, maxBytes: 1}\n"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "custom" || p.Shell != "/bin/bash" || len(p.Services) != 0 || len(p.Tasks) != 0 || !p.Logs.Timestamps || p.Logs.Tail != 0 || p.Logs.MaxBytes != 1 {
		t.Fatalf("%+v", p)
	}
}

func TestInvalidConfigurations(t *testing.T) {
	cases := []struct{ name, data, want string }{
		{"empty", "", "decode YAML"},
		{"missing version", "services: {}", "version is required"},
		{"unsupported version", "version: 2", "unsupported version"},
		{"unknown root", "version: 1\nservice: {}", "unknown field project.service"},
		{"unknown command", "version: 1\ntasks: {x: {command: echo ok, typo: yes}}", "unknown field tasks.x.typo"},
		{"unknown logs", "version: 1\nlogs: {since: 60m}", "unknown field logs.since"},
		{"duplicate root", "version: 1\nversion: 1", "duplicate key"},
		{"duplicate alias", "version: 1\nservices:\n  x: {command: echo a}\n  x: {command: echo b}", "duplicate key"},
		{"duplicate task", "version: 1\ntasks:\n  x: {command: echo a}\n  x: {command: echo b}", "duplicate key"},
		{"cross kind duplicate", "version: 1\nservices: {x: {command: echo a}}\ntasks: {x: {command: echo b}}", "duplicate alias"},
		{"duplicate command field", "version: 1\ntasks: {x: {command: a, command: b}}", "duplicate key"},
		{"duplicate env", "version: 1\ntasks: {x: {command: a, env: {FOO: a, FOO: b}}}", "duplicate key"},
		{"duplicate logs", "version: 1\nlogs: {tail: 1, tail: 2}", "duplicate key"},
		{"missing command", "version: 1\nservices: {x: {cwd: .}}", "command is required"},
		{"empty command", "version: 1\ntasks: {x: {command: '  '}}", "must not be empty"},
		{"null command", "version: 1\ntasks: {x: {command: null}}", "must be a string"},
		{"numeric alias", "version: 1\ntasks: {1: {command: x}}", "keys must be strings"},
		{"empty alias", "version: 1\ntasks: {'': {command: x}}", "alias must be nonempty"},
		{"control alias", "version: 1\ntasks: {\"a\\tb\": {command: x}}", "alias must be nonempty"},
		{"collection null", "version: 1\nservices:", "must be a mapping"},
		{"sequence", "version: 1\ntasks: []", "must be a mapping"},
		{"env type", "version: 1\ntasks: {x: {command: x, env: {FOO: 42}}}", "must be a string"},
		{"env name", "version: 1\ntasks: {x: {command: x, env: {'X=Y': z}}}", "invalid variable name"},
		{"command NUL", "version: 1\ntasks: {x: {command: \"a\\0b\"}}", "NUL"},
		{"negative tail", "version: 1\nlogs: {tail: -1}", "nonnegative"},
		{"zero maxBytes", "version: 1\nlogs: {maxBytes: 0}", "positive"},
		{"overflow", "version: 1\nlogs: {maxBytes: 9223372036854775808}", "integer"},
		{"string timestamp", "version: 1\nlogs: {timestamps: 'true'}", "boolean"},
		{"relative shell", "version: 1\nshell: bash", "absolute executable"},
		{"extra document", "version: 1\n---\nversion: 1", "one YAML document"},
		{"anchor", "version: 1\ntasks: {x: &x {command: a}}", "anchors and aliases"},
		{"merge", "version: 1\ntasks: {x: {<<: {command: a}}}", "merge keys"},
		{"wrong version type", "version: '1'", "must be an integer"},
		{"malformed", "version: [", "decode YAML"},
	}
	root := t.TempDir()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.data), root)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v; want %q", err, tc.want)
			}
		})
	}
	if _, err := Parse(make([]byte, MaxConfigBytes+1), root); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatal(err)
	}
}

func TestDiscoveryNearestAndCanonicalIdentity(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	sub := filepath.Join(nested, "sub")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{root, nested} {
		if err := os.WriteFile(filepath.Join(dir, Filename), []byte("version: 1"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := Load(sub)
	if err != nil {
		t.Fatal(err)
	}
	if p.Root != nested {
		t.Fatal("did not choose nearest config", p.Root)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(nested, link); err != nil {
		t.Fatal(err)
	}
	q, err := Load(filepath.Join(link, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	if q.ID != p.ID || q.Root != p.Root {
		t.Fatal("symlink changed identity")
	}
	r, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if r.ID == p.ID {
		t.Fatal("distinct projects have same ID")
	}
}

func TestDiscoveryErrorsAndNoExecution(t *testing.T) {
	root := t.TempDir()
	_, err := Load(root)
	if err == nil || !strings.Contains(err.Error(), MinimalExample) {
		t.Fatalf("missing config: %v", err)
	}
	marker := filepath.Join(root, "MUST_NOT_EXIST")
	if err := os.WriteFile(filepath.Join(root, Filename), []byte("version: 1\ntasks:\n  dangerous:\n    command: touch "+marker), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("configuration executed a command")
	}
	if err := os.Remove(filepath.Join(root, Filename)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, Filename), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "regular") {
		t.Fatal(err)
	}
}

func FuzzParse(f *testing.F) {
	root := f.TempDir()
	f.Add([]byte(MinimalExample))
	f.Add([]byte("version: 1\ntasks: {x: {command: echo hi}}"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = Parse(data, root)
	})
}
