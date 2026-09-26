package core

import "time"

// Limits from the spec's Global Constraints. Exact values; do not tune here.
const (
	MaxTextBytes       = 64 << 10
	MaxFileBytes       = 100 << 20
	FileChunkBytes     = 1 << 20
	MaxFrameBytes      = 256 << 10
	MailboxQueueBytes  = 52428800
	MailboxQueueFrames = 10000
	RelayTTL           = 7 * 24 * time.Hour
	RoomTTL            = 10 * time.Minute
	InviteTTL          = 10 * time.Minute
	BlobTTL            = 7 * 24 * time.Hour
	PrekeyRotation     = 7 * 24 * time.Hour
	PrekeyRetention    = 21 * 24 * time.Hour
	OutboxRetention    = 21 * 24 * time.Hour
	DedupWindow        = 30 * 24 * time.Hour
	InboxRetention     = 30 * 24 * time.Hour
	MaxMessageAge      = 21 * 24 * time.Hour
	MaxClockSkew       = 10 * time.Minute
	ApprovalExpiry     = 24 * time.Hour
	UnclaimedExpiry    = 24 * time.Hour
	ReclaimGrace       = 5 * time.Minute
	LockoutFailures    = 5
	LockoutDuration    = 15 * time.Minute
	UnlockTTL          = 10 * time.Minute
	MaxWait            = 50 * time.Second  // default wait_for_message timeout
	MaxWaitLong        = 600 * time.Second // longest wait_for_message a client may ask for
	DefaultPeerQuota   = 1 << 30
	BackoffMin         = time.Second
	BackoffMax         = 5 * time.Minute

	// Sessions and links (v2 spec sections 3, 4, 5).
	MaxSessionName         = 32
	MaxPurposeRunes        = 120
	MaxLinkNoteRunes       = 280
	AwayGrace              = 10 * time.Minute
	LinkRequestExpiry      = 10 * time.Minute
	MaxPendingLinkRequests = 5  // per peer, enforced by the receiver
	LinkRequestsPerMinute  = 10 // link.request per peer per minute, enforced by the receiver
	DiscoveryPerMinute     = 30 // sessions.list per peer per minute, enforced by the receiver
	PresenceInterval       = 30 * time.Second
	PresenceTimeout        = 150 * time.Second
	PresenceMaxAge         = 120 * time.Second
	UnknownLinkReplyEvery  = time.Minute // link.closed{unknown_link}: at most one per link
	UnsupportedReplyEvery  = time.Hour   // control.unsupported: at most one per peer
)
