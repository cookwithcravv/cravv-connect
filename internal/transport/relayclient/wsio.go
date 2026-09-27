package relayclient

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/coder/websocket"

	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
)

const (
	writeTimeout     = 10 * time.Second
	handshakeTimeout = 15 * time.Second
	// readLimit fits a deliver frame carrying MaxFrameBytes plus JSON overhead.
	readLimit = 1 << 20
)

func readFrame(ctx context.Context, ws *websocket.Conn) (relayproto.Head, []byte, error) {
	_, data, err := ws.Read(ctx)
	if err != nil {
		return relayproto.Head{}, nil, err
	}
	var h relayproto.Head
	if err := json.Unmarshal(data, &h); err != nil {
		return relayproto.Head{}, nil, fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	return h, data, nil
}

// expect reads one frame of type want; an error frame becomes *ServerError.
func expect(ctx context.Context, ws *websocket.Conn, want string, v any) error {
	h, raw, err := readFrame(ctx, ws)
	if err != nil {
		return err
	}
	if h.T == relayproto.TypeError {
		return serverError(raw)
	}
	if h.T != want {
		return fmt.Errorf("%w: got %q, want %q", ErrProtocol, h.T, want)
	}
	return json.Unmarshal(raw, v)
}

func serverError(raw []byte) error {
	var e relayproto.Error
	if err := json.Unmarshal(raw, &e); err != nil {
		return fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	return &ServerError{Code: e.Code, Message: e.Message}
}

func writeJSON(ctx context.Context, ws *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return ws.Write(wctx, websocket.MessageText, b)
}
