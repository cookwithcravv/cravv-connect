package relayproto

// Values of Error.Code and Res.Code.
const (
	CodeBadRequest         = "bad_request"
	CodeUnsupportedVersion = "unsupported_version"
	CodeAuthFailed         = "auth_failed"
	CodeNotRegistered      = "not_registered"
	CodeForbidden          = "forbidden"
	CodeNotFound           = "not_found"
	CodeGone               = "gone"
	CodeRateLimited        = "rate_limited"
	CodeTooLarge           = "too_large"
	CodeInternal           = "internal"
)
