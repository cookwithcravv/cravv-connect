package relayserver

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/coder/websocket"

	"github.com/cravv/cravv-connect/internal/relayproto"
)

const (
	handshakeTimeout = 10 * time.Second
	pushBatch        = 64
)

// errHandled means the connection was already answered and closed.
var errHandled = errors.New("relayserver: handled")

// mailboxConn is one authenticated, registered mailbox connection.
type mailboxConn struct {
	s       *Server
	ws      *websocket.Conn
	ik      ed25519.PublicKey
	mailbox string
	wake    chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
}

func (c *mailboxConn) wakeUp() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// kick replaces this connection: error{gone} and close.
func (c *mailboxConn) kick() {
	go closeWithError(context.Background(), c.ws, relayproto.CodeGone, "another connection for this mailbox took over")
}

// readLimit fits a send frame carrying MaxFrame decoded bytes plus JSON overhead.
func (s *Server) readLimit() int64 {
	return int64(s.cfg.Limits.MaxFrame)*4/3 + 16<<10
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	routeIK, ikErr := relayproto.ParseIK(r.URL.Query().Get(relayproto.QueryIK))
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(s.readLimit())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !s.ipLimiter.allow(clientIP(r)) {
		closeWithError(ctx, ws, relayproto.CodeRateLimited, "too many connections from this address")
		return
	}
	if ikErr != nil {
		closeWithError(ctx, ws, relayproto.CodeBadRequest, "missing or invalid ik query parameter")
		return
	}
	ik, err := s.handshake(ctx, ws, routeIK)
	if err != nil {
		return
	}
	c := &mailboxConn{s: s, ws: ws, ik: ik, mailbox: relayproto.MailboxID(ik), wake: make(chan struct{}, 1), ctx: ctx, cancel: cancel}
	if old := s.hub.register(c); old != nil {
		old.kick()
	}
	defer s.hub.unregister(c)
	s.log.Info("mailbox connected", "mailbox", c.mailbox)
	c.wakeUp()
	go c.pushLoop()
	c.readLoop()
	s.log.Info("mailbox disconnected", "mailbox", c.mailbox)
}

// handshake runs hello/welcome/challenge/auth and, for a new key, register.
func (s *Server) handshake(ctx context.Context, ws *websocket.Conn, routeIK ed25519.PublicKey) (ed25519.PublicKey, error) {
	hctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()

	h, raw, err := readFrame(hctx, ws)
	if err != nil {
		return nil, err
	}
	var hello relayproto.Hello
	if h.T != relayproto.TypeHello || json.Unmarshal(raw, &hello) != nil {
		closeWithError(ctx, ws, relayproto.CodeBadRequest, "expected hello")
		return nil, errHandled
	}
	if !slices.Contains(hello.Versions, relayproto.Version) {
		closeWithError(ctx, ws, relayproto.CodeUnsupportedVersion, "this relay speaks relay-v1 only")
		return nil, errHandled
	}
	if err := writeJSON(ctx, ws, relayproto.Welcome{T: relayproto.TypeWelcome, Version: relayproto.Version}); err != nil {
		return nil, err
	}
	nonce := relayproto.B64(randomNonce())
	if err := writeJSON(ctx, ws, relayproto.Challenge{T: relayproto.TypeChallenge, Nonce: nonce}); err != nil {
		return nil, err
	}

	h, raw, err = readFrame(hctx, ws)
	if err != nil {
		return nil, err
	}
	var a relayproto.Auth
	if h.T != relayproto.TypeAuth || json.Unmarshal(raw, &a) != nil {
		closeWithError(ctx, ws, relayproto.CodeBadRequest, "expected auth")
		return nil, errHandled
	}
	ik, err := relayproto.ParseIK(a.IK)
	if err != nil {
		closeWithError(ctx, ws, relayproto.CodeAuthFailed, "invalid ik")
		return nil, errHandled
	}
	if !ik.Equal(routeIK) {
		closeWithError(ctx, ws, relayproto.CodeAuthFailed, "ik does not match the ik query parameter")
		return nil, errHandled
	}
	sig, err := relayproto.UnB64(a.Sig)
	if err != nil || !ed25519.Verify(ik, relayproto.AuthMessage(s.cfg.PublicOrigin, nonce), sig) {
		closeWithError(ctx, ws, relayproto.CodeAuthFailed, "bad signature")
		return nil, errHandled
	}
	mailbox := relayproto.MailboxID(ik)
	registered, err := s.be.IsMember(ctx, mailbox)
	if err != nil {
		closeWithError(ctx, ws, relayproto.CodeInternal, "registry unavailable")
		return nil, errHandled
	}
	if err := writeJSON(ctx, ws, relayproto.AuthOK{T: relayproto.TypeAuthOK, Registered: registered, MailboxID: mailbox}); err != nil {
		return nil, err
	}
	if registered {
		return ik, nil
	}
	return ik, s.register(ctx, hctx, ws, mailbox)
}

