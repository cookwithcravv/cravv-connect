// Package relayproto holds the relay-v1 wire types and signing strings shared by
// the relay client, the reference relay server, and the conformance suite.
// protocol/relay-v1.md is the normative description of everything here.
package relayproto

// Version is the only relay protocol version this package speaks.
const Version = 1

// Frame type values (the "t" field).
const (
	// Mailbox, server to client.
	TypeWelcome   = "welcome"
	TypeChallenge = "challenge"
	TypeAuthOK    = "auth_ok"
	TypeRes       = "res"
	TypeDeliver   = "deliver"
	TypeError     = "error"

	// Mailbox, client to server.
	TypeHello         = "hello"
	TypeAuth          = "auth"
	TypeRegister      = "register"
	TypeAllow         = "allow"
	TypeDeny          = "deny"
	TypeInviteRequest = "invite_request"
	TypeRoomCreate    = "room_create"
	TypeSend          = "send"
	TypeAck           = "ack"

	// Pairing room, both directions.
	TypeWaiting    = "waiting"
	TypePeerJoined = "peer_joined"
	TypeMsg        = "msg"
	TypeClosed     = "closed"
)

// Head is decoded first from every incoming frame to route it by type.
type Head struct {
	T   string `json:"t"`
	RID string `json:"rid,omitempty"`
}

// Server -> client.

type Welcome struct {
	T       string `json:"t"`
	Version int    `json:"version"`
}

type Challenge struct {
	T     string `json:"t"`
	Nonce string `json:"nonce"`
}

type AuthOK struct {
	T          string `json:"t"`
	Registered bool   `json:"registered"`
	MailboxID  string `json:"mailbox_id"`
}

type Res struct {
	T            string `json:"t"`
	RID          string `json:"rid"`
	Status       string `json:"status"`
	Code         string `json:"code,omitempty"`
	Invite       string `json:"invite,omitempty"`
	Nameplate    string `json:"nameplate,omitempty"`
	CreatorToken string `json:"creator_token,omitempty"`
}

type Deliver struct {
	T     string `json:"t"`
	Seq   uint64 `json:"seq"`
	From  string `json:"from"` // sender IK, B64
	ID    string `json:"id"`
	Frame string `json:"frame"` // B64
}

// Error is sent right before the server closes the connection.
type Error struct {
	T       string `json:"t"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Client -> server.

type Hello struct {
	T        string `json:"t"`
	Versions []int  `json:"versions"`
}

type Auth struct {
	T   string `json:"t"`
	IK  string `json:"ik"`
	Sig string `json:"sig"`
}

type Register struct {
	T          string `json:"t"`
	RID        string `json:"rid"`
	AdminToken string `json:"admin_token,omitempty"`
	Invite     string `json:"invite,omitempty"`
}

type Allow struct {
	T   string `json:"t"`
	RID string `json:"rid"`
	IK  string `json:"ik"`
}

type Deny struct {
	T   string `json:"t"`
	RID string `json:"rid"`
	IK  string `json:"ik"`
}

type InviteRequest struct {
	T   string `json:"t"`
	RID string `json:"rid"`
}

type RoomCreate struct {
	T   string `json:"t"`
	RID string `json:"rid"`
}

type Send struct {
	T     string `json:"t"`
	RID   string `json:"rid"`
	To    string `json:"to"` // recipient mailbox ID
	ID    string `json:"id"`
	Frame string `json:"frame"` // B64
}

// Ack acknowledges every frame with seq <= Seq. The server does not reply.
type Ack struct {
	T   string `json:"t"`
	Seq uint64 `json:"seq"`
}

// Pairing room frames.

// RoomSignal carries the data-less room frames: waiting, peer_joined, closed.
type RoomSignal struct {
	T string `json:"t"`
}

// RoomMsg is an opaque message relayed verbatim to the other side of a room.
type RoomMsg struct {
	T    string `json:"t"`
	Data string `json:"data"` // B64
}

// MaxIDLen bounds Send.ID.
const MaxIDLen = 128
