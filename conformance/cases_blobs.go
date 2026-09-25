package conformance

import (
	"errors"
	"net/http"
	"testing"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/relayproto"
	"github.com/cravv/cravv-connect/internal/transport"
	"github.com/cravv/cravv-connect/internal/transport/relayclient"
)

func blobCases() []testCase {
	return []testCase{
		{"blobs/acl", func(t *testing.T, s *suite) {
			up, _ := s.member(t)
			rcpt, _ := s.member(t)
			other, _ := s.member(t)
			ub, rb, ob := s.client.Blobs(up), s.client.Blobs(rcpt), s.client.Blobs(other)
			id, err := ub.Create(ctxT(t), rcpt.Public(), core.FileChunkBytes+4, 2)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if err := ub.PutChunk(ctxT(t), id, 0, []byte("abcd")); err != nil {
				t.Fatalf("uploader put: %v", err)
			}
			if err := ub.PutChunk(ctxT(t), id, 1, []byte("efgh")); err != nil {
				t.Fatalf("uploader put: %v", err)
			}
			if err := rb.PutChunk(ctxT(t), id, 0, []byte("zzzz")); httpStatus(err) != http.StatusForbidden {
				t.Fatalf("recipient put: %v, want 403", err)
			}
			if err := ob.PutChunk(ctxT(t), id, 0, []byte("zzzz")); httpStatus(err) != http.StatusForbidden {
				t.Fatalf("other put: %v, want 403", err)
			}
			if got, err := rb.GetChunk(ctxT(t), id, 0); err != nil || string(got) != "abcd" {
				t.Fatalf("recipient get: %q %v", got, err)
			}
			if _, err := ub.GetChunk(ctxT(t), id, 0); httpStatus(err) != http.StatusForbidden {
				t.Fatalf("uploader get: %v, want 403", err)
			}
			if _, err := ob.GetChunk(ctxT(t), id, 0); httpStatus(err) != http.StatusForbidden {
				t.Fatalf("other get: %v, want 403", err)
			}
			if err := ob.Delete(ctxT(t), id); httpStatus(err) != http.StatusForbidden {
				t.Fatalf("other delete: %v, want 403", err)
			}
			if _, err := rb.GetChunk(ctxT(t), "no-such-blob", 0); httpStatus(err) != http.StatusNotFound {
				t.Fatalf("unknown blob: %v, want 404", err)
			}
			if err := rb.Delete(ctxT(t), id); err != nil {
				t.Fatalf("recipient delete: %v", err)
			}
			if _, err := rb.GetChunk(ctxT(t), id, 0); httpStatus(err) != http.StatusNotFound {
				t.Fatalf("after delete: %v, want 404", err)
			}
		}},
		{"blobs/chunk_count_and_index_checked", func(t *testing.T, s *suite) {
			up, _ := s.member(t)
			rcpt, _ := s.member(t)
			b := s.client.Blobs(up)
			for _, bad := range []struct {
				size   int64
				chunks uint32
			}{{10, 0}, {10, 2}, {core.FileChunkBytes, 2}} {
				if _, err := b.Create(ctxT(t), rcpt.Public(), bad.size, bad.chunks); httpStatus(err) != http.StatusBadRequest {
					t.Fatalf("size %d chunks %d: %v, want 400", bad.size, bad.chunks, err)
				}
			}
			if _, err := b.Create(ctxT(t), rcpt.Public(), 0, 1); err != nil {
				t.Fatalf("empty blob: %v", err)
			}
			id, err := b.Create(ctxT(t), rcpt.Public(), core.FileChunkBytes+1, 2)
			if err != nil {
				t.Fatal(err)
			}
			if err := b.PutChunk(ctxT(t), id, 2, []byte("x")); httpStatus(err) != http.StatusBadRequest {
				t.Fatalf("chunk index == chunks: %v, want 400", err)
			}
		}},
		{"blobs/error_body_is_json", func(t *testing.T, s *suite) {
			rcpt, _ := s.member(t)
			_, err := s.client.Blobs(s.tg.NewIdentity()).Create(ctxT(t), rcpt.Public(), 1, 1)
			var he *relayclient.HTTPError
			if !errors.As(err, &he) || he.Code != relayproto.CodeForbidden {
				t.Fatalf("err = %v, want JSON body with code forbidden", err)
			}
		}},
		{"blobs/uploader_may_delete", func(t *testing.T, s *suite) {
			up, _ := s.member(t)
			rcpt, _ := s.member(t)
			id, err := s.client.Blobs(up).Create(ctxT(t), rcpt.Public(), 1, 1)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.client.Blobs(up).Delete(ctxT(t), id); err != nil {
				t.Fatalf("uploader delete: %v", err)
			}
		}},
		{"blobs/non_member_forbidden", func(t *testing.T, s *suite) {
			rcpt, _ := s.member(t)
			_, err := s.client.Blobs(s.tg.NewIdentity()).Create(ctxT(t), rcpt.Public(), 1, 1)
			if httpStatus(err) != http.StatusForbidden {
				t.Fatalf("err = %v, want 403", err)
			}
		}},
		{"blobs/bad_signature_401", func(t *testing.T, s *suite) {
			up, _ := s.member(t)
			rcpt, _ := s.member(t)
			_, err := s.client.Blobs(badSigner{up}).Create(ctxT(t), rcpt.Public(), 1, 1)
			if httpStatus(err) != http.StatusUnauthorized {
				t.Fatalf("err = %v, want 401", err)
			}
		}},
		{"blobs/too_large_413", func(t *testing.T, s *suite) {
			up, _ := s.member(t)
			rcpt, _ := s.member(t)
			b := s.client.Blobs(up)
			if _, err := b.Create(ctxT(t), rcpt.Public(), core.MaxFileBytes+1, 101); httpStatus(err) != http.StatusRequestEntityTooLarge || !errors.Is(err, core.ErrTooLarge) {
				t.Fatalf("oversize blob: %v, want 413", err)
			}
			id, err := b.Create(ctxT(t), rcpt.Public(), core.FileChunkBytes, 1)
			if err != nil {
				t.Fatal(err)
			}
			if err := b.PutChunk(ctxT(t), id, 0, make([]byte, core.FileChunkBytes+relayproto.ChunkOverhead+1)); httpStatus(err) != http.StatusRequestEntityTooLarge {
				t.Fatalf("oversize chunk: %v, want 413", err)
			}
			if err := b.PutChunk(ctxT(t), id, 0, make([]byte, core.FileChunkBytes+relayproto.ChunkOverhead)); err != nil {
				t.Fatalf("max chunk: %v", err)
			}
		}},
		{"blobs/forbidden_maps_to_sentinel", func(t *testing.T, s *suite) {
			up, _ := s.member(t)
			rcpt, _ := s.member(t)
			id, err := s.client.Blobs(up).Create(ctxT(t), rcpt.Public(), 1, 1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.client.Blobs(up).GetChunk(ctxT(t), id, 0); !errors.Is(err, transport.ErrRelayForbidden) {
				t.Fatalf("err = %v, want ErrRelayForbidden", err)
			}
		}},
	}
}
