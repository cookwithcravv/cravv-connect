package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// ReasonDisconnected is the local close reason when this side disconnected
// (the peer is told closed_by_peer).
const ReasonDisconnected = "disconnected"

// LinkSessions is what LinkService needs from shared sessions. Implemented by *SessionService.
type LinkSessions interface {
	Get(ctx context.Context, id string) (store.SharedSession, error)
	VisibleTo(ctx context.Context, id string, peer core.MachineID) (store.SharedSession, error)
}

// Directory lists a peer's visible sessions. Implemented by *Discovery.
type Directory interface {
	List(ctx context.Context, machine string) (store.Peer, core.SessionsListedBody, error)
}

// SessionInbox stores an item for exactly one shared session. Implemented by *InboxService.
type SessionInbox interface {
	Deliver(ctx context.Context, it store.InboxItem) (int64, error)
}

// UnknownLinkReplier answers traffic on a link this side does not have open.
// Implemented by *LinkReplies.
type UnknownLinkReplier interface {
	UnknownLink(ctx context.Context, peer store.Peer, linkID string)
}

// LinkCloseObserver is told after an active link closed: tasks on it fail,
// held files are declined, an away session's queued items are dropped.
type LinkCloseObserver interface {
	LinkClosed(ctx context.Context, l store.Link) error
}

// LinkLowerObserver is told after a link's permission_in was lowered.
type LinkLowerObserver interface {
	LinkLowered(ctx context.Context, l store.Link) error
}

