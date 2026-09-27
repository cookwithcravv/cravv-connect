package relayserver

import (
	"encoding/json"
	"errors"

	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
)

// errBadFrame makes the connection end with error{bad_request}.
var errBadFrame = errors.New("relayserver: malformed frame")

// opHandler handles one client request type on a live mailbox connection.
// A returned error is an internal failure; protocol outcomes go in the Res.
type opHandler func(c *mailboxConn, raw []byte) (relayproto.Res, error)

var badRequest = relayproto.Res{Status: relayproto.StatusError, Code: relayproto.CodeBadRequest}

// mailboxOps is the request registry. New operations are added here only.
func (s *Server) mailboxOps() map[string]opHandler {
	return map[string]opHandler{
		relayproto.TypeAllow:         s.opAllow,
		relayproto.TypeDeny:          s.opDeny,
		relayproto.TypeInviteRequest: s.opInvite,
		relayproto.TypeRoomCreate:    s.opRoomCreate,
		relayproto.TypeSend:          s.opSend,
		relayproto.TypeAck:           s.opAck,
		relayproto.TypeRegister:      s.opRegister,
	}
}

func (s *Server) opAllow(c *mailboxConn, raw []byte) (relayproto.Res, error) {
	var m relayproto.Allow
	if json.Unmarshal(raw, &m) != nil {
		return badRequest, nil
	}
	ik, err := relayproto.ParseIK(m.IK)
	if err != nil {
		return badRequest, nil
	}
	return relayproto.Res{Status: relayproto.StatusOK}, s.be.Allow(c.ctx, c.mailbox, relayproto.MailboxID(ik))
}

func (s *Server) opDeny(c *mailboxConn, raw []byte) (relayproto.Res, error) {
	var m relayproto.Deny
	if json.Unmarshal(raw, &m) != nil {
		return badRequest, nil
	}
	ik, err := relayproto.ParseIK(m.IK)
	if err != nil {
		return badRequest, nil
	}
	return relayproto.Res{Status: relayproto.StatusOK}, s.be.Deny(c.ctx, c.mailbox, relayproto.MailboxID(ik))
}

var capReached = relayproto.Res{Status: relayproto.StatusError, Code: relayproto.CodeRateLimited}

func (s *Server) opInvite(c *mailboxConn, _ []byte) (relayproto.Res, error) {
	lim := s.cfg.Limits
	for range 4 {
		tok := randomToken()
		err := s.be.PutInvite(c.ctx, tok, c.mailbox, lim.InviteTTL, lim.MaxInvitesPerMember)
		switch {
		case errors.Is(err, ErrLimit):
			return capReached, nil
		case errors.Is(err, ErrExists):
			continue
		case err != nil:
			return relayproto.Res{}, err
		}
		return relayproto.Res{Status: relayproto.StatusOK, Invite: tok}, nil
	}
	return relayproto.Res{}, errors.New("no free invite token")
}

func (s *Server) opRoomCreate(c *mailboxConn, _ []byte) (relayproto.Res, error) {
	for range 16 {
		rec := RoomRecord{
			Nameplate:    randomNameplate(),
			CreatorToken: randomHexToken(),
			Owner:        c.mailbox,
			ExpiresAt:    s.cfg.Clock.Now().Add(s.cfg.Limits.RoomTTL),
		}
		err := s.be.CreateRoom(c.ctx, rec, s.cfg.Limits.MaxRoomsPerMember)
		if errors.Is(err, ErrLimit) {
			return capReached, nil
		}
		if errors.Is(err, ErrExists) {
			continue
		}
		if err != nil {
			return relayproto.Res{}, err
		}
		return relayproto.Res{Status: relayproto.StatusOK, Nameplate: rec.Nameplate, CreatorToken: rec.CreatorToken}, nil
	}
	return relayproto.Res{}, errors.New("no free nameplate")
}

func (s *Server) opSend(c *mailboxConn, raw []byte) (relayproto.Res, error) {
	var m relayproto.Send
	if json.Unmarshal(raw, &m) != nil || m.To == "" || m.ID == "" || len(m.ID) > relayproto.MaxIDLen {
		return badRequest, nil
	}
	frame, err := relayproto.UnB64(m.Frame)
	if err != nil {
		return badRequest, nil
	}
	lim := s.cfg.Limits
	if len(frame) > lim.MaxFrame {
		return relayproto.Res{Status: relayproto.StatusTooLarge}, nil
	}
	member, err := s.be.IsMember(c.ctx, m.To)
	if err != nil {
		return relayproto.Res{}, err
	}
	if !member {
		return relayproto.Res{Status: relayproto.StatusUnknownMailbox}, nil
	}
	allowed, err := s.be.IsAllowed(c.ctx, m.To, c.mailbox)
	if err != nil {
		return relayproto.Res{}, err
	}
	if !allowed {
		return relayproto.Res{Status: relayproto.StatusNotAllowed}, nil
	}
	_, err = s.be.Enqueue(c.ctx, m.To, c.ik, m.ID, frame, QueueLimits{MaxBytes: lim.QueueBytes, MaxFrames: lim.QueueFrames, TTL: lim.QueueTTL})
	if errors.Is(err, ErrQueueFull) {
		return relayproto.Res{Status: relayproto.StatusQueueFull}, nil
	}
	if err != nil {
		return relayproto.Res{}, err
	}
	s.hub.notify(m.To)
	return relayproto.Res{Status: relayproto.StatusQueued}, nil
}

// opRegister on an already registered mailbox is a harmless no-op.
func (s *Server) opRegister(*mailboxConn, []byte) (relayproto.Res, error) {
	return relayproto.Res{Status: relayproto.StatusOK}, nil
}

func (s *Server) opAck(c *mailboxConn, raw []byte) (relayproto.Res, error) {
	var m relayproto.Ack
	if json.Unmarshal(raw, &m) != nil {
		return relayproto.Res{}, errBadFrame
	}
	return relayproto.Res{}, s.be.Ack(c.ctx, c.mailbox, m.Seq)
}
