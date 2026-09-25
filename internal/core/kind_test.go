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
		{Kind("control"), false},
		{Kind("xcontrol.prekey"), false},
	}
	for _, tt := range tests {
		if got := tt.k.IsControl(); got != tt.want {
			t.Errorf("Kind(%q).IsControl() = %v, want %v", tt.k, got, tt.want)
		}
	}
}
