package relayserver

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/relayproto"
)

// do sends a request signed by k for the relay's origin at the relay clock's time (plus skew).
func (tr *testRelay) do(t *testing.T, k ed25519.PrivateKey, method, path string, body []byte, skew time.Duration) (int, []byte) {
	t.Helper()
	return tr.doOrigin(t, tr.ts.URL, k, method, path, body, skew)
}

// doOrigin is do with the origin bound into the signature chosen by the caller.
func (tr *testRelay) doOrigin(t *testing.T, origin string, k ed25519.PrivateKey, method, path string, body []byte, skew time.Duration) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, tr.ts.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	relayproto.SignRequest(req, origin, func(m []byte) []byte { return ed25519.Sign(k, m) }, pubOf(k), tr.clock.Now().Add(skew).Unix(), body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (tr *testRelay) createBlob(t *testing.T, up, rcpt ed25519.PrivateKey, size int64, chunks uint32) (int, string) {
	t.Helper()
	body, _ := json.Marshal(relayproto.BlobCreateRequest{Size: size, Chunks: chunks, Recipient: relayproto.B64(pubOf(rcpt))})
	code, out := tr.do(t, up, "POST", relayproto.PathBlobs, body, 0)
	var r relayproto.BlobCreateResponse
	_ = json.Unmarshal(out, &r)
	return code, r.BlobID
}

func chunkPath(id string, n int) string {
	return fmt.Sprintf("%s/%s/chunks/%d", relayproto.PathBlobs, id, n)
}

func TestBlobACLAndLifecycle(t *testing.T) {
	tr := newTestRelay(t, Limits{})
	up, rcpt, other, stranger := newKey(t), newKey(t), newKey(t), newKey(t)
	for _, k := range []ed25519.PrivateKey{up, rcpt, other} {
		tr.member(t, k)
	}
	code, id := tr.createBlob(t, up, rcpt, 1<<20+10, 2)
	if code != http.StatusCreated || id == "" {
		t.Fatalf("create = %d %q", code, id)
	}
	steps := []struct {
		name   string
		k      ed25519.PrivateKey
		method string
		path   string
		body   []byte
		want   int
	}{
		{"stranger cannot create", stranger, "POST", relayproto.PathBlobs, []byte(`{"size":1,"chunks":1,"recipient":"` + relayproto.B64(pubOf(rcpt)) + `"}`), http.StatusForbidden},
		{"recipient cannot put", rcpt, "PUT", chunkPath(id, 0), []byte("hello"), http.StatusForbidden},
		{"other cannot put", other, "PUT", chunkPath(id, 0), []byte("hello"), http.StatusForbidden},
		{"chunk not uploaded yet", rcpt, "GET", chunkPath(id, 0), nil, http.StatusNotFound},
		{"uploader puts chunk 0", up, "PUT", chunkPath(id, 0), []byte("hello"), http.StatusNoContent},
		{"uploader puts chunk 1", up, "PUT", chunkPath(id, 1), []byte("world"), http.StatusNoContent},
		{"chunk index out of range", up, "PUT", chunkPath(id, 2), []byte("x"), http.StatusBadRequest},
		{"uploader cannot get", up, "GET", chunkPath(id, 0), nil, http.StatusForbidden},
		{"other cannot get", other, "GET", chunkPath(id, 0), nil, http.StatusForbidden},
		{"stranger cannot get", stranger, "GET", chunkPath(id, 0), nil, http.StatusForbidden},
		{"recipient gets", rcpt, "GET", chunkPath(id, 1), nil, http.StatusOK},
		{"unknown blob", rcpt, "GET", chunkPath("nope", 0), nil, http.StatusNotFound},
		{"other cannot delete", other, "DELETE", relayproto.PathBlobs + "/" + id, nil, http.StatusForbidden},
		{"recipient deletes", rcpt, "DELETE", relayproto.PathBlobs + "/" + id, nil, http.StatusNoContent},
		{"gone after delete", rcpt, "GET", chunkPath(id, 0), nil, http.StatusNotFound},
	}
	for _, s := range steps {
		code, body := tr.do(t, s.k, s.method, s.path, s.body, 0)
		if code != s.want {
			t.Fatalf("%s: status %d, want %d (%s)", s.name, code, s.want, body)
		}
		if s.name == "recipient gets" && string(body) != "world" {
			t.Fatalf("chunk body = %q", body)
		}
	}
}

func TestBlobErrorBodyIsJSON(t *testing.T) {
	tr := newTestRelay(t, Limits{})
	k := newKey(t)
	code, body := tr.do(t, k, "GET", chunkPath("nope", 0), nil, 0)
	var e relayproto.HTTPErrorBody
	if code != http.StatusForbidden || json.Unmarshal(body, &e) != nil || e.Code != relayproto.CodeForbidden || e.Message == "" {
		t.Fatalf("status %d body %s", code, body)
	}
}

func TestBlobLimits(t *testing.T) {
	tr := newTestRelay(t, Limits{})
	up, rcpt := newKey(t), newKey(t)
	tr.member(t, up)
	tr.member(t, rcpt)

	if code, _ := tr.createBlob(t, up, rcpt, 100<<20+1, 101); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize create = %d", code)
	}
	if code, _ := tr.createBlob(t, up, rcpt, 100<<20, 100); code != http.StatusCreated {
		t.Fatalf("max size create = %d", code)
	}
	_, id := tr.createBlob(t, up, rcpt, 1<<20, 1)
	if code, _ := tr.do(t, up, "PUT", chunkPath(id, 0), make([]byte, 1<<20+65), 0); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize chunk = %d", code)
	}
	if code, _ := tr.do(t, up, "PUT", chunkPath(id, 0), make([]byte, 1<<20+64), 0); code != http.StatusNoContent {
		t.Fatalf("max chunk = %d", code)
	}
	chunkCounts := []struct {
		size   int64
		chunks uint32
		want   int
	}{
		{0, 1, http.StatusCreated},
		{0, 2, http.StatusBadRequest},
		{1 << 20, 1, http.StatusCreated},
		{1 << 20, 2, http.StatusBadRequest},
		{1<<20 + 1, 2, http.StatusCreated},
		{10, 0, http.StatusBadRequest},
	}
	for _, cc := range chunkCounts {
		if code, _ := tr.createBlob(t, up, rcpt, cc.size, cc.chunks); code != cc.want {
			t.Fatalf("size %d chunks %d: %d, want %d", cc.size, cc.chunks, code, cc.want)
		}
	}
	_, small := tr.createBlob(t, up, rcpt, 10, 1)
	if code, _ := tr.do(t, up, "PUT", chunkPath(small, 0), make([]byte, 75), 0); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunk beyond declared size = %d", code)
	}
}

