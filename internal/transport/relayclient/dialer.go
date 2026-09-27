package relayclient

import (
	"context"
	"fmt"
	"net/url"

	"github.com/coder/websocket"

	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
	"github.com/cookwithcravv/cravv-connect/internal/transport"
)

type dialer struct{ c *Client }

// Dial connects to /v1/connect, authenticates, registers with creds when the relay
// reports the key as unregistered, and returns a live Mailbox. It never retries.
func (d dialer) Dial(ctx context.Context, id transport.Signer, creds transport.Credentials) (transport.Mailbox, error) {
	u := d.c.wsBase + relayproto.PathConnect + "?" + relayproto.QueryIK + "=" + url.QueryEscape(relayproto.B64(id.Public()))
	ws, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPClient: d.c.http})
	if err != nil {
		return nil, fmt.Errorf("relay: dial: %w", err)
	}
	ws.SetReadLimit(readLimit)
	hctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	if err := d.handshake(hctx, ws, id, creds); err != nil {
		ws.CloseNow()
		return nil, err
	}
	return newMailbox(ws, d.c.pingInterval, d.c.pingTimeout), nil
}

func (d dialer) handshake(ctx context.Context, ws *websocket.Conn, id transport.Signer, creds transport.Credentials) error {
	if err := writeJSON(ctx, ws, relayproto.Hello{T: relayproto.TypeHello, Versions: []int{relayproto.Version}}); err != nil {
		return err
	}
	var wel relayproto.Welcome
	if err := expect(ctx, ws, relayproto.TypeWelcome, &wel); err != nil {
		return err
	}
	if wel.Version != relayproto.Version {
		return fmt.Errorf("%w: relay chose version %d", ErrProtocol, wel.Version)
	}
	var ch relayproto.Challenge
	if err := expect(ctx, ws, relayproto.TypeChallenge, &ch); err != nil {
		return err
	}
	sig := id.Sign(relayproto.AuthMessage(d.c.origin, ch.Nonce))
	if err := writeJSON(ctx, ws, relayproto.Auth{T: relayproto.TypeAuth, IK: relayproto.B64(id.Public()), Sig: relayproto.B64(sig)}); err != nil {
		return err
	}
	var ok relayproto.AuthOK
	if err := expect(ctx, ws, relayproto.TypeAuthOK, &ok); err != nil {
		return err
	}
	if ok.Registered {
		return nil
	}
	reg := relayproto.Register{T: relayproto.TypeRegister, RID: "register", AdminToken: creds.AdminToken, Invite: creds.Invite}
	if err := writeJSON(ctx, ws, reg); err != nil {
		return err
	}
	var res relayproto.Res
	if err := expect(ctx, ws, relayproto.TypeRes, &res); err != nil {
		return err
	}
	if res.Status != relayproto.StatusOK {
		return &ServerError{Code: res.Code, Message: "register refused"}
	}
	return nil
}