// register handles the one frame an unregistered key may send.
func (s *Server) register(ctx, hctx context.Context, ws *websocket.Conn, mailbox string) error {
	h, raw, err := readFrame(hctx, ws)
	if err != nil {
		return err
	}
	var reg relayproto.Register
	if h.T != relayproto.TypeRegister || json.Unmarshal(raw, &reg) != nil {
		closeWithError(ctx, ws, relayproto.CodeNotRegistered, "register first")
		return errHandled
	}
	ok := secretEqual(reg.AdminToken, s.cfg.AdminToken)
	if ok {
		err = s.be.AddMember(ctx, mailbox)
	} else if reg.Invite != "" {
		ok, err = s.be.RegisterWithInvite(ctx, reg.Invite, mailbox)
	}
	if err != nil {
		closeWithError(ctx, ws, relayproto.CodeInternal, "registry unavailable")
		return errHandled
	}
	if !ok {
		_ = reply(ctx, ws, relayproto.Res{RID: reg.RID, Status: relayproto.StatusError, Code: relayproto.CodeForbidden})
		_ = ws.Close(websocket.StatusPolicyViolation, relayproto.CodeForbidden)
		return errHandled
	}
	s.log.Info("mailbox registered", "mailbox", mailbox, "via_invite", reg.Invite != "" && reg.AdminToken == "")
	return reply(ctx, ws, relayproto.Res{RID: reg.RID, Status: relayproto.StatusOK})
}

// pushLoop sends the unacked backlog, then new frames as they arrive.
func (c *mailboxConn) pushLoop() {
	var last uint64
	lim := c.s.cfg.Limits
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.wake:
		}
		for {
			frames, err := c.s.be.Pending(c.ctx, c.mailbox, last, lim.QueueTTL, pushBatch)
			if err != nil {
				c.s.log.Error("pending", "mailbox", c.mailbox, "err", err)
				c.cancel()
				_ = c.ws.Close(websocket.StatusInternalError, relayproto.CodeInternal)
				return
			}
			for _, f := range frames {
				d := relayproto.Deliver{T: relayproto.TypeDeliver, Seq: f.Seq, From: relayproto.B64(f.From), ID: f.ID, Frame: relayproto.B64(f.Frame)}
				if err := writeJSON(c.ctx, c.ws, d); err != nil {
					return
				}
				last = f.Seq
			}
			if len(frames) < pushBatch {
				break
			}
		}
	}
}

// readLoop dispatches client requests until the connection ends.
func (c *mailboxConn) readLoop() {
	for {
		h, raw, err := readFrame(c.ctx, c.ws)
		if err != nil {
			if c.ctx.Err() == nil && websocket.CloseStatus(err) == -1 && !errors.Is(err, context.Canceled) {
				closeWithError(c.ctx, c.ws, relayproto.CodeBadRequest, "malformed frame")
			}
			return
		}
		op, ok := c.s.ops[h.T]
		if !ok {
			if h.RID == "" {
				closeWithError(c.ctx, c.ws, relayproto.CodeBadRequest, "unknown frame type")
				return
			}
			if reply(c.ctx, c.ws, relayproto.Res{RID: h.RID, Status: relayproto.StatusError, Code: relayproto.CodeBadRequest}) != nil {
				return
			}
			continue
		}
		if h.T != relayproto.TypeAck {
			if h.RID == "" {
				closeWithError(c.ctx, c.ws, relayproto.CodeBadRequest, "missing rid")
				return
			}
			if !c.s.limiter.allow(c.mailbox) {
				if reply(c.ctx, c.ws, relayproto.Res{RID: h.RID, Status: relayproto.StatusRateLimited}) != nil {
					return
				}
				continue
			}
		}
		res, err := op(c, raw)
		if errors.Is(err, errBadFrame) {
			closeWithError(c.ctx, c.ws, relayproto.CodeBadRequest, "malformed "+h.T)
			return
		}
		if err != nil {
			c.s.log.Error("mailbox op", "op", h.T, "mailbox", c.mailbox, "err", err)
			res = relayproto.Res{Status: relayproto.StatusError, Code: relayproto.CodeInternal}
		}
		if h.T == relayproto.TypeAck {
			continue
		}
		res.RID = h.RID
		if reply(c.ctx, c.ws, res) != nil {
			return
		}
	}
}
