package core

import "testing"

func TestKindIsControl(t *testing.T) {
	tests := []struct {
		k    Kind
		want bool
	}{
		{KindChat, false},
		{KindTaskCreate, false},
		{KindTaskUpdate, false},
		{KindTaskCancel, false},
		{KindFileOffer, false},
		{KindControlPrekey, true},
		{KindControlStalePrekey, true},
		{KindControlDelivered, true},
		{KindControlPaused, true},
		{KindControlResumed, true},
		{KindControlUnpaired, true},
		{KindControlRelayMoved, true},
		{KindControlUnsupported, true},
		{KindLinkRequest, false},
		{KindPresencePing, false},
		{Kind("control"), false},
		{Kind("xcontrol.prekey"), false},
	}
	for _, tt := range tests {
		if got := tt.k.IsControl(); got != tt.want {
			t.Errorf("Kind(%q).IsControl() = %v, want %v", tt.k, got, tt.want)
		}
	}
}

func TestKindTraits(t *testing.T) {
	tests := []struct {
		k                                Kind
		linkScoped, ephemeral, receipted bool
	}{
		{KindChat, true, false, true},
		{KindTaskCreate, true, false, true},
		{KindTaskUpdate, true, false, true},
		{KindTaskCancel, true, false, true},
		{KindFileOffer, true, false, true},
		{KindSessionsList, false, true, false},
		{KindSessionsListed, false, true, false},
		{KindPresencePing, false, true, false},
		{KindPresencePong, false, true, false},
		{KindLinkRequest, false, false, true},
		{KindLinkAccepted, false, false, true},
		{KindLinkRejected, false, false, true},
		{KindLinkClosed, false, false, true},
		{KindLinkState, false, false, true},
		{KindControlUnsupported, false, false, false},
		{KindControlDelivered, false, false, false},
		{Kind("bogus"), false, false, true},
	}
	for _, tt := range tests {
		if got := tt.k.LinkScoped(); got != tt.linkScoped {
			t.Errorf("%s.LinkScoped() = %v, want %v", tt.k, got, tt.linkScoped)
		}
		if got := tt.k.Ephemeral(); got != tt.ephemeral {
			t.Errorf("%s.Ephemeral() = %v, want %v", tt.k, got, tt.ephemeral)
		}
		if got := tt.k.Receipted(); got != tt.receipted {
			t.Errorf("%s.Receipted() = %v, want %v", tt.k, got, tt.receipted)
		}
	}
}