// LinkNotice is the inbox body of link events shown to a session.
type LinkNotice struct {
	Link       int64           `json:"link"`
	Event      string          `json:"event"` // request | accepted | rejected | closed
	Permission core.Permission `json:"permission,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Note       string          `json:"note,omitempty"`
}

// LinkDeps are the LinkService collaborators.
type LinkDeps struct {
	Links     store.LinkStore
	Sessions  LinkSessions
	Peers     store.PeerStore
	Directory Directory
	Sender    EnvelopeSender
	Replies   UnknownLinkReplier
	Inbox     SessionInbox
	Desktop   DesktopNotifier
	Clock     core.Clock
	Audit     audit.Logger
	Log       *slog.Logger
}

// LinkService owns session-to-session links (v2 spec 3.4 and 4): requests,
// the tiered accept gate, permission changes, close and its effects.
type LinkService struct {
	d        LinkDeps
	requests *RateLimiter // inbound link.request per peer

	mu     sync.Mutex
	closes []LinkCloseObserver
	lowers []LinkLowerObserver
}

// NewLinkService builds a LinkService.
func NewLinkService(d LinkDeps) *LinkService {
	if d.Audit == nil {
		d.Audit = audit.Nop{}
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return &LinkService{d: d, requests: NewRateLimiter(d.Clock, core.LinkRequestsPerMinute, time.Minute)}
}

// AddCloseObserver registers o for closed active links.
func (s *LinkService) AddCloseObserver(o LinkCloseObserver) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closes = append(s.closes, o)
}

// AddLowerObserver registers o for lowered permissions.
func (s *LinkService) AddLowerObserver(o LinkLowerObserver) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lowers = append(s.lowers, o)
}

// Connect asks target ("machine/session") to link with the local session,
// proposing what this side may do there. The link is pending until the peer
// answers; this side lets the peer send messages only.
func (s *LinkService) Connect(ctx context.Context, sessionID, target string, proposed core.Permission, note string) (store.Link, error) {
	if !proposed.Valid() {
		return store.Link{}, fmt.Errorf("permission %q: %w", proposed, ErrBadPermission)
	}
	if !core.ValidNote(note) {
		return store.Link{}, ErrBadNote
	}
	sess, err := s.d.Sessions.Get(ctx, sessionID)
	if err != nil {
		return store.Link{}, err
	}
	if sess.State != core.SessionOpen {
		return store.Link{}, core.ErrNotShared
	}
	machine, name, ok := strings.Cut(strings.TrimSpace(target), "/")
	if !ok || machine == "" || name == "" {
		return store.Link{}, ErrBadTarget
	}
	if strings.HasPrefix(name, "new:") {
		return store.Link{}, fmt.Errorf("%s: this machine offers no managed sessions yet: %w", target, core.ErrNotFound)
	}
	peer, listed, err := s.d.Directory.List(ctx, machine)
	if err != nil {
		return store.Link{}, err
	}
	var remote *core.ListedSession
	for i := range listed.Sessions {
		if listed.Sessions[i].Name == name {
			remote = &listed.Sessions[i]
			break
		}
	}
	if remote == nil {
		return store.Link{}, fmt.Errorf("session %s on %s: %w", name, peer.Alias, core.ErrNotFound)
	}
	now := s.d.Clock.Now()
	l, err := s.d.Links.InsertLink(ctx, store.Link{
		Peer: peer.MachineID, ID: core.NewIDAt(s.d.Clock), Direction: store.LinkOutbound, Session: sess.ID,
		RemoteSession: remote.SessionID, RemoteName: remote.Name, RemotePurpose: remote.Purpose,
		PermissionIn: core.PermMessages, Proposed: proposed, State: store.LinkPending,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(core.LinkRequestExpiry),
	})
	if err != nil {
		return store.Link{}, err
	}
	body := core.LinkRequestBody{
		LinkID: l.ID, FromSession: core.SessionRef{ID: sess.ID, Name: sess.Name, Purpose: sess.Purpose},
		ToSessionID: remote.SessionID, ProposedPermission: proposed, Note: note,
	}
	if _, err := s.d.Sender.SendEnvelope(ctx, peer.MachineID, core.KindLinkRequest, "", body); err != nil {
		_, _ = s.d.Links.UpdateLink(ctx, l.Peer, l.ID, func(x *store.Link) error {
			x.State, x.Reason, x.ExpiresAt, x.UpdatedAt = store.LinkClosed, "not sent: "+err.Error(), time.Time{}, now
			return nil
		})
		return store.Link{}, err
	}
	s.record(audit.EvLinkRequest, peer, l, map[string]any{"direction": "out", "proposed": string(proposed)})
	return l, nil
}

// HandleRequest records a link.request as pending for a human decision, or
// rejects it: busy past core.LinkRequestsPerMinute new requests a minute or
// core.MaxPendingLinkRequests pending requests from the peer, not_found for a session that is missing, closed or not visible
// to the peer (these look identical), timeout for a request older than
// core.LinkRequestExpiry, policy for a malformed one.
func (s *LinkService) HandleRequest(ctx context.Context, peer store.Peer, env core.Envelope) error {
	b, err := decodeEnvBody[core.LinkRequestBody](env.Body)
	if err != nil {
		return err
	}
	if !core.ValidID(b.LinkID) {
		return fmt.Errorf("link.request: link_id: %w", errBadPeerID)
	}
	if _, err := s.d.Links.GetLink(ctx, peer.MachineID, b.LinkID); err == nil {
		return nil // a duplicate: already pending or decided
	} else if !errors.Is(err, core.ErrNotFound) {
		return Retryable(err)
	}
	if !s.requests.Allow(string(peer.MachineID)) {
		return s.reject(ctx, peer, b.LinkID, core.RejectBusy)
	}
	from, ok := cleanSessionRef(b.FromSession)
	if !ok || !b.ProposedPermission.Valid() || !core.ValidNote(b.Note) {
		return s.reject(ctx, peer, b.LinkID, core.RejectPolicy)
	}
	if b.OfferID != "" || !core.ValidID(b.ToSessionID) {
		return s.reject(ctx, peer, b.LinkID, core.RejectNotFound)
	}
	now := s.d.Clock.Now()
	if now.Sub(time.UnixMilli(env.TS)) > core.LinkRequestExpiry {
		return s.reject(ctx, peer, b.LinkID, core.RejectTimeout)
	}
	pending, err := s.d.Links.ListLinks(ctx, store.LinkFilter{Peer: peer.MachineID, Direction: store.LinkInbound, States: []store.LinkState{store.LinkPending}})
	if err != nil {
		return Retryable(err)
	}
	if len(pending) >= core.MaxPendingLinkRequests {
		return s.reject(ctx, peer, b.LinkID, core.RejectBusy)
	}
	sess, err := s.d.Sessions.VisibleTo(ctx, b.ToSessionID, peer.MachineID)
	if err != nil {
		return s.reject(ctx, peer, b.LinkID, core.RejectNotFound)
	}
	l, err := s.d.Links.InsertLink(ctx, store.Link{
		Peer: peer.MachineID, ID: b.LinkID, Direction: store.LinkInbound, Session: sess.ID,
		RemoteSession: from.ID, RemoteName: from.Name, RemotePurpose: from.Purpose,
		Proposed: b.ProposedPermission, Note: b.Note, State: store.LinkPending,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(core.LinkRequestExpiry),
	})
	if errors.Is(err, store.ErrLinkExists) {
		return nil
	}
	if err != nil {
		return Retryable(err)
	}
	s.record(audit.EvLinkRequest, peer, l, map[string]any{"direction": "in", "proposed": string(l.Proposed)})
	s.tell(ctx, l, env.ID, core.KindLinkRequest, LinkNotice{Event: "request", Permission: l.Proposed, Note: l.Note})
	if s.d.Desktop != nil {
		s.d.Desktop.Notify("cravv-connect", fmt.Sprintf("cravv-connect: link request %d from %s", l.Num, peer.Alias))
	}
	return nil
}

// reject answers a request that is not stored.
func (s *LinkService) reject(ctx context.Context, peer store.Peer, linkID, reason string) error {
	_, err := s.d.Sender.SendEnvelope(ctx, peer.MachineID, core.KindLinkRejected, "", core.LinkRejectedBody{LinkID: linkID, Reason: reason})
	return err
}

// Decide accepts or rejects a pending request (link number num). Rejecting
// needs no authority. Accepting grants perm (at most the proposed level; ""
// means the proposed level) and needs AuthChat, or AuthPassword for
// tasks-auto (v2 spec section 10).
func (s *LinkService) Decide(ctx context.Context, num int64, accept bool, perm core.Permission, auth Authority) (store.Link, error) {
	l, err := s.d.Links.GetLinkByNum(ctx, num)
	if err != nil {
		return l, err
	}
	if l.Direction != store.LinkInbound || l.State != store.LinkPending {
		return l, fmt.Errorf("link %d is not waiting for a decision: %w", num, core.ErrBadTransition)
	}
	if !accept {
		return s.closeLink(ctx, l, closeSpec{local: core.RejectDeclined, reject: core.RejectDeclined})
	}
	if perm == "" {
		perm = l.Proposed
	}
	if !perm.Valid() || l.Proposed.Below(perm) {
		return l, fmt.Errorf("grant %q (asked for %s): %w", perm, l.Proposed, ErrBadPermission)
	}
	if auth < grantAuthority(perm) {
		return l, fmt.Errorf("accepting at %s: %w", perm, core.ErrAuthRequired)
	}
	now := s.d.Clock.Now()
	if !l.ExpiresAt.IsZero() && !now.Before(l.ExpiresAt) {
		_, _ = s.closeLink(ctx, l, closeSpec{local: core.RejectTimeout, reject: core.RejectTimeout})
		return l, fmt.Errorf("link %d request expired: %w", num, core.ErrBadTransition)
	}
	sess, err := s.d.Sessions.Get(ctx, l.Session)
	if err != nil || sess.State == core.SessionClosed {
		_, _ = s.closeLink(ctx, l, closeSpec{local: core.CloseSessionClosed, reject: core.RejectNotFound})
		return l, fmt.Errorf("link %d: the session closed: %w", num, core.ErrNotFound)
	}
	l, err = s.d.Links.UpdateLink(ctx, l.Peer, l.ID, func(x *store.Link) error {
		if x.State != store.LinkPending {
			return fmt.Errorf("link %d is %s: %w", num, x.State, core.ErrBadTransition)
		}
		x.State, x.PermissionIn, x.ExpiresAt, x.UpdatedAt = store.LinkActive, perm, time.Time{}, now
		// The requester lets this side send messages only until it raises that.
		x.PermissionOut = core.PermMessages
		return nil
	})
	if err != nil {
		return l, err
	}
	body := core.LinkAcceptedBody{
		LinkID: l.ID, ToSession: core.SessionRef{ID: sess.ID, Name: sess.Name, Purpose: sess.Purpose}, GrantedPermission: perm,
	}
	if _, err := s.d.Sender.SendEnvelope(ctx, l.Peer, core.KindLinkAccepted, "", body); err != nil {
		return l, err
	}
	s.recordLink(ctx, audit.EvLinkAccept, l, map[string]any{"permission": string(perm), "authority": auth.String()})
	return l, nil
}

// DecideVia asks the human through d (Phase 2: elicitation or a
// confirmation code) and applies the answer with AuthChat.
func (s *LinkService) DecideVia(ctx context.Context, d Decider, num int64) (store.Link, error) {
	l, err := s.d.Links.GetLinkByNum(ctx, num)
	if err != nil {
		return l, err
	}
	if l.Direction != store.LinkInbound || l.State != store.LinkPending {
		return l, fmt.Errorf("link %d is not waiting for a decision: %w", num, core.ErrBadTransition)
	}
	ans, err := d.Decide(ctx, DecisionRequest{Link: l, Alias: s.alias(ctx, l.Peer)})
	if err != nil {
		return l, err
	}
	return s.Decide(ctx, num, ans.Accept, ans.Permission, AuthChat)
}

// HandleAccepted activates a pending outgoing link. An answer for a link
// that is not pending here gets link.closed{unknown_link}.
func (s *LinkService) HandleAccepted(ctx context.Context, peer store.Peer, env core.Envelope) error {
	b, err := decodeEnvBody[core.LinkAcceptedBody](env.Body)
	if err != nil {
		return err
	}
	l, err := s.d.Links.GetLink(ctx, peer.MachineID, b.LinkID)
	if errors.Is(err, core.ErrNotFound) {
		s.d.Replies.UnknownLink(ctx, peer, b.LinkID)
		return nil
	}
	if err != nil {
		return Retryable(err)
	}
	if l.Direction != store.LinkOutbound {
		return nil
	}
	if l.State != store.LinkPending {
		if l.State == store.LinkClosed {
			s.d.Replies.UnknownLink(ctx, peer, b.LinkID)
		}
		return nil
	}
	to, ok := cleanSessionRef(b.ToSession)
	if !ok || to.ID != l.RemoteSession || !b.GrantedPermission.Valid() {
		_, err := s.closeLink(ctx, l, closeSpec{local: core.CloseUnknownLink, wire: core.CloseUnknownLink, tell: true})
		return err
	}
	now := s.d.Clock.Now()
	l, err = s.d.Links.UpdateLink(ctx, l.Peer, l.ID, func(x *store.Link) error {
		if x.State != store.LinkPending {
			return core.ErrBadTransition
		}
		x.State, x.PermissionOut, x.ExpiresAt, x.UpdatedAt = store.LinkActive, b.GrantedPermission, time.Time{}, now
		x.RemoteName, x.RemotePurpose = to.Name, to.Purpose
		return nil
	})
	if errors.Is(err, core.ErrBadTransition) {
		return nil
	}
	if err != nil {
		return Retryable(err)
	}
	s.recordLink(ctx, audit.EvLinkAccept, l, map[string]any{"permission_out": string(b.GrantedPermission)})
	s.tell(ctx, l, env.ID, core.KindLinkAccepted, LinkNotice{Event: "accepted", Permission: b.GrantedPermission})
	return nil
}

// HandleRejected closes a pending outgoing link the peer declined.
func (s *LinkService) HandleRejected(ctx context.Context, peer store.Peer, env core.Envelope) error {
	b, err := decodeEnvBody[core.LinkRejectedBody](env.Body)
	if err != nil {
		return err
	}
	l, err := s.d.Links.GetLink(ctx, peer.MachineID, b.LinkID)
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	if err != nil {
		return Retryable(err)
	}
	if l.Direction != store.LinkOutbound || l.State != store.LinkPending {
		return nil
	}
	reason := b.Reason
	if !core.ValidRejectReason(reason) {
		reason = core.RejectDeclined
	}
	_, err = s.closeLink(ctx, l, closeSpec{local: reason, tell: true, event: "rejected", msgID: env.ID})
	return err
}

// HandleClosed closes the local side of a link the peer closed. It never
// answers: an unknown link.closed is dropped, so two sides that both
// consider a link unknown cannot ping-pong.
func (s *LinkService) HandleClosed(ctx context.Context, peer store.Peer, env core.Envelope) error {
	b, err := decodeEnvBody[core.LinkClosedBody](env.Body)
	if err != nil {
		return err
	}
	l, err := s.d.Links.GetLink(ctx, peer.MachineID, b.LinkID)
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	if err != nil {
		return Retryable(err)
	}
	if !l.Open() {
		return nil
	}
	reason := b.Reason
	if !core.ValidCloseReason(reason) {
		reason = core.CloseClosedByPeer
	}
	_, err = s.closeLink(ctx, l, closeSpec{local: reason, tell: true, msgID: env.ID})
	return err
}

// HandleState records the peer's side of an active link: away or active,
// and what it now lets this side do. A link.state for a link still pending
// here (it overtook the link.accepted) is dropped silently; one for a link
// that is unknown or closed gets link.closed{unknown_link}.
func (s *LinkService) HandleState(ctx context.Context, peer store.Peer, env core.Envelope) error {
	b, err := decodeEnvBody[core.LinkStateBody](env.Body)
	if err != nil {
		return err
	}
	l, err := s.d.Links.GetLink(ctx, peer.MachineID, b.LinkID)
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		return Retryable(err)
	}
	if err == nil && l.State == store.LinkPending {
		return nil
	}
	if err != nil || l.State != store.LinkActive {
		s.d.Replies.UnknownLink(ctx, peer, b.LinkID)
		return nil
	}
	_, err = s.d.Links.UpdateLink(ctx, l.Peer, l.ID, func(x *store.Link) error {
		x.RemoteAway = b.State == core.LinkStateAway
		if b.PermissionIn.Valid() {
			x.PermissionOut = b.PermissionIn
		}
		x.UpdatedAt = s.d.Clock.Now()
		return nil
	})
	return Retryable(err)
}

// Disconnect closes a link. A non-empty sessionID limits it to that
// session's links (agents); "" allows any link (the human's CLI).
func (s *LinkService) Disconnect(ctx context.Context, sessionID string, num int64) error {
	l, err := s.owned(ctx, sessionID, num)
	if err != nil {
		return err
	}
	if !l.Open() {
		return nil
	}
	spec := closeSpec{local: ReasonDisconnected, wire: core.CloseClosedByPeer}
	if l.State == store.LinkPending && l.Direction == store.LinkInbound {
		spec.wire, spec.reject = "", core.RejectDeclined
	}
	_, err = s.closeLink(ctx, l, spec)
	return err
}

// SetPermission changes what the peer may do on an active link. Lowering
// needs no authority; raising needs AuthPassword. A non-empty sessionID
// limits it to that session's links.
func (s *LinkService) SetPermission(ctx context.Context, sessionID string, num int64, perm core.Permission, auth Authority) (store.Link, error) {
	if !perm.Valid() {
		return store.Link{}, fmt.Errorf("permission %q: %w", perm, ErrBadPermission)
	}
	l, err := s.owned(ctx, sessionID, num)
	if err != nil {
		return l, err
	}
	if l.State != store.LinkActive {
		return l, fmt.Errorf("link %d: %w", num, core.ErrLinkClosed)
	}
	if perm == l.PermissionIn {
		return l, nil
	}
	lowered := perm.Below(l.PermissionIn)
	if !lowered && auth < AuthPassword {
		return l, fmt.Errorf("raising link %d to %s: %w", num, perm, core.ErrAuthRequired)
	}
	old := l.PermissionIn
	l, err = s.d.Links.UpdateLink(ctx, l.Peer, l.ID, func(x *store.Link) error {
		if x.State != store.LinkActive {
			return core.ErrLinkClosed
		}
		x.PermissionIn, x.UpdatedAt = perm, s.d.Clock.Now()
		return nil
	})
	if err != nil {
		return l, err
	}
	s.recordLink(ctx, audit.EvLinkPermission, l, map[string]any{"from": string(old), "to": string(perm), "authority": auth.String()})
	s.sendState(ctx, l, "")
	if lowered {
		s.mu.Lock()
		obs := append([]LinkLowerObserver(nil), s.lowers...)
		s.mu.Unlock()
		var errs []error
		for _, o := range obs {
			errs = append(errs, o.LinkLowered(ctx, l))
		}
		return l, errors.Join(errs...)
	}
	return l, nil
}

// owned returns link num, which must belong to sessionID unless it is "".
// Another session's link looks missing.
func (s *LinkService) owned(ctx context.Context, sessionID string, num int64) (store.Link, error) {
	l, err := s.d.Links.GetLinkByNum(ctx, num)
	if err != nil || (sessionID != "" && l.Session != sessionID) {
		return store.Link{}, fmt.Errorf("link %d: %w", num, core.ErrNotFound)
	}
	return l, nil
}

// Active returns the session's link num when it is active: the only state
// in which chat, tasks and files may travel (v2 spec 5: a send on any other
// link fails with core.ErrLinkClosed at once).
func (s *LinkService) Active(ctx context.Context, sessionID string, num int64) (store.Link, error) {
	l, err := s.owned(ctx, sessionID, num)
	if err != nil {
		return l, err
	}
	if l.State != store.LinkActive {
		return l, fmt.Errorf("link %d: %w", num, core.ErrLinkClosed)
	}
	return l, nil
}

// Get returns link num.
func (s *LinkService) Get(ctx context.Context, num int64) (store.Link, error) {
	return s.d.Links.GetLinkByNum(ctx, num)
}

// List returns a session's links, or every link when sessionID is "".
func (s *LinkService) List(ctx context.Context, sessionID string) ([]store.Link, error) {
	return s.d.Links.ListLinks(ctx, store.LinkFilter{Session: sessionID})
}

// SessionAway implements SessionObserver: the session's peers learn it is away.
func (s *LinkService) SessionAway(ctx context.Context, sess store.SharedSession) {
	s.sendStates(ctx, sess.ID, core.LinkStateAway)
}

// SessionBack implements SessionObserver: the session's peers learn it is back.
func (s *LinkService) SessionBack(ctx context.Context, sess store.SharedSession) {
	s.sendStates(ctx, sess.ID, core.LinkStateActive)
}

// SessionClosed implements SessionObserver: every open link of the session
// closes (session_closed), and pending requests to it are rejected (not_found).
func (s *LinkService) SessionClosed(ctx context.Context, sess store.SharedSession) {
	ls, err := s.d.Links.ListLinks(ctx, store.LinkFilter{Session: sess.ID, States: []store.LinkState{store.LinkPending, store.LinkActive}})
	if err != nil {
		s.d.Log.Warn("list links of a closed session", "err", err)
		return
	}
	for _, l := range ls {
		spec := closeSpec{local: core.CloseSessionClosed, wire: core.CloseSessionClosed}
		if l.State == store.LinkPending && l.Direction == store.LinkInbound {
			spec.wire, spec.reject = "", core.RejectNotFound
		}
		if _, err := s.closeLink(ctx, l, spec); err != nil {
			s.d.Log.Warn("close link of a closed session", "link", l.Num, "err", err)
		}
	}
}

// cutOffReasons maps PeerService cut-off notes to link.closed reasons.
var cutOffReasons = map[string]string{
	CutOffPaused: core.ClosePaused, CutOffPausedByPeer: core.ClosePaused, CutOffUnpaired: core.CloseUnpaired,
}

// PeerCutOff implements PeerCutOffObserver: pausing or unpairing a machine
// (either side) closes every link with it. Nothing is sent: control.paused
// and control.unpaired already tell the peer.
func (s *LinkService) PeerCutOff(ctx context.Context, peer store.Peer, reason string) error {
	local, ok := cutOffReasons[reason]
	if !ok {
		local = core.ClosePaused
	}
	ls, err := s.d.Links.ListLinks(ctx, store.LinkFilter{Peer: peer.MachineID, States: []store.LinkState{store.LinkPending, store.LinkActive}})
	if err != nil {
		return err
	}
	var errs []error
	for _, l := range ls {
		_, err := s.closeLink(ctx, l, closeSpec{local: local, tell: true})
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// ClosePresence implements PresenceCloser: the link closes with
// presence_timeout, the session is told, and link.closed is queued so the
// peer converges when it is reachable again.
func (s *LinkService) ClosePresence(ctx context.Context, l store.Link) error {
	_, err := s.closeLink(ctx, l, closeSpec{local: core.ClosePresenceTimeout, wire: core.ClosePresenceTimeout, tell: true})
	return err
}

// CloseAll closes every open link and tells the peers (kill switch).
func (s *LinkService) CloseAll(ctx context.Context, reason string) error {
	ls, err := s.d.Links.ListLinks(ctx, store.LinkFilter{States: []store.LinkState{store.LinkPending, store.LinkActive}})
	if err != nil {
		return err
	}
	var errs []error
	for _, l := range ls {
		spec := closeSpec{local: reason, wire: reason}
		if l.State == store.LinkPending && l.Direction == store.LinkInbound {
			spec.wire, spec.reject = "", core.RejectDeclined
		}
		_, err := s.closeLink(ctx, l, spec)
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// ExpireDue ends requests pending longer than core.LinkRequestExpiry: an
// incoming one is rejected (timeout), an outgoing one closes here.
func (s *LinkService) ExpireDue(ctx context.Context) (int, error) {
	ls, err := s.d.Links.ListLinks(ctx, store.LinkFilter{States: []store.LinkState{store.LinkPending}, ExpiredBefore: s.d.Clock.Now()})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, l := range ls {
		spec := closeSpec{local: core.RejectTimeout, tell: true, event: "rejected"}
		if l.Direction == store.LinkInbound {
			spec = closeSpec{local: core.RejectTimeout, reject: core.RejectTimeout}
		}
		if _, err := s.closeLink(ctx, l, spec); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// PurgeClosed deletes links closed longer ago than core.InboxRetention.
func (s *LinkService) PurgeClosed(ctx context.Context) (int, error) {
	return s.d.Links.PurgeClosedLinks(ctx, s.d.Clock.Now().Add(-core.InboxRetention))
}

// closeSpec says how a link closes: the local reason, what (if anything)
// the peer is sent, and whether the local session is told.
type closeSpec struct {
	local  string // stored reason
	wire   string // link.closed reason to send ("" sends none)
	reject string // link.rejected reason to send instead (pending incoming requests)
	tell   bool   // deliver a notice to the local session
	event  string // notice event (default "closed")
	msgID  string // the message that caused it (notice ID); "" mints one
}

// closeLink closes l once: a link already closed is left alone. Closing an
// active link runs the close observers.
func (s *LinkService) closeLink(ctx context.Context, l store.Link, spec closeSpec) (store.Link, error) {
	wasActive := false
	now := s.d.Clock.Now()
	closed, err := s.d.Links.UpdateLink(ctx, l.Peer, l.ID, func(x *store.Link) error {
		if x.State == store.LinkClosed {
			return errAlreadyClosed
		}
		wasActive = x.State == store.LinkActive
		x.State, x.Reason, x.ExpiresAt, x.UpdatedAt = store.LinkClosed, spec.local, time.Time{}, now
		return nil
	})
	if errors.Is(err, errAlreadyClosed) {
		return l, nil
	}
	if err != nil {
		return l, err
	}
	var errs []error
	switch {
	case spec.reject != "":
		_, err := s.d.Sender.SendEnvelope(ctx, l.Peer, core.KindLinkRejected, "", core.LinkRejectedBody{LinkID: l.ID, Reason: spec.reject})
		errs = append(errs, err)
	case spec.wire != "":
		_, err := s.d.Sender.SendEnvelope(ctx, l.Peer, core.KindLinkClosed, "", core.LinkClosedBody{LinkID: l.ID, Reason: spec.wire})
		errs = append(errs, err)
	}
	ev := audit.EvLinkClose
	if spec.reject != "" || spec.event == "rejected" {
		ev = audit.EvLinkReject
	}
	s.recordLink(ctx, ev, closed, map[string]any{"reason": spec.local})
	if wasActive {
		s.mu.Lock()
		obs := append([]LinkCloseObserver(nil), s.closes...)
		s.mu.Unlock()
		for _, o := range obs {
			errs = append(errs, o.LinkClosed(ctx, closed))
		}
	}
	if spec.tell {
		event := spec.event
		if event == "" {
			event = "closed"
		}
		kind := core.KindLinkClosed
		if event == "rejected" {
			kind = core.KindLinkRejected
		}
		s.tell(ctx, closed, spec.msgID, kind, LinkNotice{Event: event, Reason: spec.local})
	}
	return closed, errors.Join(errs...)
}

var errAlreadyClosed = errors.New("link already closed")

// sendStates sends link.state for every active link of the session.
func (s *LinkService) sendStates(ctx context.Context, sessionID, state string) {
	ls, err := s.d.Links.ListLinks(ctx, store.LinkFilter{Session: sessionID, States: []store.LinkState{store.LinkActive}})
	if err != nil {
		s.d.Log.Warn("list links for link.state", "err", err)
		return
	}
	for _, l := range ls {
		s.sendState(ctx, l, state)
	}
}

// sendState tells the peer this side's state and permission_in; state ""
// means the local session's current state.
func (s *LinkService) sendState(ctx context.Context, l store.Link, state string) {
	if state == "" {
		state = core.LinkStateActive
		if sess, err := s.d.Sessions.Get(ctx, l.Session); err == nil && sess.State == core.SessionAway {
			state = core.LinkStateAway
		}
	}
	body := core.LinkStateBody{LinkID: l.ID, State: state, PermissionIn: l.PermissionIn}
	if _, err := s.d.Sender.SendEnvelope(ctx, l.Peer, core.KindLinkState, "", body); err != nil {
		s.d.Log.Warn("link.state not queued", "link", l.Num, "err", err)
	}
}

// tell delivers a link notice to the link's local session.
func (s *LinkService) tell(ctx context.Context, l store.Link, msgID string, kind core.Kind, n LinkNotice) {
	n.Link = l.Num
	body, err := json.Marshal(n)
	if err != nil {
		return
	}
	if msgID == "" {
		msgID = core.NewIDAt(s.d.Clock)
	}
	if _, err := s.d.Inbox.Deliver(ctx, store.InboxItem{
		MsgID: msgID, From: l.Peer, FromSession: l.RemoteName, ToSession: l.Session, LinkID: l.ID, Kind: kind, Body: body,
	}); err != nil {
		s.d.Log.Warn("link notice not stored", "link", l.Num, "err", err)
	}
}

func (s *LinkService) alias(ctx context.Context, id core.MachineID) string {
	if p, err := s.d.Peers.GetPeer(ctx, id); err == nil {
		return p.Alias
	}
	return id.Short()
}

func (s *LinkService) recordLink(ctx context.Context, typ string, l store.Link, detail map[string]any) {
	s.record(typ, store.Peer{MachineID: l.Peer, Alias: s.alias(ctx, l.Peer)}, l, detail)
}

func (s *LinkService) record(typ string, peer store.Peer, l store.Link, detail map[string]any) {
	if detail == nil {
		detail = map[string]any{}
	}
	detail["link"] = l.Num
	_ = s.d.Audit.Record(audit.Event{Type: typ, Peer: peer.MachineID, Alias: peer.Alias, ItemID: l.ID, Detail: detail})
}

// Errors for invalid link requests (the API maps them to bad_request).
var (
	ErrBadPermission = errors.New("invalid permission: use messages, tasks-ask or tasks-auto")
	ErrBadNote       = errors.New("invalid note: at most 280 characters")
	ErrBadTarget     = errors.New("invalid target: use machine/session")
)
