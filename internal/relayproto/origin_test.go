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

// Hosts must be plain ASCII: a non-ASCII host has to be given in punycode, so
// a lookalike (Cyrillic U+0430 for Latin "a") or an invisible character cannot
// pass for another relay when a person reads it back.
func TestNormalizeOriginRefusesNonASCIIAndControl(t *testing.T) {
	for _, in := range []string{
		"https://\u0430pple.com",               // Cyrillic a
		"https://relay.ex\u0430mple.com",       // Cyrillic a inside
		"https://relay\u200b.example.com",      // zero-width space
		"https://\u202erelay.example.com",      // right-to-left override
		"https://relay.example.com\u200e",      // left-to-right mark
		"https://relay.example.com\u00a0",      // no-break space
		"https://relay.example.com:8443\u2060", // word joiner
		"https://relay.example.com\x7f",
		"https://relay.example.com\t",
		"https://relay.example.com\x00",
		" https://relay.example.com",
	} {
		if got, err := NormalizeOrigin(in); err == nil {
			t.Errorf("NormalizeOrigin(%q) = %q, want error", in, got)
		}
	}
	got, err := NormalizeOrigin("https://xn--pple-43d.com")
	if err != nil || got != "https://xn--pple-43d.com" {
		t.Fatalf("punycode host: %q %v", got, err)
	}
}
