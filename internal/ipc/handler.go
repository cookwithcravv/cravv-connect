package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// Handler serves one method. The returned value is marshaled as the result;
// nil marshals as {}.
type Handler func(ctx context.Context, cs *ConnState, params json.RawMessage) (any, error)

// Gate is a bitmask of the checks the server runs before a handler.
type Gate uint8

const (
	// GateNone runs no check except the kill switch.
	GateNone Gate = 0
	// GateSession requires session.register on this connection.
	GateSession Gate = 1 << 0
	// GateUnlock requires a successful auth.unlock within UnlockTTL.
	GateUnlock Gate = 1 << 1
	// GateAllowWhenKilled lets the method run while the kill switch is on.
	GateAllowWhenKilled Gate = 1 << 2
)

// Typed adapts a function taking decoded params into a Handler. Empty or null
// params decode as the zero value; malformed params return ErrBadRequest.
func Typed[P any](fn func(ctx context.Context, cs *ConnState, p P) (any, error)) Handler {
	return func(ctx context.Context, cs *ConnState, raw json.RawMessage) (any, error) {
		var p P
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
			if err := json.Unmarshal(trimmed, &p); err != nil {
				return nil, fmt.Errorf("%w: invalid params: %v", ErrBadRequest, err)
			}
		}
		return fn(ctx, cs, p)
	}
}
