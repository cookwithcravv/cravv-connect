package core

import "unicode/utf8"

// SessionState is a shared session's lifecycle state.
type SessionState string

const (
	SessionOpen   SessionState = "open"
	SessionAway   SessionState = "away"
	SessionClosed SessionState = "closed"
)

// SessionKind says who runs a shared session.
type SessionKind string

const (
	SessionLive    SessionKind = "live"    // a human's chat
	SessionManaged SessionKind = "managed" // started by the daemon (later phase)
)

// ValidSessionName reports whether s is a session name: 1 to MaxSessionName
// characters of [a-z0-9-], not starting with '-'.
func ValidSessionName(s string) bool {
	if s == "" || len(s) > MaxSessionName || s[0] == '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// ValidPurpose reports whether s fits a session purpose: one line of at
// most MaxPurposeRunes characters.
func ValidPurpose(s string) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > MaxPurposeRunes {
		return false
	}
	for _, r := range s {
		if r == '\n' || r == '\r' {
			return false
		}
	}
	return true
}

// ValidNote reports whether s fits a link request note (MaxLinkNoteRunes).
func ValidNote(s string) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= MaxLinkNoteRunes
}
