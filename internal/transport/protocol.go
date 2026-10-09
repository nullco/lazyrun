// Package transport provides bounded, versioned JSON IPC over private Unix sockets.
package transport

import (
	"encoding/json"
	"fmt"
	"github.com/nullco/lazyrun/internal/model"
)

const ProtocolVersion = 2

type Operation string

const (
	Handshake  Operation = "handshake"
	SyncConfig Operation = "sync_config"
	ListState  Operation = "list_state"
	Start      Operation = "start"
	Stop       Operation = "stop"
	Restart    Operation = "restart"
	ReadLogs   Operation = "read_logs"
)

// Payloads remain operation-specific; keep environment-bearing control messages
// separate from redacted run state. Never send requests to diagnostic logging.
type Request struct {
	Version   int             `json:"version"`
	ID        string          `json:"id"`
	Operation Operation       `json:"operation"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

type Response struct {
	Version int             `json:"version"`
	ID      string          `json:"id"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

const (
	CodeIncompatible   = "incompatible_protocol"
	CodeInvalid        = "invalid_request"
	CodeUnmanaged      = "unmanaged_run"
	CodeAlreadyRunning = "already_running"
	CodeRunChanged     = "run_changed"
	CodeRemoved        = "removed_from_config"
	CodeRestartPending = "restart_pending"
	CodeRestartBlocked = "restart_blocked"
	CodeUnknownAlias   = "unknown_alias"
	CodeRuntime        = "runtime_error"
)

type Hello struct {
	ProjectID     string                `json:"projectId"`
	BinaryVersion string                `json:"binaryVersion"`
	Supervisor    model.ProcessIdentity `json:"supervisor"`
}

// Configuration control messages carry raw validated YAML, not redacted state.
type SyncPayload struct {
	Config []byte `json:"config"`
}

type HandshakePayload struct {
	ProjectID string `json:"projectId"`
}

type StartPayload struct {
	Alias       string   `json:"alias"`
	Environment [][]byte `json:"environment"` // Base64 preserves arbitrary Unix environment bytes.
}

type AliasPayload struct {
	Alias string `json:"alias"`
}

type ReadLogsPayload struct {
	Alias string `json:"alias"`
	RunID string `json:"runId"`
	After uint64 `json:"after"`
	Limit int    `json:"limit,omitempty"`
	Tail  *int   `json:"tail,omitempty"` // nil: cursor read; zero: byte-bounded tail
}
