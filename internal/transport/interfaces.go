// Package transport declares the seam between the daemon and whatever carries
// its frames. The daemon depends only on these interfaces; relayclient
// implements them over relay-v1.
package transport

import (
	"context"
	"crypto/ed25519"
	"errors"

	"github.com/cookwithcravv/cravv-connect/internal/core"
)

// SendStatus is the relay's verdict on one send. Values equal relayproto Status* strings.
type SendStatus string

const (
	SendQueued         SendStatus = "queued"
	SendNotAllowed     SendStatus = "not_allowed"
	SendQueueFull      SendStatus = "queue_full"
	SendTooLarge       SendStatus = "too_large"
	SendUnknownMailbox SendStatus = "unknown_mailbox"
	SendRateLimited    SendStatus = "rate_limited"
)

// Delivery is one frame pushed by the relay.
type Delivery struct {
	Seq   uint64
	From  ed25519.PublicKey
	ID    string
	Frame []byte
}

// Credentials are used only when the relay says the mailbox is not registered yet.
type Credentials struct{ AdminToken, Invite string }

// FrameConn is the frame-carrying half of a mailbox connection: what a component
// that only moves peer frames needs.
type FrameConn interface {
	Send(ctx context.Context, to core.MachineID, id string, frame []byte) (SendStatus, error)
	// Deliveries yields frames pushed by the relay, in the order received. Reading it
	// slowly never stalls request replies: the connection buffers undrained frames in
	// memory. It is closed when the connection ends; frames not yet received are then
	// dropped (they were not acked, so the relay pushes them again next connection).
	Deliveries() <-chan Delivery
	Ack(ctx context.Context, seq uint64) error
	Done() <-chan struct{}
	Err() error // why Done closed
	Close() error
}

// RelayControl is the relay-management half of a mailbox connection.
type RelayControl interface {
	Allow(ctx context.Context, ik ed25519.PublicKey) error
	Deny(ctx context.Context, ik ed25519.PublicKey) error
	RequestInvite(ctx context.Context) (string, error)
	CreateRoom(ctx context.Context) (nameplate, creatorToken string, err error)
}

// Mailbox is one authenticated, live connection to the caller's relay mailbox.
type Mailbox interface {
	FrameConn
	RelayControl
}

// Dialer opens a Mailbox. It does not reconnect; the caller owns reconnection.
type Dialer interface {
	Dial(ctx context.Context, id Signer, creds Credentials) (Mailbox, error)
}

// Signer is satisfied by *keys.Identity.
type Signer interface {
	Public() ed25519.PublicKey
	Sign(msg []byte) []byte
}

// Room is one side of a pairing room.
type Room interface {
	WaitPeer(ctx context.Context) error // creator: returns when peer_joined; joiner: returns immediately
	Send(ctx context.Context, data []byte) error
	Recv(ctx context.Context) ([]byte, error) // io.EOF when peer closed
	Close() error
}

// Rooms opens pairing rooms.
type Rooms interface {
	Open(ctx context.Context, nameplate, creatorToken string) (Room, error) // creatorToken "" => joiner
}

// BlobStore moves encrypted file chunks through the relay.
type BlobStore interface {
	Create(ctx context.Context, recipient ed25519.PublicKey, size int64, chunks uint32) (string, error)
	PutChunk(ctx context.Context, blobID string, n uint32, data []byte) error
	GetChunk(ctx context.Context, blobID string, n uint32) ([]byte, error)
	Delete(ctx context.Context, blobID string) error
}

var (
	ErrRelayForbidden = errors.New("relay: forbidden")
	ErrRoomGone       = errors.New("relay: pairing room gone or already used")
)
