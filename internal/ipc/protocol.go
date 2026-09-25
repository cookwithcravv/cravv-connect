// Package ipc implements the local JSON-RPC 2.0 API between the daemon and its
// clients (CLI, MCP server, hook). Messages are newline-delimited JSON over a
// unix socket.
package ipc

import "encoding/json"

// Version is the JSON-RPC version string every message carries.
const Version = "2.0"

// MaxLineBytes caps one request or response line (8 MiB).
const MaxLineBytes = 8 << 20

// JSON-RPC error codes used by this protocol.
const (
	CodeParseError     = -32700
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeServerError    = -32000
)

// Request is one JSON-RPC request line.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is one JSON-RPC response line. Exactly one of Result and Error is set.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is the JSON-RPC error object. Data.Kind carries the domain error kind.
type Error struct {
	Code    int        `json:"code"`
	Message string     `json:"message"`
	Data    *ErrorData `json:"data,omitempty"`
}

// ErrorData carries the machine-readable error kind (see errors.go).
type ErrorData struct {
	Kind string `json:"kind"`
}
