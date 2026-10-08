package model

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

type LogRead struct {
	RunID       string `json:"runId"`
	Data        []byte `json:"data"` // JSON base64 retains invalid UTF-8 safely.
	Next        uint64 `json:"next"`
	Truncated   bool   `json:"truncated"`
	Unavailable bool   `json:"unavailable,omitempty"`
}
