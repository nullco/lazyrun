package model

import "time"

// CommandState separates the current configuration from the latest run's
// immutable definition. A moved active alias still belongs to its original pane.
type CommandState struct {
	Definition *Definition `json:"definition,omitempty"`
	Run        Run         `json:"run"`
	Removed    bool        `json:"removedFromConfig"`
}

func (s CommandState) DisplayKind() Kind {
	if s.Run.Lifecycle.Active() || s.Definition == nil {
		return s.Run.Definition.Kind
	}
	return s.Definition.Kind
}

type State struct {
	Project  Project        `json:"project"`
	Commands []CommandState `json:"commands"`
}

// LogRecord timestamps a captured chunk (not a fabricated per-line time).
// Cursor identifies the first raw byte, including when a read splits a chunk.
type LogRecord struct {
	Cursor uint64    `json:"cursor"`
	Time   time.Time `json:"time"`
	Data   []byte    `json:"data"`
}

type LogRead struct {
	RunID       string      `json:"runId"`
	Data        []byte      `json:"data"` // JSON base64 retains invalid UTF-8 safely.
	Next        uint64      `json:"next"` // Resume with after + same run ID; may jump over gaps.
	Truncated   bool        `json:"truncated"`
	Unavailable bool        `json:"unavailable,omitempty"`
	Records     []LogRecord `json:"records,omitempty"`
	Error       string      `json:"error,omitempty"`
}
