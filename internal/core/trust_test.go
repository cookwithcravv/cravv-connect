package core

import "testing"

func TestParseTrustRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		level TrustLevel
	}{
		{"chat-only", TrustChatOnly},
		{"ask-first", TrustAskFirst},
		{"autonomous", TrustAutonomous},
	}
	for _, tt := range tests {
		got, err := ParseTrust(tt.name)
		if err != nil {
			t.Fatalf("ParseTrust(%q): %v", tt.name, err)
		}
		if got != tt.level {
			t.Errorf("ParseTrust(%q) = %d, want %d", tt.name, got, tt.level)
		}
		if got.String() != tt.name {
			t.Errorf("String() = %q, want %q", got.String(), tt.name)
		}
		if !got.Valid() {
			t.Errorf("%q not Valid()", tt.name)
		}
	}
}

func TestParseTrustInvalid(t *testing.T) {
	for _, s := range []string{"", "Autonomous", "ask_first", "full", " chat-only", "3"} {
		if _, err := ParseTrust(s); err == nil {
			t.Errorf("ParseTrust(%q) succeeded, want error", s)
		}
	}
}

func TestTrustLevelInvalidValues(t *testing.T) {
	for _, l := range []TrustLevel{0, -1, 4, 99} {
		if l.Valid() {
			t.Errorf("TrustLevel(%d).Valid() = true", l)
		}
		if l.String() != "invalid" {
			t.Errorf("TrustLevel(%d).String() = %q, want invalid", l, l.String())
		}
	}
}

func TestTrustOrdering(t *testing.T) {
	if !(TrustChatOnly < TrustAskFirst && TrustAskFirst < TrustAutonomous) {
		t.Fatal("trust levels must increase: chat-only < ask-first < autonomous")
	}
}
