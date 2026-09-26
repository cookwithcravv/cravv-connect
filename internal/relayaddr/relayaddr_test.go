package relayaddr

import (
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	good := []string{"https://relay.example", "https://relay.example:8443/x", "http://localhost:8787",
		"http://127.0.0.1:8787", "http://[::1]:8787", "http://192.168.1.10:8787", "http://10.0.0.5",
		"http://172.16.4.2:8787", "http://[fd12::1]:8787", "http://100.101.102.103:8787", "http://mac.local:8787",
		"http://MAC.LOCAL:8787", "http://[::ffff:192.168.1.10]:8787"}
	bad := []string{"", "ftp://x", "javascript:alert(1)", "https://", "http://relay.example",
		"http://localhost.evil.example", "relay.example", "http://8.8.8.8", "http://172.32.0.1",
		"http://local.evil.example", "http://192.168.1.10.evil.example", "http://a.b.local:8787",
		"http://.local", "https://user@relay.example"}
	for _, u := range good {
		if err := Check(u); err != nil {
			t.Errorf("Check(%q) = %v", u, err)
		}
	}
	for _, u := range bad {
		if err := Check(u); err == nil {
			t.Errorf("Check(%q) accepted", u)
		}
	}
	if err := Check("http://relay.example"); err == nil || !strings.Contains(err.Error(), "plain http is only allowed") {
		t.Errorf("public http: %v", err)
	}
}

func TestPrivateHost(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": true, "127.0.0.1": true, "::1": true, "10.1.2.3": true, "192.168.0.1": true,
		"100.64.0.1": true, "fd00::1": true, "gpu-box.local": true,
		"example.com": false, "8.8.8.8": false, "100.128.0.1": false, "a.b.local": false, "local": false,
	} {
		if got := PrivateHost(host); got != want {
			t.Errorf("PrivateHost(%q) = %v, want %v", host, got, want)
		}
	}
}
