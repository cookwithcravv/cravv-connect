package daemon

import "testing"

func TestValidRelayURL(t *testing.T) {
	good := []string{"https://relay.example", "https://relay.example:8443/x", "http://localhost:8787",
		"http://127.0.0.1:8787", "http://[::1]:8787", "http://192.168.1.10:8787", "http://10.0.0.5",
		"http://172.16.4.2:8787", "http://[fd12::1]:8787", "http://100.101.102.103:8787", "http://mac.local:8787"}
	bad := []string{"", "ftp://x", "javascript:alert(1)", "https://", "http://relay.example",
		"http://localhost.evil.example", "relay.example", "http://8.8.8.8", "http://172.32.0.1",
		"http://local.evil.example", "http://192.168.1.10.evil.example"}
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
