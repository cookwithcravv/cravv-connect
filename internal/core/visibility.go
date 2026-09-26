package core

import "slices"

// VisibilityMode says which paired machines can see a shared session.
type VisibilityMode string

const (
	VisibilityPrivate  VisibilityMode = "private"   // nobody (the default)
	VisibilityPeers    VisibilityMode = "peers"     // only the machines listed in Peers
	VisibilityAllPeers VisibilityMode = "all-peers" // every paired machine
)

// Visibility is a session's visibility: private, peers:[machine ids] or all-peers.
type Visibility struct {
	Mode  VisibilityMode `json:"mode"`
	Peers []MachineID    `json:"peers,omitempty"`
}

// Valid reports whether v is well formed: a known mode, and a machine list
// only (and at least one machine) for mode peers.
func (v Visibility) Valid() bool {
	switch v.Mode {
	case VisibilityPrivate, VisibilityAllPeers:
		return len(v.Peers) == 0
	case VisibilityPeers:
		return len(v.Peers) > 0
	}
	return false
}

// Includes reports whether the machine may see the session. An invalid
// visibility includes nobody.
func (v Visibility) Includes(id MachineID) bool {
	if !v.Valid() {
		return false
	}
	switch v.Mode {
	case VisibilityAllPeers:
		return true
	case VisibilityPeers:
		return slices.Contains(v.Peers, id)
	}
	return false
}
