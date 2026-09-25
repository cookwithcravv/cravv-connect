package relayproto

// Values of Res.Status.
const (
	StatusOK             = "ok"
	StatusQueued         = "queued"
	StatusNotAllowed     = "not_allowed"
	StatusQueueFull      = "queue_full"
	StatusTooLarge       = "too_large"
	StatusUnknownMailbox = "unknown_mailbox"
	StatusRateLimited    = "rate_limited"
	StatusError          = "error" // Res.Code says why
)
