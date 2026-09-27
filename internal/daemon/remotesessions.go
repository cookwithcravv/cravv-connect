package daemon

import "github.com/cookwithcravv/cravv-connect/internal/core"

// Peers choose their session names, purposes and agent labels. They are
// shown to agents and humans, so each is checked on receipt: an entry with
// a bad ID or name is dropped, a bad purpose or agent label is blanked.

// cleanSessionRef validates a session reference a peer sent.
func cleanSessionRef(r core.SessionRef) (core.SessionRef, bool) {
	if !core.ValidID(r.ID) || !core.ValidSessionName(r.Name) {
		return core.SessionRef{}, false
	}
	if !core.ValidPurpose(r.Purpose) {
		r.Purpose = ""
	}
	return r, true
}

// cleanListedSession validates one sessions.listed entry.
func cleanListedSession(s core.ListedSession) (core.ListedSession, bool) {
	ref, ok := cleanSessionRef(core.SessionRef{ID: s.SessionID, Name: s.Name, Purpose: s.Purpose})
	if !ok {
		return core.ListedSession{}, false
	}
	switch s.Kind {
	case core.SessionLive, core.SessionManaged:
	default:
		return core.ListedSession{}, false
	}
	switch s.State {
	case core.SessionOpen, core.SessionAway:
	default:
		return core.ListedSession{}, false
	}
	if !validAgentLabel(s.Agent) {
		s.Agent = ""
	}
	s.SessionID, s.Name, s.Purpose = ref.ID, ref.Name, ref.Purpose
	return s, true
}

// validAgentLabel accepts 1 to 32 characters of [a-z0-9-].
func validAgentLabel(a string) bool {
	if a == "" || len(a) > 32 {
		return false
	}
	for i := 0; i < len(a); i++ {
		c := a[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
