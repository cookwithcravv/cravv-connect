package daemon

import "testing"

func TestValidRelayURL(t *testing.T) {
	good := []string{"https://relay.example", "https://relay.example:8443/x", "http://localhost:8787",
		"http://127.0.0.1:8787", "http://[::1]:8787"}
	bad := []string{"", "ftp://x", "javascript:alert(1)", "https://", "http://relay.example",
		"http://localhost.evil.example", "http://10.0.0.5", "relay.example"}
	for _, u := range good {
		if err := validRelayURL(u); err != nil {
			t.Errorf("validRelayURL(%q) = %v", u, err)
		}
	}
	for _, u := range bad {
		if err := validRelayURL(u); err == nil {
			t.Errorf("validRelayURL(%q) accepted", u)
		}
	}
}
