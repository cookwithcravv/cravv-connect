package core

import "testing"

func TestParseRunMode(t *testing.T) {
	for _, s := range []string{"read-only", "edit-in-folder", "shell", " shell "} {
		m, err := ParseRunMode(s)
		if err != nil || !m.Valid() {
			t.Fatalf("ParseRunMode(%q) = %q, %v", s, m, err)
		}
	}
	for _, s := range []string{"", "readonly", "Shell", "bypass", "edit"} {
		if _, err := ParseRunMode(s); err == nil {
			t.Errorf("ParseRunMode(%q) succeeded, want error", s)
		}
	}
}

func TestValidOfferLabel(t *testing.T) {
	for _, s := range []string{"trainer", "gpu-1", "a", "abcdefghijklmnopqrstuvwxyz0"} {
		if !ValidOfferLabel(s) {
			t.Errorf("%q should be a valid label", s)
		}
	}
	for _, s := range []string{"", "-x", "Trainer", "has space", "new:x", "abcdefghijklmnopqrstuvwxyz01"} {
		if ValidOfferLabel(s) {
			t.Errorf("%q should not be a valid label", s)
		}
	}
	if MaxOfferLabel+5 != MaxSessionName {
		t.Fatalf("a label plus -xxxx must fit a session name")
	}
}
