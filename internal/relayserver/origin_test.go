package relayserver

import (
	"net/http"
	"strings"
	"testing"
)

func TestNewRejectsBadOrigin(t *testing.T) {
	for _, o := range []string{"", "ftp://x", "https://x/path", "relay.example.com"} {
		if srv, err := New(Config{PublicOrigin: o}, NewMemoryBackend(nil)); err == nil {
			srv.Close()
			t.Errorf("New accepted origin %q", o)
		}
	}
}

// A relay configured with a non-canonical origin must verify against the normalized form.
func TestPublicOriginNormalized(t *testing.T) {
	tr := newTestRelayCfg(t, func(c *Config) {
		c.PublicOrigin = strings.ToUpper(c.PublicOrigin[:4]) + c.PublicOrigin[4:] + "/"
	})
	up, rcpt := newKey(t), newKey(t)
	tr.member(t, up) // auth signs AuthMessage(tr.ts.URL, nonce)
	tr.member(t, rcpt)
	if code, _ := tr.createBlob(t, up, rcpt, 1, 1); code != http.StatusCreated {
		t.Fatalf("create with normalized origin = %d", code)
	}
}

func TestHTTPSignatureBindsOrigin(t *testing.T) {
	tr := newTestRelay(t, Limits{})
	up := newKey(t)
	tr.member(t, up)
	if code, _ := tr.doOrigin(t, "https://other-relay.example", up, "GET", chunkPath("x", 0), nil, 0); code != http.StatusUnauthorized {
		t.Fatalf("wrong-origin signature = %d, want 401", code)
	}
	if code, _ := tr.do(t, up, "GET", chunkPath("x", 0), nil, 0); code != http.StatusNotFound {
		t.Fatalf("right-origin signature = %d, want 404", code)
	}
}
