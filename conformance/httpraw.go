package conformance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
	"github.com/cookwithcravv/cravv-connect/internal/transport"
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
	return s.sendHTTP(t, method, path, body, sigHeaders(id, signOrigin, method, path, strconv.FormatInt(ts.Unix(), 10), body))
}

// sigHeaders returns the signature headers for a request signed by id over exactly these
// strings, so a case can sign something other than what it sends. The signing string is
// relayproto.HTTPMessage's, with ts taken as text so a malformed X-Cravv-TS can be signed too.
func sigHeaders(id transport.Signer, origin, method, path, ts string, body []byte) http.Header {
	sum := sha256.Sum256(body)
	msg := relayproto.HTTPContext + "\n" + origin + "\n" + method + "\n" + path + "\n" + ts + "\n" + hex.EncodeToString(sum[:])
	h := http.Header{}
	h.Set(relayproto.HeaderIK, relayproto.B64(id.Public()))
	h.Set(relayproto.HeaderTS, ts)
	h.Set(relayproto.HeaderSig, relayproto.B64(id.Sign([]byte(msg))))
	return h
}

// sendHTTP sends a blob request with exactly the given headers and returns the status.
func (s *suite) sendHTTP(t *testing.T, method, path string, body []byte, h http.Header) int {
	t.Helper()
	req, err := http.NewRequestWithContext(ctxT(t), method, s.client.Origin()+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range h {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}
