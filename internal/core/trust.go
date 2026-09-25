package core

import "fmt"

// TrustLevel is what a peer may do on this machine.
type TrustLevel int

const (
	TrustChatOnly   TrustLevel = 1
	TrustAskFirst   TrustLevel = 2
	TrustAutonomous TrustLevel = 3
)

var trustNames = map[TrustLevel]string{
	TrustChatOnly:   "chat-only",
	TrustAskFirst:   "ask-first",
	TrustAutonomous: "autonomous",
}

// ParseTrust parses "chat-only", "ask-first", or "autonomous".
func ParseTrust(s string) (TrustLevel, error) {
	for level, name := range trustNames {
		if name == s {
			return level, nil
		}
	}
	return 0, fmt.Errorf("invalid trust level %q (use chat-only, ask-first, or autonomous)", s)
}

// String returns the canonical name, or "invalid" for an unknown level.
func (t TrustLevel) String() string {
	if name, ok := trustNames[t]; ok {
		return name
	}
	return "invalid"
}

// Valid reports whether t is one of the three defined levels.
func (t TrustLevel) Valid() bool {
	_, ok := trustNames[t]
	return ok
}
