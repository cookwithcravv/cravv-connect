package relayclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
	"github.com/cookwithcravv/cravv-connect/internal/transport"
)

// ErrClosed is Mailbox.Err after Close.
var ErrClosed = errors.New("relay: connection closed")

// ErrProtocol reports a frame the relay-v1 contract does not allow at that point.
var ErrProtocol = errors.New("relay: protocol violation")

// ServerError is an error{code,message} frame or a res{status:"error",code}.
// Code "forbidden" matches transport.ErrRelayForbidden and code "internal"
// matches transport.ErrRelayInternal with errors.Is.
type ServerError struct {
	Code    string
	Message string
}

func (e *ServerError) Error() string {
	if e.Message == "" {
		return "relay: " + e.Code
	}
	return "relay: " + e.Code + ": " + e.Message
}

func (e *ServerError) Unwrap() error {
	switch e.Code {
	case relayproto.CodeForbidden:
		return transport.ErrRelayForbidden
	case relayproto.CodeInternal:
		return transport.ErrRelayInternal
	}
	return nil
}

// HTTPError is a non-success blob response. It matches transport.ErrRelayForbidden (403),
// core.ErrNotFound (404, 410), and core.ErrTooLarge (413) with errors.Is.
type HTTPError struct {
	Status  int
	Code    string // from the JSON error body, "" when absent
	Message string
}

func newHTTPError(status int, body []byte) *HTTPError {
	var b relayproto.HTTPErrorBody
	_ = json.Unmarshal(body, &b)
	return &HTTPError{Status: status, Code: b.Code, Message: b.Message}
}

func (e *HTTPError) Error() string {
	msg := fmt.Sprintf("relay: http %d %s", e.Status, http.StatusText(e.Status))
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

func (e *HTTPError) Unwrap() error {
	switch e.Status {
	case http.StatusForbidden:
		return transport.ErrRelayForbidden
	case http.StatusNotFound, http.StatusGone:
		return core.ErrNotFound
	case http.StatusRequestEntityTooLarge:
		return core.ErrTooLarge
	}
	return nil
}
