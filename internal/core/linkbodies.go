package core

// ProtocolVersion is the peer protocol version this build speaks. A peer
// that sends link-less chat, task.* or file.offer (v1) is told the minimum
// version with control.unsupported.
const ProtocolVersion = 2

// SessionRef names a session to a peer. The project folder is never sent.
type SessionRef struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Purpose string `json:"purpose,omitempty"`
}

// SessionsListBody is sessions.list: a request for the visible sessions.
type SessionsListBody struct {
	ReqID string `json:"req_id"`
}

// ListedSession is one visible session in sessions.listed.
type ListedSession struct {
	SessionID string       `json:"session_id"`
	Name      string       `json:"name"`
	Purpose   string       `json:"purpose,omitempty"`
	Kind      SessionKind  `json:"kind"`
	Agent     string       `json:"agent"`
	State     SessionState `json:"state"`
}

// ListedOffer is one managed-session offer in sessions.listed (labels only).
type ListedOffer struct {
	OfferID       string     `json:"offer_id"`
	Label         string     `json:"label"`
	Agent         string     `json:"agent"`
	MaxPermission Permission `json:"max_permission"`
}

// SessionsListedBody answers sessions.list with the entries visible to the asker.
type SessionsListedBody struct {
	ReqID    string          `json:"req_id"`
	Sessions []ListedSession `json:"sessions"`
	Offers   []ListedOffer   `json:"offers"`
}

// LinkRequestBody is link.request. Exactly one of ToSessionID and OfferID is set.
type LinkRequestBody struct {
	LinkID             string     `json:"link_id"`
	FromSession        SessionRef `json:"from_session"`
	ToSessionID        string     `json:"to_session_id,omitempty"`
	OfferID            string     `json:"offer_id,omitempty"`
	ProposedPermission Permission `json:"proposed_permission"`
	Note               string     `json:"note,omitempty"`
}

// LinkAcceptedBody is link.accepted: GrantedPermission is the acceptor's
// permission_in, what the requester may do on the acceptor's session.
type LinkAcceptedBody struct {
	LinkID            string     `json:"link_id"`
	ToSession         SessionRef `json:"to_session"`
	GrantedPermission Permission `json:"granted_permission"`
}

// LinkRejectedBody is link.rejected.
type LinkRejectedBody struct {
	LinkID string `json:"link_id"`
	Reason string `json:"reason"`
}

// LinkClosedBody is link.closed.
type LinkClosedBody struct {
	LinkID string `json:"link_id"`
	Reason string `json:"reason"`
}

// LinkStateBody is link.state (informational; enforcement is local).
type LinkStateBody struct {
	LinkID       string     `json:"link_id"`
	State        string     `json:"state"`
	PermissionIn Permission `json:"permission_in"`
}

// PresencePingBody is presence.ping: TS is unix milliseconds.
type PresencePingBody struct {
	TS      int64    `json:"ts"`
	LinkIDs []string `json:"link_ids"`
}

// PresencePongBody is presence.pong: TS echoes the ping's TS.
type PresencePongBody struct {
	TS          int64    `json:"ts"`
	LinkIDsOpen []string `json:"link_ids_open"`
}

// UnsupportedBody is control.unsupported.
type UnsupportedBody struct {
	MinVersion int `json:"min_version"`
}

// link.rejected reasons.
const (
	RejectDeclined = "declined"
	RejectNotFound = "not_found"
	RejectBusy     = "busy"
	RejectPolicy   = "policy"
	RejectTimeout  = "timeout"
)

// link.closed reasons.
const (
	CloseClosedByPeer    = "closed_by_peer"
	CloseSessionClosed   = "session_closed"
	ClosePaused          = "paused"
	CloseUnpaired        = "unpaired"
	CloseKilled          = "killed"
	ClosePresenceTimeout = "presence_timeout"
	CloseUnknownLink     = "unknown_link"
)

// link.state values.
const (
	LinkStateActive = "active"
	LinkStateAway   = "away"
)

var (
	rejectReasons = map[string]bool{RejectDeclined: true, RejectNotFound: true, RejectBusy: true, RejectPolicy: true, RejectTimeout: true}
	closeReasons  = map[string]bool{
		CloseClosedByPeer: true, CloseSessionClosed: true, ClosePaused: true, CloseUnpaired: true,
		CloseKilled: true, ClosePresenceTimeout: true, CloseUnknownLink: true,
	}
)

// ValidRejectReason reports whether r is a defined link.rejected reason.
func ValidRejectReason(r string) bool { return rejectReasons[r] }

// ValidCloseReason reports whether r is a defined link.closed reason.
func ValidCloseReason(r string) bool { return closeReasons[r] }
