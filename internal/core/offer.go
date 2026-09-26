package core

import (
	"fmt"
	"strings"
	"time"
)

// RunMode says what a managed run may do in its folder (v2 spec 6.1).
type RunMode string

const (
	// RunReadOnly reads and searches the folder only.
	RunReadOnly RunMode = "read-only"
	// RunEditInFolder also edits files, with no shell and no web access.
	RunEditInFolder RunMode = "edit-in-folder"
	// RunShell also runs commands as the owner's user. The rule editor
	// makes the human type "shell" to choose it.
	RunShell RunMode = "shell"
)

// ParseRunMode parses "read-only", "edit-in-folder" or "shell".
func ParseRunMode(s string) (RunMode, error) {
	m := RunMode(strings.TrimSpace(s))
	if !m.Valid() {
		return "", fmt.Errorf("invalid run mode %q (use read-only, edit-in-folder, or shell)", s)
	}
	return m, nil
}

// Valid reports whether m is one of the three modes.
func (m RunMode) Valid() bool {
	return m == RunReadOnly || m == RunEditInFolder || m == RunShell
}

// Offer rule defaults and bounds (v2 spec 6.1).
const (
	DefaultMaxConcurrent  = 2
	DefaultIdleTimeout    = 2 * time.Hour
	DefaultMaxTurnsPerRun = 40
	DefaultRunTimeout     = 30 * time.Minute
	DefaultRunsPerHour    = 30  // per link
	DefaultRunsPerDay     = 200 // per peer machine

	// MaxOfferLabel leaves room in a session name (MaxSessionName) for "-"
	// and the 4-character suffix of a managed session.
	MaxOfferLabel = MaxSessionName - 5
	// ManagedAgentClaude is the only agent v2 offers.
	ManagedAgentClaude = "claude"
)

// ValidOfferLabel reports whether s can label an offer: 1 to MaxOfferLabel
// characters of [a-z0-9-], not starting with '-'.
func ValidOfferLabel(s string) bool {
	return len(s) <= MaxOfferLabel && ValidSessionName(s)
}
