package relayserver

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/cravv/cravv-connect/internal/relayproto"
)

const maxCreateBody = 4 << 10

// httpError writes the relay-v1 JSON error body.
func httpError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(relayproto.HTTPErrorBody{Code: code, Message: msg})
}

// admit applies the per-IP limit; it writes 429 when exceeded.
func (s *Server) admit(w http.ResponseWriter, r *http.Request) bool {
	if !s.ipLimiter.allow(clientIP(r)) {
		httpError(w, http.StatusTooManyRequests, relayproto.CodeRateLimited, "too many requests from this address")
		return false
	}
	return true
}

// readBody reads at most limit bytes; ok=false (and 413 sent) when larger.
func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, bool) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			httpError(w, http.StatusRequestEntityTooLarge, relayproto.CodeTooLarge, "body too large")
		} else {
			httpError(w, http.StatusBadRequest, relayproto.CodeBadRequest, "unreadable body")
		}
		return nil, false
	}
	return b, true
}

// caller verifies the request signature and membership. It writes 401/403 on failure.
func (s *Server) caller(w http.ResponseWriter, r *http.Request, body []byte) (string, bool) {
	ik, err := relayproto.VerifyRequest(r, s.cfg.PublicOrigin, body, s.cfg.Clock.Now())
	if err != nil {
		httpError(w, http.StatusUnauthorized, relayproto.CodeAuthFailed, "bad or missing request signature")
		return "", false
	}
	mb := relayproto.MailboxID(ik)
	member, err := s.be.IsMember(r.Context(), mb)
	if err != nil {
		httpError(w, http.StatusInternalServerError, relayproto.CodeInternal, "registry unavailable")
		return "", false
	}
	if !member {
		httpError(w, http.StatusForbidden, relayproto.CodeForbidden, "not a member of this relay")
		return "", false
	}
	return mb, true
}

// blob loads a blob record, writing 404/410 on failure. Expired blobs are deleted.
func (s *Server) blob(w http.ResponseWriter, r *http.Request) (BlobRecord, bool) {
	id := r.PathValue("id")
	b, err := s.be.GetBlob(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		httpError(w, http.StatusNotFound, relayproto.CodeNotFound, "no such blob")
		return BlobRecord{}, false
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, relayproto.CodeInternal, "blob store unavailable")
		return BlobRecord{}, false
	}
	if !s.cfg.Clock.Now().Before(b.ExpiresAt) {
		_ = s.be.DeleteBlob(r.Context(), id)
		httpError(w, http.StatusGone, relayproto.CodeGone, "blob expired")
		return BlobRecord{}, false
	}
	return b, true
}

// chunkIndex parses {n}; it writes 400 when n is not below the blob's chunk count.
func chunkIndex(w http.ResponseWriter, r *http.Request, b BlobRecord) (uint32, bool) {
	n, err := strconv.ParseUint(r.PathValue("n"), 10, 32)
	if err != nil || uint32(n) >= b.Chunks {
		httpError(w, http.StatusBadRequest, relayproto.CodeBadRequest, "chunk index out of range")
		return 0, false
	}
	return uint32(n), true
}

// maxChunks is max(1, ceil(size / plaintext chunk size)).
func (s *Server) maxChunks(size int64) int64 {
	per := int64(s.cfg.Limits.MaxChunk - relayproto.ChunkOverhead)
	return max(1, (size+per-1)/per)
}

