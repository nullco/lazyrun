// Package model contains configuration and run state shared by the runtime and
// future supervisor/transport/UI. It has no process or terminal dependencies.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

type Kind string

const (
	Service Kind = "service"
	Task    Kind = "task"
)

type LogSettings struct {
	Timestamps bool  `json:"timestamps"`
	Tail       int   `json:"tail"`
	MaxBytes   int64 `json:"maxBytes"`
}

func DefaultLogSettings() LogSettings {
	return LogSettings{Tail: 1000, MaxBytes: 10 * 1024 * 1024}
}

type Definition struct {
	Alias   string            `json:"alias"`
	Kind    Kind              `json:"kind"`
	Command string            `json:"command"`
	Cwd     string            `json:"cwd"`
	Env     map[string]string `json:"-"` // Never include environment values in state/metadata.
}

func (d Definition) Clone() Definition {
	if d.Env != nil {
		env := make(map[string]string, len(d.Env))
		for k, v := range d.Env {
			env[k] = v
		}
		d.Env = env
	}
	return d
}

type Project struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Root       string       `json:"root"`
	ConfigPath string       `json:"configPath"`
	Shell      string       `json:"shell"`
	Logs       LogSettings  `json:"logs"`
	Services   []Definition `json:"services"`
	Tasks      []Definition `json:"tasks"`
}

// ProjectID must be given an absolute, symlink-canonicalized project root.
func ProjectID(root string) string {
	sum := sha256.Sum256([]byte(root))
	return hex.EncodeToString(sum[:])
}

func (p Project) Clone() Project {
	p.Services = cloneDefinitions(p.Services)
	p.Tasks = cloneDefinitions(p.Tasks)
	return p
}

func cloneDefinitions(defs []Definition) []Definition {
	result := make([]Definition, len(defs))
	for i, d := range defs {
		result[i] = d.Clone()
	}
	return result
}

func (p Project) Definitions() []Definition {
	defs := make([]Definition, 0, len(p.Services)+len(p.Tasks))
	for _, d := range p.Services {
		defs = append(defs, d.Clone())
	}
	for _, d := range p.Tasks {
		defs = append(defs, d.Clone())
	}
	return defs
}

type Lifecycle string

const (
	NotStarted Lifecycle = "not started"
	Starting   Lifecycle = "starting"
	Running    Lifecycle = "running"
	Stopping   Lifecycle = "stopping"
	Exited     Lifecycle = "exited"
	Unknown    Lifecycle = "unknown"
)

func (l Lifecycle) Active() bool {
	return l == Starting || l == Running || l == Stopping || l == Unknown
}

type OutcomeKind string

const (
	Success      OutcomeKind = "success"
	NonzeroExit  OutcomeKind = "nonzero exit"
	Signaled     OutcomeKind = "signal termination"
	LaunchFailed OutcomeKind = "launch failure"
)

type Outcome struct {
	Kind     OutcomeKind `json:"kind"`
	ExitCode *int        `json:"exitCode,omitempty"`
	Signal   int         `json:"signal,omitempty"`
	Error    string      `json:"error,omitempty"`
}

// ProcessIdentity supplements PID/PGID with Linux boot and process start identity.
// Persisted identity is diagnostic information, NOT permission to signal a PID.
type ProcessIdentity struct {
	PID        int    `json:"pid"`
	PGID       int    `json:"pgid"`
	StartTicks uint64 `json:"startTicks"`
	BootID     string `json:"bootId"`
}

type Run struct {
	ID            string          `json:"id"`
	ProjectID     string          `json:"projectId"`
	Definition    Definition      `json:"definition"`
	Shell         string          `json:"shell"`
	Lifecycle     Lifecycle       `json:"lifecycle"`
	Identity      ProcessIdentity `json:"identity"`
	StartedAt     time.Time       `json:"startedAt"`
	EndedAt       *time.Time      `json:"endedAt,omitempty"`
	StopRequested bool            `json:"stopRequested"`
	Outcome       *Outcome        `json:"outcome,omitempty"`
	Error         string          `json:"error,omitempty"`
	MetadataError string          `json:"metadataError,omitempty"`
}

func (r Run) Clone() Run {
	r.Definition = r.Definition.Clone()
	if r.EndedAt != nil {
		t := *r.EndedAt
		r.EndedAt = &t
	}
	if r.Outcome != nil {
		o := *r.Outcome
		if o.ExitCode != nil {
			code := *o.ExitCode
			o.ExitCode = &code
		}
		r.Outcome = &o
	}
	return r
}

// Label changes presentation, never the recorded lifecycle or actual outcome.
func (r Run) Label() string {
	if r.Lifecycle != Exited || r.Outcome == nil {
		return string(r.Lifecycle)
	}
	if r.Outcome.Kind == LaunchFailed {
		return "launch failed"
	}
	if r.StopRequested {
		return "stopped"
	}
	if r.Definition.Kind == Service {
		return "exited"
	}
	if r.Outcome.Kind == Success {
		return "completed"
	}
	return "failed"
}
