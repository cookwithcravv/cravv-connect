package relayserver

import (
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
)

// Limits bounds what one relay accepts. Zero fields are replaced by DefaultLimits values.
type Limits struct {
	QueueBytes  int64
	QueueFrames int
	QueueTTL    time.Duration
	RoomTTL     time.Duration
	InviteTTL   time.Duration
	BlobTTL     time.Duration
	MaxFrame    int
	MaxBlob     int64
	MaxChunk    int
	// BlobQuota caps the total declared size of one uploader's unexpired blobs.
	BlobQuota int64
	// TotalBlobBytes caps the total declared size of all unexpired blobs on the relay.
	TotalBlobBytes int64
	// MaxInvitesPerMember caps one member's unused, unexpired invites.
	MaxInvitesPerMember int
	// MaxRoomsPerMember caps one member's live pairing rooms.
	MaxRoomsPerMember int
	// OpRate is the sustained number of mailbox requests per second allowed per mailbox,
	// and OpBurst the bucket size. OpRate < 0 disables the per-mailbox limit.
	OpRate  float64
	OpBurst int
	// IPRate and IPBurst limit new WebSocket connections and blob requests per client IP.
	// IPRate < 0 disables the per-IP limit.
	IPRate  float64
	IPBurst int
}

// DefaultLimits returns the relay-v1 limits from internal/core.
func DefaultLimits() Limits {
	return Limits{
		QueueBytes:          core.MailboxQueueBytes,
		QueueFrames:         core.MailboxQueueFrames,
		QueueTTL:            core.RelayTTL,
		RoomTTL:             core.RoomTTL,
		InviteTTL:           core.InviteTTL,
		BlobTTL:             core.BlobTTL,
		MaxFrame:            core.MaxFrameBytes,
		MaxBlob:             core.MaxFileBytes,
		MaxChunk:            core.FileChunkBytes + relayproto.ChunkOverhead,
		BlobQuota:           2 << 30,
		TotalBlobBytes:      50 << 30,
		MaxInvitesPerMember: 20,
		MaxRoomsPerMember:   8,
		OpRate:              200,
		OpBurst:             1000,
		IPRate:              50,
		IPBurst:             500,
	}
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.QueueBytes == 0 {
		l.QueueBytes = d.QueueBytes
	}
	if l.QueueFrames == 0 {
		l.QueueFrames = d.QueueFrames
	}
	if l.QueueTTL == 0 {
		l.QueueTTL = d.QueueTTL
	}
	if l.RoomTTL == 0 {
		l.RoomTTL = d.RoomTTL
	}
	if l.InviteTTL == 0 {
		l.InviteTTL = d.InviteTTL
	}
	if l.BlobTTL == 0 {
		l.BlobTTL = d.BlobTTL
	}
	if l.MaxFrame == 0 {
		l.MaxFrame = d.MaxFrame
	}
	if l.MaxBlob == 0 {
		l.MaxBlob = d.MaxBlob
	}
	if l.MaxChunk == 0 {
		l.MaxChunk = d.MaxChunk
	}
	if l.BlobQuota == 0 {
		l.BlobQuota = d.BlobQuota
	}
	if l.TotalBlobBytes == 0 {
		l.TotalBlobBytes = d.TotalBlobBytes
	}
	if l.MaxInvitesPerMember == 0 {
		l.MaxInvitesPerMember = d.MaxInvitesPerMember
	}
	if l.MaxRoomsPerMember == 0 {
		l.MaxRoomsPerMember = d.MaxRoomsPerMember
	}
	if l.OpRate == 0 {
		l.OpRate = d.OpRate
	}
	if l.OpBurst == 0 {
		l.OpBurst = d.OpBurst
	}
	if l.IPRate == 0 {
		l.IPRate = d.IPRate
	}
	if l.IPBurst == 0 {
		l.IPBurst = d.IPBurst
	}
	return l
}
