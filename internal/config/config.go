// Package config discovers and strictly decodes one project's lazyrun.yml.
// Loading configuration never executes a command.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"

	"lazyrun/internal/model"
)

const (
	Filename       = "lazyrun.yml"
	MaxConfigBytes = 1024 * 1024
	MinimalExample = "version: 1\nservices:\n  api:\n    command: .venv/bin/python -u -m flask run\ntasks:\n  tests:\n    command: .venv/bin/python -m pytest\n"
)

// Discover walks the physical directory hierarchy. Symlinked invocation paths
// and invocations from subdirectories therefore share a canonical identity.
func Discover(dir string) (string, error) {
	root, err := canonicalDir(dir)
	if err != nil {
		return "", err
	}
	for {
		path := filepath.Join(root, Filename)
		info, err := os.Lstat(path)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				info, err = os.Stat(path)
				if err != nil {
					return "", fmt.Errorf("resolve configuration symlink %s: %w", path, err)
				}
			}
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("%s must be a regular configuration file", path)
			}
			return path, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect %s: %w", path, err)
		}
		parent := filepath.Dir(root)
		if parent == root {
			return "", fmt.Errorf("no %s found in %s or its parents; create a trusted configuration, for example:\n\n%s", Filename, dir, MinimalExample)
		}
		root = parent
	}
}

func Load(dir string) (model.Project, error) {
	project, _, err := LoadBytes(dir)
	return project, err
}

// LoadBytes returns the exact validated source for supervisor synchronization.
func LoadBytes(dir string) (model.Project, []byte, error) {
	path, err := Discover(dir)
	if err != nil {
		return model.Project{}, nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return model.Project{}, nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxConfigBytes+1))
	if err != nil {
		return model.Project{}, nil, fmt.Errorf("read %s: %w", path, err)
	}
	project, err := Parse(data, filepath.Dir(path))
	if err != nil {
		return model.Project{}, nil, fmt.Errorf("%s: %w", path, err)
	}
	return project, data, nil
}

// Parse preserves mapping order using YAML nodes, while explicitly validating
// keys and scalar types at every schema level. Aliases/anchors/merge keys are
// deliberately unsupported; definitions should remain explicit and auditable.
func Parse(data []byte, root string) (model.Project, error) {
	if len(data) > MaxConfigBytes {
		return model.Project{}, fmt.Errorf("configuration exceeds %d bytes", MaxConfigBytes)
	}
	root, err := canonicalDir(root)
	if err != nil {
		return model.Project{}, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return model.Project{}, fmt.Errorf("decode YAML: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err != io.EOF {
		if err != nil {
			return model.Project{}, fmt.Errorf("decode YAML: %w", err)
		}
		return model.Project{}, errors.New("only one YAML document is allowed")
	}
	if len(doc.Content) != 1 {
		return model.Project{}, errors.New("expected a configuration mapping")
	}
	if err := explicitNodes(doc.Content[0]); err != nil {
		return model.Project{}, err
	}
	fields, err := mapping(doc.Content[0], "project", "version", "name", "shell", "services", "tasks", "logs")
	if err != nil {
		return model.Project{}, err
	}
	v, ok := fields["version"]
	if !ok {
		return model.Project{}, errors.New("version is required (supported: 1)")
	}
	version, err := integer(v, "version")
	if err != nil {
		return model.Project{}, err
	}
	if version != 1 {
		return model.Project{}, fmt.Errorf("unsupported version %d (supported: 1)", version)
	}
	p := model.Project{
		ID: model.ProjectID(root), Name: filepath.Base(root), Root: root,
		ConfigPath: filepath.Join(root, Filename), Shell: "/bin/sh", Logs: model.DefaultLogSettings(),
		Services: []model.Definition{}, Tasks: []model.Definition{},
	}
	if n, ok := fields["name"]; ok {
		p.Name, err = text(n, "name", true)
		if err != nil {
			return model.Project{}, err
		}
	}
	if n, ok := fields["shell"]; ok {
		p.Shell, err = text(n, "shell", true)
		if err != nil {
			return model.Project{}, err
		}
		if !filepath.IsAbs(p.Shell) {
			return model.Project{}, errors.New("shell must be an absolute executable path (for example /bin/bash)")
		}
	}
	seen := make(map[string]string)
	for _, section := range []struct {
		name string
		kind model.Kind
		dest *[]model.Definition
	}{
		{"services", model.Service, &p.Services}, {"tasks", model.Task, &p.Tasks},
	} {
		n, ok := fields[section.name]
		if !ok {
			continue
		}
		if _, err := mapping(n, section.name); err != nil {
			return model.Project{}, err
		}
		for i := 0; i < len(n.Content); i += 2 {
			alias := n.Content[i].Value
			if strings.TrimSpace(alias) == "" || strings.IndexFunc(alias, unicode.IsControl) >= 0 {
				return model.Project{}, at(n.Content[i], "%s alias must be nonempty printable text", section.name)
			}
			if previous, ok := seen[alias]; ok {
				return model.Project{}, at(n.Content[i], "duplicate alias %q in %s (already in %s)", alias, section.name, previous)
			}
			seen[alias] = section.name
			path := section.name + "." + alias
			f, err := mapping(n.Content[i+1], path, "command", "cwd", "env")
			if err != nil {
				return model.Project{}, err
			}
			command, ok := f["command"]
			if !ok {
				return model.Project{}, at(n.Content[i+1], "%s.command is required", path)
			}
			d := model.Definition{Alias: alias, Kind: section.kind, Cwd: root}
			d.Command, err = text(command, path+".command", true)
			if err != nil {
				return model.Project{}, err
			}
			if cwd, ok := f["cwd"]; ok {
				d.Cwd, err = text(cwd, path+".cwd", true)
				if err != nil {
					return model.Project{}, err
				}
				if !filepath.IsAbs(d.Cwd) {
					d.Cwd = filepath.Join(root, d.Cwd)
				}
				d.Cwd = filepath.Clean(d.Cwd)
			}
			if env, ok := f["env"]; ok {
				e, err := mapping(env, path+".env")
				if err != nil {
					return model.Project{}, err
				}
				d.Env = make(map[string]string, len(e))
				for k, value := range e {
					if k == "" || strings.ContainsAny(k, "=\x00") {
						return model.Project{}, at(env, "%s.env has invalid variable name %q", path, k)
					}
					d.Env[k], err = text(value, path+".env."+k, false)
					if err != nil {
						return model.Project{}, err
					}
				}
			}
			*section.dest = append(*section.dest, d)
		}
	}
	if logs, ok := fields["logs"]; ok {
		f, err := mapping(logs, "logs", "timestamps", "tail", "maxBytes")
		if err != nil {
			return model.Project{}, err
		}
		if n, ok := f["timestamps"]; ok {
			if n.Kind != yaml.ScalarNode || n.Tag != "!!bool" {
				return model.Project{}, at(n, "logs.timestamps must be a boolean")
			}
			if err := n.Decode(&p.Logs.Timestamps); err != nil {
				return model.Project{}, err
			}
		}
		if n, ok := f["tail"]; ok {
			x, err := integer(n, "logs.tail")
			if err != nil {
				return model.Project{}, err
			}
			if x < 0 || x > int64(^uint(0)>>1) {
				return model.Project{}, at(n, "logs.tail must be a nonnegative integer fitting this platform")
			}
			p.Logs.Tail = int(x)
		}
		if n, ok := f["maxBytes"]; ok {
			x, err := integer(n, "logs.maxBytes")
			if err != nil {
				return model.Project{}, err
			}
			if x <= 0 {
				return model.Project{}, at(n, "logs.maxBytes must be positive")
			}
			p.Logs.MaxBytes = x
		}
	}
	return p, nil
}

func canonicalDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve directory: %w", err)
	}
	root, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", abs, err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", root)
	}
	return root, nil
}

