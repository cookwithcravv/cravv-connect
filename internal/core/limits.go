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
	NewSessionBacklog  = 24 * time.Hour
	LockoutFailures    = 5
	LockoutDuration    = 15 * time.Minute
	UnlockTTL          = 10 * time.Minute
	MaxWait            = 50 * time.Second
	DefaultPeerQuota   = 1 << 30
	BackoffMin         = time.Second
	BackoffMax         = 5 * time.Minute
)
