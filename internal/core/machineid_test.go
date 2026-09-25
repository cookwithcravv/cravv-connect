package core

import "testing"

func TestMachineIDShort(t *testing.T) {
	tests := []struct {
		in   MachineID
		want string
	}{
		{"abcdefghijklmnopqrstuvwxyz234567abcdefghijklmnopqrst", "abcdefghijklmnop"},
		{"short", "short"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := tt.in.Short(); got != tt.want {
			t.Errorf("MachineID(%q).Short() = %q, want %q", tt.in, got, tt.want)
		}
	}
}
