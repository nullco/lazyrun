// Package transport defines the initial protocol vocabulary. Socket framing,
// request validation, compatibility checks, and client/server I/O belong to M3.
package transport

import "encoding/json"

const ProtocolVersion = 1

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

type HandshakePayload struct {
	ProjectID string `json:"projectId"`
}

type StartPayload struct {
	Alias       string   `json:"alias"`
	Environment []string `json:"environment"`
}

type AliasPayload struct {
	Alias string `json:"alias"`
}

type ReadLogsPayload struct {
	Alias string `json:"alias"`
	RunID string `json:"runId"`
	After uint64 `json:"after"`
}
