package relayserver

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/coder/websocket"

	"github.com/cravv/cravv-connect/internal/relayproto"
)

const writeTimeout = 10 * time.Second

var errNotText = errors.New("relayserver: binary frame")

// readFrame reads one text frame and decodes its head.
func readFrame(ctx context.Context, ws *websocket.Conn) (relayproto.Head, []byte, error) {
	typ, data, err := ws.Read(ctx)
	if err != nil {
		return relayproto.Head{}, nil, err
	}
	if typ != websocket.MessageText {
		return relayproto.Head{}, nil, errNotText
	}
	var h relayproto.Head
	if err := json.Unmarshal(data, &h); err != nil {
		return relayproto.Head{}, nil, err
	}
	return h, data, nil
}

// writeJSON writes v as one text frame with a bounded wait.
func writeJSON(ctx context.Context, ws *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return ws.Write(wctx, websocket.MessageText, b)
}

// closeWithError sends error{code,message} and closes the connection.
func closeWithError(ctx context.Context, ws *websocket.Conn, code, msg string) {
	_ = writeJSON(ctx, ws, relayproto.Error{T: relayproto.TypeError, Code: code, Message: msg})
	_ = ws.Close(websocket.StatusPolicyViolation, code)
}

// reply sends a res frame.
func reply(ctx context.Context, ws *websocket.Conn, res relayproto.Res) error {
	res.T = relayproto.TypeRes
	return writeJSON(ctx, ws, res)
}