func explicitNodes(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return at(n, "YAML anchors and aliases are unsupported; write definitions explicitly")
	}
	for _, child := range n.Content {
		if err := explicitNodes(child); err != nil {
			return err
		}
	}
	return nil
}

// An absent allow-list means arbitrary string mapping keys (aliases/env names).
func mapping(n *yaml.Node, path string, allowed ...string) (map[string]*yaml.Node, error) {
	if n.Kind != yaml.MappingNode || n.Tag != "!!map" {
		return nil, at(n, "%s must be a mapping (use {} for an empty collection)", path)
	}
	out := make(map[string]*yaml.Node, len(n.Content)/2)
	for i := 0; i < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Kind != yaml.ScalarNode || k.Tag != "!!str" {
			return nil, at(k, "%s keys must be strings; YAML merge keys are unsupported", path)
		}
		if _, exists := out[k.Value]; exists {
			return nil, at(k, "duplicate key %q in %s", k.Value, path)
		}
		if len(allowed) != 0 {
			found := false
			for _, name := range allowed {
				if name == k.Value {
					found = true
					break
				}
			}
			if !found {
				return nil, at(k, "unknown field %s.%s", path, k.Value)
			}
		}
		out[k.Value] = v
	}
	return out, nil
}

func text(n *yaml.Node, path string, nonempty bool) (string, error) {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		return "", at(n, "%s must be a string (quote numeric/boolean environment values)", path)
	}
	if strings.ContainsRune(n.Value, 0) {
		return "", at(n, "%s must not contain NUL bytes", path)
	}
	if nonempty && strings.TrimSpace(n.Value) == "" {
		return "", at(n, "%s must not be empty", path)
	}
	return n.Value, nil
}

func integer(n *yaml.Node, path string) (int64, error) {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!int" {
		return 0, at(n, "%s must be an integer", path)
	}
	var value int64
	if err := n.Decode(&value); err != nil {
		return 0, at(n, "%s must fit a signed 64-bit integer: %s", path, strconv.Quote(n.Value))
	}
	return value, nil
}

func at(n *yaml.Node, format string, args ...any) error {
	return fmt.Errorf("line %d: %s", n.Line, fmt.Sprintf(format, args...))
}
