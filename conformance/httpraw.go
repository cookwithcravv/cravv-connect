package conformance

import (
	"bytes"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/relayproto"
	"github.com/cravv/cravv-connect/internal/transport"
)

// now is the relay's clock when the target exposes it, else system time.
func (s *suite) now() time.Time {
	if s.tg.Clock != nil {
		return s.tg.Clock.Now()
	}
	return core.SystemClock{}.Now()
}

// rawHTTP sends a blob request signed by id for signOrigin at ts and returns the status.
func (s *suite) rawHTTP(t *testing.T, id transport.Signer, signOrigin, method, path string, body []byte, ts time.Time) int {
	t.Helper()
	req, err := http.NewRequestWithContext(ctxT(t), method, s.client.Origin()+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	relayproto.SignRequest(req, signOrigin, id.Sign, id.Public(), ts.Unix(), body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}
