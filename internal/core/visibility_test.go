package core

import "testing"

func TestVisibilityIncludes(t *testing.T) {
	a, b := MachineID("aaaa"), MachineID("bbbb")
	tests := []struct {
		v            Visibility
		valid, seesA bool
	}{
		{Visibility{Mode: VisibilityPrivate}, true, false},
		{Visibility{Mode: VisibilityAllPeers}, true, true},
		{Visibility{Mode: VisibilityPeers, Peers: []MachineID{a}}, true, true},
		{Visibility{Mode: VisibilityPeers, Peers: []MachineID{b}}, true, false},
		{Visibility{Mode: VisibilityPeers}, false, false},
		{Visibility{Mode: VisibilityPrivate, Peers: []MachineID{a}}, false, false},
		{Visibility{Mode: VisibilityAllPeers, Peers: []MachineID{a}}, false, false},
		{Visibility{}, false, false},
		{Visibility{Mode: "public"}, false, false},
	}
	for _, tt := range tests {
		if got := tt.v.Valid(); got != tt.valid {
			t.Errorf("%+v.Valid() = %v, want %v", tt.v, got, tt.valid)
		}
		if got := tt.v.Includes(a); got != tt.seesA {
			t.Errorf("%+v.Includes(a) = %v, want %v", tt.v, got, tt.seesA)
		}
	}
}