func TestBlobQuota(t *testing.T) {
	tr := newTestRelay(t, Limits{BlobQuota: 100})
	up, rcpt := newKey(t), newKey(t)
	tr.member(t, up)
	tr.member(t, rcpt)
	if code, _ := tr.createBlob(t, up, rcpt, 60, 1); code != http.StatusCreated {
		t.Fatalf("first = %d", code)
	}
	if code, _ := tr.createBlob(t, up, rcpt, 50, 1); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over quota = %d", code)
	}
	if code, _ := tr.createBlob(t, rcpt, up, 50, 1); code != http.StatusCreated {
		t.Fatalf("quota is per uploader, got %d", code)
	}
	tr.clock.Advance(7 * 24 * time.Hour)
	if code, _ := tr.createBlob(t, up, rcpt, 50, 1); code != http.StatusCreated {
		t.Fatalf("expired blobs still counted: %d", code)
	}
}

func TestBlobPerIPLimit(t *testing.T) {
	tr := newTestRelay(t, Limits{IPRate: 1, IPBurst: 2})
	k := newKey(t)
	for i, want := range []int{http.StatusForbidden, http.StatusForbidden, http.StatusTooManyRequests} {
		if code, _ := tr.do(t, k, "GET", chunkPath("x", 0), nil, 0); code != want {
			t.Fatalf("request %d = %d, want %d", i, code, want)
		}
	}
}

func TestBlobAuthAndExpiry(t *testing.T) {
	tr := newTestRelay(t, Limits{})
	up, rcpt := newKey(t), newKey(t)
	tr.member(t, up)
	tr.member(t, rcpt)
	_, id := tr.createBlob(t, up, rcpt, 5, 1)

	if code, _ := tr.do(t, up, "PUT", chunkPath(id, 0), []byte("hello"), 6*time.Minute); code != http.StatusUnauthorized {
		t.Fatalf("skewed = %d", code)
	}
	req, _ := http.NewRequest("PUT", tr.ts.URL+chunkPath(id, 0), bytes.NewReader([]byte("hello")))
	relayproto.SignRequest(req, tr.ts.URL, func(m []byte) []byte { return ed25519.Sign(up, m) }, pubOf(up), tr.clock.Now().Unix(), []byte("other body"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("tampered body = %d", resp.StatusCode)
	}
	if code, _ := tr.do(t, up, "PUT", chunkPath(id, 0), []byte("hello"), 0); code != http.StatusNoContent {
		t.Fatalf("put = %d", code)
	}
	tr.clock.Advance(7 * 24 * time.Hour)
	if code, _ := tr.do(t, rcpt, "GET", chunkPath(id, 0), nil, 0); code != http.StatusGone {
		t.Fatalf("expired = %d", code)
	}
}
