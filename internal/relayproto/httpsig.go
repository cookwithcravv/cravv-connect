package relayproto

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"time"
)

// HTTPContext prefixes the HTTP request signing string.
const HTTPContext = "cravv-http-v1"

// Signed request headers.
const (
	HeaderIK  = "X-Cravv-IK"
	HeaderTS  = "X-Cravv-TS"
	HeaderSig = "X-Cravv-Sig"
)

// MaxHTTPSkew is how far X-Cravv-TS may be from the server clock.
const MaxHTTPSkew = 5 * time.Minute

var (
	ErrMissingSignature = errors.New("relayproto: missing signature headers")
	ErrBadSignature     = errors.New("relayproto: bad request signature")
	ErrClockSkew        = errors.New("relayproto: request timestamp outside allowed skew")
)

// HTTPMessage is the byte string signed for an HTTP request.
// ts is unix seconds; path is the URL path without query string.
func HTTPMessage(method, path string, ts int64, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte(HTTPContext + "\n" + method + "\n" + path + "\n" + strconv.FormatInt(ts, 10) + "\n" + hex.EncodeToString(sum[:]))
}

// SignRequest sets the three signature headers on r. ts is unix seconds.
func SignRequest(r *http.Request, sign func([]byte) []byte, ik ed25519.PublicKey, ts int64, body []byte) {
	sig := sign(HTTPMessage(r.Method, r.URL.Path, ts, body))
	r.Header.Set(HeaderIK, B64(ik))
	r.Header.Set(HeaderTS, strconv.FormatInt(ts, 10))
	r.Header.Set(HeaderSig, B64(sig))
}

// VerifyRequest checks the signature headers of r against body and returns the signer's IK.
func VerifyRequest(r *http.Request, body []byte, now time.Time) (ed25519.PublicKey, error) {
	ikS, tsS, sigS := r.Header.Get(HeaderIK), r.Header.Get(HeaderTS), r.Header.Get(HeaderSig)
	if ikS == "" || tsS == "" || sigS == "" {
		return nil, ErrMissingSignature
	}
	ik, err := ParseIK(ikS)
	if err != nil {
		return nil, ErrBadSignature
	}
	ts, err := strconv.ParseInt(tsS, 10, 64)
	if err != nil {
		return nil, ErrBadSignature
	}
	sig, err := UnB64(sigS)
	if err != nil {
		return nil, ErrBadSignature
	}
	d := now.Sub(time.Unix(ts, 0))
	if d > MaxHTTPSkew || d < -MaxHTTPSkew {
		return nil, ErrClockSkew
	}
	if !ed25519.Verify(ik, HTTPMessage(r.Method, r.URL.Path, ts, body), sig) {
		return nil, ErrBadSignature
	}
	return ik, nil
}
