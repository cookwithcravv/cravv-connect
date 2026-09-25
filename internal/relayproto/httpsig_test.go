package relayproto

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func signedReq(t *testing.T, priv ed25519.PrivateKey, method, target string, ts time.Time, body []byte) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	SignRequest(r, func(m []byte) []byte { return ed25519.Sign(priv, m) }, priv.Public().(ed25519.PublicKey), ts.Unix(), body)
	return r
}

func TestHTTPMessageExactBytes(t *testing.T) {
	got := string(HTTPMessage("PUT", "/v1/blobs/abc/chunks/0", 1700000000, []byte("hi")))
	want := "cravv-http-v1\nPUT\n/v1/blobs/abc/chunks/0\n1700000000\n8f434346648f6b96df89dda901c5176b10a6d83961dd3c1ac88b59b2dc327aa4"
	if got != want {
		t.Fatalf("HTTPMessage = %q, want %q", got, want)
	}
}

func TestVerifyRequest(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{"size":1}`)

	t.Run("valid", func(t *testing.T) {
		r := signedReq(t, priv, "POST", "/v1/blobs", now, body)
		ik, err := VerifyRequest(r, body, now)
		if err != nil || !ik.Equal(pub) {
			t.Fatalf("VerifyRequest = %v, %v", ik, err)
		}
	})
	t.Run("query string not signed", func(t *testing.T) {
		r := signedReq(t, priv, "GET", "/v1/blobs/x/chunks/0?cache=1", now, nil)
		if _, err := VerifyRequest(r, nil, now); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
	skew := []struct {
		name string
		d    time.Duration
		want error
	}{
		{"4m59s behind ok", -(4*time.Minute + 59*time.Second), nil},
		{"4m59s ahead ok", 4*time.Minute + 59*time.Second, nil},
		{"5m1s behind rejected", -(5*time.Minute + time.Second), ErrClockSkew},
		{"5m1s ahead rejected", 5*time.Minute + time.Second, ErrClockSkew},
	}
	for _, tc := range skew {
		t.Run(tc.name, func(t *testing.T) {
			r := signedReq(t, priv, "POST", "/v1/blobs", now.Add(tc.d), body)
			_, err := VerifyRequest(r, body, now)
			if !errors.Is(err, tc.want) && !(tc.want == nil && err == nil) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	tamper := []struct {
		name string
		mod  func(r *http.Request) []byte
	}{
		{"body changed", func(r *http.Request) []byte { return []byte(`{"size":2}`) }},
		{"method changed", func(r *http.Request) []byte { r.Method = "PUT"; return body }},
		{"path changed", func(r *http.Request) []byte { r.URL.Path = "/v1/blobs/other"; return body }},
		{"ts changed", func(r *http.Request) []byte { r.Header.Set(HeaderTS, "1800000001"); return body }},
		{"other key", func(r *http.Request) []byte {
			other, _, _ := ed25519.GenerateKey(rand.Reader)
			r.Header.Set(HeaderIK, B64(other))
			return body
		}},
		{"sig garbage", func(r *http.Request) []byte { r.Header.Set(HeaderSig, "@@@"); return body }},
	}
	for _, tc := range tamper {
		t.Run(tc.name, func(t *testing.T) {
			r := signedReq(t, priv, "POST", "/v1/blobs", now, body)
			b := tc.mod(r)
			if _, err := VerifyRequest(r, b, now); !errors.Is(err, ErrBadSignature) {
				t.Fatalf("err = %v, want ErrBadSignature", err)
			}
		})
	}
	t.Run("missing headers", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/v1/blobs", nil)
		if _, err := VerifyRequest(r, body, now); !errors.Is(err, ErrMissingSignature) {
			t.Fatalf("err = %v", err)
		}
	})
}
