package conformance_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/conformance"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/keys"
	"github.com/cravv/cravv-connect/internal/relayserver"
	"github.com/cravv/cravv-connect/internal/transport"
)

// TestRelayServer runs the full suite, TTL cases included, against the in-process
// reference relay on a fake clock. Rate limits are off because the fake clock never
// refills token buckets on its own.
func TestRelayServer(t *testing.T) {
	clock := core.NewFakeClock(time.Now())
	var h http.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }))
	defer ts.Close()
	srv, err := relayserver.New(relayserver.Config{
		PublicOrigin: ts.URL,
		AdminToken:   "conformance-admin",
		Clock:        clock,
		Limits:       relayserver.Limits{OpRate: -1, IPRate: -1},
	}, relayserver.NewMemoryBackend(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	h = srv
	conformance.Run(t, conformance.Target{
		URL:        ts.URL,
		AdminToken: "conformance-admin",
		NewIdentity: func() transport.Signer {
			id, err := keys.GenerateIdentity()
			if err != nil {
				t.Fatal(err)
			}
			return id
		},
		Clock:   clock,
		Advance: clock.Advance,
	})
}
