package relayclient_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/transport"
	"github.com/cookwithcravv/cravv-connect/internal/transport/relayclient"
)

// stallingServer accepts every request and never answers it.
func stallingServer(t *testing.T) string {
	t.Helper()
	stop := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-stop:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(stop); ts.Close() })
	return ts.URL
}

// A relay that accepts the TCP connection but never answers the WebSocket
// upgrade fails the dial after the dial timeout, even when the caller's
// context has no deadline.
func TestDialTimesOutOnAStalledUpgrade(t *testing.T) {
	c, err := relayclient.New(stallingServer(t), relayclient.WithDialTimeout(100*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := c.Dialer().Dial(t.Context(), ident(t), transport.Credentials{}); err == nil {
		t.Fatal("dial succeeded")
	}
	if waited := time.Since(start); waited > 3*time.Second {
		t.Fatalf("dial waited %v", waited)
	}
	start = time.Now()
	if _, err := c.Rooms().Open(t.Context(), "ABCD", ""); err == nil {
		t.Fatal("room open succeeded")
	}
	if waited := time.Since(start); waited > 3*time.Second {
		t.Fatalf("room open waited %v", waited)
	}
}

// A blob chunk request that stalls fails after the blob timeout, so the
// download counts a failed attempt instead of hanging.
func TestBlobRequestTimesOut(t *testing.T) {
	c, err := relayclient.New(stallingServer(t), relayclient.WithBlobTimeout(100*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	b := c.Blobs(ident(t))
	start := time.Now()
	if _, err := b.GetChunk(t.Context(), "abc", 0); err == nil {
		t.Fatal("GetChunk succeeded")
	}
	if err := b.PutChunk(t.Context(), "abc", 0, make([]byte, 1024)); err == nil {
		t.Fatal("PutChunk succeeded")
	}
	if waited := time.Since(start); waited > 3*time.Second {
		t.Fatalf("blob requests waited %v", waited)
	}
}
