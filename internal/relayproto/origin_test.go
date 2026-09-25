package relayproto

import "testing"

func TestNormalizeOrigin(t *testing.T) {
	good := map[string]string{
		"https://relay.example.com":      "https://relay.example.com",
		"https://relay.example.com/":     "https://relay.example.com",
		"HTTPS://Relay.Example.COM":      "https://relay.example.com",
		"https://relay.example.com:443":  "https://relay.example.com",
		"https://relay.example.com:8443": "https://relay.example.com:8443",
		"http://relay.example.com:80":    "http://relay.example.com",
		"http://relay.example.com:443":   "http://relay.example.com:443",
		"https://relay.example.com:80":   "https://relay.example.com:80",
		"https://relay.example.com.":     "https://relay.example.com",
		"https://relay.example.com.:443": "https://relay.example.com",
		"http://127.0.0.1:8787":          "http://127.0.0.1:8787",
		"http://[::1]:8787":              "http://[::1]:8787",
		"http://[::1]:80":                "http://[::1]",
		"https://[2001:DB8::1]":          "https://[2001:db8::1]",
		"https://relay.example.com:":     "https://relay.example.com",
	}
	for in, want := range good {
		got, err := NormalizeOrigin(in)
		if err != nil || got != want {
			t.Errorf("NormalizeOrigin(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{
		"", "relay.example.com", "ftp://x", "wss://x", "https://", "https://x/path",
		"https://x?q=1", "https://x#f", "https://u:p@x", "https://x:0", "https://x:65536",
		"https://x:abc", "https://.", "https://[::1", "http://::1",
	}
	for _, in := range bad {
		if got, err := NormalizeOrigin(in); err == nil {
			t.Errorf("NormalizeOrigin(%q) = %q, want error", in, got)
		}
	}
}

func TestNormalizeOriginIdempotent(t *testing.T) {
	for _, in := range []string{"https://A.b.:443", "http://[::1]:80", "http://127.0.0.1:8787/"} {
		once, err := NormalizeOrigin(in)
		if err != nil {
			t.Fatal(err)
		}
		twice, err := NormalizeOrigin(once)
		if err != nil || twice != once {
			t.Fatalf("not idempotent: %q -> %q -> %q (%v)", in, once, twice, err)
		}
	}
}