func (s *Server) handleBlobCreate(w http.ResponseWriter, r *http.Request) {
	if !s.admit(w, r) {
		return
	}
	body, ok := readBody(w, r, maxCreateBody)
	if !ok {
		return
	}
	uploader, ok := s.caller(w, r, body)
	if !ok {
		return
	}
	var req relayproto.BlobCreateRequest
	if json.Unmarshal(body, &req) != nil || req.Size < 0 {
		httpError(w, http.StatusBadRequest, relayproto.CodeBadRequest, "bad blob request")
		return
	}
	lim := s.cfg.Limits
	if req.Size > lim.MaxBlob {
		httpError(w, http.StatusRequestEntityTooLarge, relayproto.CodeTooLarge, "blob larger than the relay allows")
		return
	}
	if req.Chunks == 0 || int64(req.Chunks) > s.maxChunks(req.Size) {
		httpError(w, http.StatusBadRequest, relayproto.CodeBadRequest, "chunk count does not match size")
		return
	}
	rik, err := relayproto.ParseIK(req.Recipient)
	if err != nil {
		httpError(w, http.StatusBadRequest, relayproto.CodeBadRequest, "bad recipient")
		return
	}
	used, err := s.be.UploaderBytes(r.Context(), uploader)
	if err != nil {
		httpError(w, http.StatusInternalServerError, relayproto.CodeInternal, "blob store unavailable")
		return
	}
	if used+req.Size > lim.BlobQuota {
		httpError(w, http.StatusRequestEntityTooLarge, relayproto.CodeTooLarge, "blob storage quota exceeded")
		return
	}
	rec := BlobRecord{
		ID:        randomToken(),
		Uploader:  uploader,
		Recipient: relayproto.MailboxID(rik),
		Size:      req.Size,
		Chunks:    req.Chunks,
		ExpiresAt: s.cfg.Clock.Now().Add(lim.BlobTTL),
	}
	if err := s.be.CreateBlob(r.Context(), rec); err != nil {
		httpError(w, http.StatusInternalServerError, relayproto.CodeInternal, "blob store unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(relayproto.BlobCreateResponse{BlobID: rec.ID})
}

func (s *Server) handleChunkPut(w http.ResponseWriter, r *http.Request) {
	if !s.admit(w, r) {
		return
	}
	body, ok := readBody(w, r, int64(s.cfg.Limits.MaxChunk))
	if !ok {
		return
	}
	who, ok := s.caller(w, r, body)
	if !ok {
		return
	}
	b, ok := s.blob(w, r)
	if !ok {
		return
	}
	if who != b.Uploader {
		httpError(w, http.StatusForbidden, relayproto.CodeForbidden, "only the uploader may write")
		return
	}
	n, ok := chunkIndex(w, r, b)
	if !ok {
		return
	}
	maxTotal := b.Size + int64(b.Chunks)*relayproto.ChunkOverhead
	err := s.be.PutChunk(r.Context(), b.ID, n, body, maxTotal)
	switch {
	case errors.Is(err, ErrTooLarge):
		httpError(w, http.StatusRequestEntityTooLarge, relayproto.CodeTooLarge, "chunks exceed the declared size")
	case errors.Is(err, ErrNotFound):
		httpError(w, http.StatusNotFound, relayproto.CodeNotFound, "no such blob")
	case err != nil:
		httpError(w, http.StatusInternalServerError, relayproto.CodeInternal, "blob store unavailable")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleChunkGet(w http.ResponseWriter, r *http.Request) {
	if !s.admit(w, r) {
		return
	}
	who, ok := s.caller(w, r, nil)
	if !ok {
		return
	}
	b, ok := s.blob(w, r)
	if !ok {
		return
	}
	if who != b.Recipient {
		httpError(w, http.StatusForbidden, relayproto.CodeForbidden, "only the recipient may read")
		return
	}
	n, ok := chunkIndex(w, r, b)
	if !ok {
		return
	}
	data, err := s.be.GetChunk(r.Context(), b.ID, n)
	if errors.Is(err, ErrNotFound) {
		httpError(w, http.StatusNotFound, relayproto.CodeNotFound, "chunk not uploaded")
		return
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, relayproto.CodeInternal, "blob store unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func (s *Server) handleBlobDelete(w http.ResponseWriter, r *http.Request) {
	if !s.admit(w, r) {
		return
	}
	who, ok := s.caller(w, r, nil)
	if !ok {
		return
	}
	b, ok := s.blob(w, r)
	if !ok {
		return
	}
	if who != b.Uploader && who != b.Recipient {
		httpError(w, http.StatusForbidden, relayproto.CodeForbidden, "not a party to this blob")
		return
	}
	if err := s.be.DeleteBlob(r.Context(), b.ID); err != nil {
		httpError(w, http.StatusInternalServerError, relayproto.CodeInternal, "blob store unavailable")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
