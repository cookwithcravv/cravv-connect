package core

import (
	"strings"
	"testing"
)

func TestValidSessionName(t *testing.T) {
	for _, s := range []string{"trainer", "a", "gpu-box-2", strings.Repeat("a", 32), "0day"} {
		if !ValidSessionName(s) {
			t.Errorf("ValidSessionName(%q) = false", s)
		}
	}
	for _, s := range []string{"", "-lead", "Trainer", "a_b", "a.b", "a b", strings.Repeat("a", 33), "naïve"} {
		if ValidSessionName(s) {
			t.Errorf("ValidSessionName(%q) = true", s)
		}
	}
}

func TestValidPurposeAndNote(t *testing.T) {
	if !ValidPurpose(strings.Repeat("é", 120)) || ValidPurpose(strings.Repeat("é", 121)) {
		t.Error("purpose limit is 120 characters")
	}
	if ValidPurpose("two\nlines") || ValidPurpose("cr\r") || ValidPurpose("\xff") {
		t.Error("purpose must be one line of valid UTF-8")
	}
	if !ValidNote(strings.Repeat("x", 280)) || ValidNote(strings.Repeat("x", 281)) || ValidNote("\xff") {
		t.Error("note limit is 280 characters of valid UTF-8")
	}
	if !ValidNote("multi\nline is fine") {
		t.Error("notes may span lines")
	}
}

func TestReasonSets(t *testing.T) {
	for _, r := range []string{RejectDeclined, RejectNotFound, RejectBusy, RejectPolicy, RejectTimeout} {
		if !ValidRejectReason(r) {
			t.Errorf("reject reason %q", r)
		}
	}
	for _, r := range []string{CloseClosedByPeer, CloseSessionClosed, ClosePaused, CloseUnpaired, CloseKilled, ClosePresenceTimeout, CloseUnknownLink} {
		if !ValidCloseReason(r) {
			t.Errorf("close reason %q", r)
		}
	}
	if ValidRejectReason("closed_by_peer") || ValidCloseReason("declined") || ValidCloseReason("") {
		t.Error("the reason sets must not overlap or accept empty")
	}
}
