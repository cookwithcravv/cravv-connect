package relayclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"

	"github.com/coder/websocket"

	"github.com/cravv/cravv-connect/internal/relayproto"
	"github.com/cravv/cravv-connect/internal/transport"
)

type rooms struct{ c *Client }

// Open connects to a pairing room. With a creator token it is the creator side and
// returns after "waiting"; without one it is the joiner and returns after "peer_joined".
// Cancelling a ctx passed to WaitPeer or Recv closes the room.
func (r rooms) Open(ctx context.Context, nameplate, creatorToken string) (transport.Room, error) {
	u := r.c.wsBase + relayproto.PathPair + url.PathEscape(nameplate)
	if creatorToken != "" {
		u += "?" + relayproto.QueryToken + "=" + url.QueryEscape(creatorToken)
	}
	ws, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPClient: r.c.http})
	if err != nil {
		return nil, fmt.Errorf("relay: dial room: %w", err)
	}
	ws.SetReadLimit(readLimit)
	rm := &room{ws: ws}
	h, raw, err := readFrame(ctx, ws)
	if err != nil {
		ws.CloseNow()
		return nil, err
	}
	switch {
	case h.T == relayproto.TypeError:
		ws.CloseNow()
		return nil, roomError(serverError(raw))
	case h.T == relayproto.TypePeerJoined:
		rm.joined = true
	case h.T == relayproto.TypeWaiting && creatorToken != "":
	default:
		ws.CloseNow()
		return nil, fmt.Errorf("%w: room opened with %q", ErrProtocol, h.T)
	}
	return rm, nil
}

// roomError maps gone and not_found to transport.ErrRoomGone.
func roomError(err error) error {
	var se *ServerError
	if errors.As(err, &se) && (se.Code == relayproto.CodeGone || se.Code == relayproto.CodeNotFound) {
		return fmt.Errorf("%w (%s)", transport.ErrRoomGone, se.Code)
	}
	return err
}

type room struct {
	ws     *websocket.Conn
	joined bool // touched only by the goroutine calling WaitPeer/Recv
}

func (r *room) WaitPeer(ctx context.Context) error {
	for !r.joined {
		h, raw, err := readFrame(ctx, r.ws)
		if err != nil {
			return err
		}
		switch h.T {
		case relayproto.TypePeerJoined:
			r.joined = true
		case relayproto.TypeWaiting:
		case relayproto.TypeError:
			return roomError(serverError(raw))
		case relayproto.TypeClosed:
			return transport.ErrRoomGone
		default:
			return fmt.Errorf("%w: %q before peer_joined", ErrProtocol, h.T)
		}
	}
	return nil
}

func (r *room) Send(ctx context.Context, data []byte) error {
	return writeJSON(ctx, r.ws, relayproto.RoomMsg{T: relayproto.TypeMsg, Data: relayproto.B64(data)})
}

func (r *room) Recv(ctx context.Context) ([]byte, error) {
	for {
		h, raw, err := readFrame(ctx, r.ws)
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				return nil, io.EOF
			}
			return nil, err
		}
		switch h.T {
		case relayproto.TypeMsg:
			var m relayproto.RoomMsg
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrProtocol, err)
			}
			return relayproto.UnB64(m.Data)
		case relayproto.TypePeerJoined:
			r.joined = true
		case relayproto.TypeWaiting:
		case relayproto.TypeClosed:
			return nil, io.EOF
		case relayproto.TypeError:
			return nil, roomError(serverError(raw))
		}
	}
}

func (r *room) Close() error {
	return r.ws.Close(websocket.StatusNormalClosure, "")
}
