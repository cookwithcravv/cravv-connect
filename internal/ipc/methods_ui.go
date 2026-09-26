package ipc

// Methods of the local web UI (v2 phase 5). They are registered by
// api.RegisterUI; the gates are set there.
const (
	// MethodUIStart starts the local web UI if it is not running and returns
	// a URL carrying a new one-time launch token.
	MethodUIStart = "ui.start"
	// MethodSessionsLocal lists this machine's open and away shared sessions
	// for the human (CLI or UI connections only).
	MethodSessionsLocal = "sessions.local"
	// MethodLinkConnectAs asks for a link from a named local session on the
	// human's behalf (CLI or UI connections only; needs the password).
	MethodLinkConnectAs = "link.connect_as"
	// MethodSessionsClose closes this machine's open or away session called
	// name, a chat's or a managed one, and so all its links (CLI or UI
	// connections only; a cut-off: no password, works while killed).
	MethodSessionsClose = "sessions.close"
)

// SessionNameParams names a local session.
type SessionNameParams struct {
	Name string `json:"name"`
}

// UIStartResult is the launch URL. The token in it works once.
type UIStartResult struct {
	URL string `json:"url"`
}

// LocalSessionsResult lists local shared sessions. Views never carry IDs.
type LocalSessionsResult struct {
	Sessions []SharedSessionView `json:"sessions"`
}

// LinkConnectAsParams asks target ("machine/session") for a link from the
// local open session named Session. Permission is what that session proposes
// to do on the other side.
type LinkConnectAsParams struct {
	Session    string `json:"session"`
	Target     string `json:"target"`
	Permission string `json:"permission"`
	Note       string `json:"note,omitempty"`
}
