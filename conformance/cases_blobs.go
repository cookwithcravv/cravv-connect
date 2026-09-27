package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
	"github.com/cookwithcravv/cravv-connect/internal/transport"
	"github.com/cookwithcravv/cravv-connect/internal/transport/relayclient"
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
		{"blobs/clock_skew_over_300s_401", func(t *testing.T, s *suite) {
			up, _ := s.member(t)
			rcpt, _ := s.member(t)
			body, _ := json.Marshal(relayproto.BlobCreateRequest{Size: 1, Chunks: 1, Recipient: relayproto.B64(rcpt.Public())})
			origin := s.client.Origin()
			// 330s leaves margin for clock drift between the suite and an external relay.
			for _, d := range []time.Duration{-330 * time.Second, 330 * time.Second} {
				if code := s.rawHTTP(t, up, origin, http.MethodPost, relayproto.PathBlobs, body, s.now().Add(d)); code != http.StatusUnauthorized {
					t.Fatalf("ts skewed by %v: %d, want 401", d, code)
				}
			}
			if code := s.rawHTTP(t, up, origin, http.MethodPost, relayproto.PathBlobs, body, s.now()); code != http.StatusCreated {
				t.Fatalf("unskewed control request: %d, want 201", code)
			}
		}},
		{"blobs/signature_bound_to_origin_401", func(t *testing.T, s *suite) {
			up, _ := s.member(t)
			rcpt, _ := s.member(t)
			body, _ := json.Marshal(relayproto.BlobCreateRequest{Size: 1, Chunks: 1, Recipient: relayproto.B64(rcpt.Public())})
			if code := s.rawHTTP(t, up, "https://other-relay.invalid", http.MethodPost, relayproto.PathBlobs, body, s.now()); code != http.StatusUnauthorized {
				t.Fatalf("signature for another origin: %d, want 401", code)
			}
		}},
		{"blobs/stored_bytes_capped_by_declared_size_413", func(t *testing.T, s *suite) {
			up, _ := s.member(t)
			rcpt, _ := s.member(t)
			b := s.client.Blobs(up)
			// size FileChunkBytes+1 in 2 chunks: stored bytes may total size + 2*64.
			id, err := b.Create(ctxT(t), rcpt.Public(), core.FileChunkBytes+1, 2)
			if err != nil {
				t.Fatal(err)
			}
			if err := b.PutChunk(ctxT(t), id, 0, make([]byte, core.FileChunkBytes+relayproto.ChunkOverhead)); err != nil {
				t.Fatalf("chunk 0 at the body limit: %v", err)
			}
			if err := b.PutChunk(ctxT(t), id, 1, make([]byte, relayproto.ChunkOverhead+2)); httpStatus(err) != http.StatusRequestEntityTooLarge {
				t.Fatalf("total one byte over size+64*chunks: %v, want 413", err)
			}
			if err := b.PutChunk(ctxT(t), id, 1, make([]byte, relayproto.ChunkOverhead+1)); err != nil {
				t.Fatalf("total exactly size+64*chunks: %v", err)
			}
		}},
		{"blobs/per_member_quota_413", func(t *testing.T, s *suite) {
			up, _ := s.member(t)
			other, _ := s.member(t)
			rcpt, _ := s.member(t)
			b := s.client.Blobs(up)
			var ids []string
			t.Cleanup(func() {
				for _, id := range ids {
					_ = b.Delete(context.Background(), id)
				}
			})
			// Blobs are only declared, never uploaded, so this is cheap even for a
			// multi-GiB quota. relay-v1 requires a quota of at least 1 GiB.
			const maxTries = 64 // 6.4 GiB
			for len(ids) < maxTries {
				id, err := b.Create(ctxT(t), rcpt.Public(), core.MaxFileBytes, core.MaxFileBytes/core.FileChunkBytes)
				if httpStatus(err) == http.StatusRequestEntityTooLarge {
					break
				}
				if err != nil {
					t.Fatalf("create %d: %v", len(ids)+1, err)
				}
				ids = append(ids, id)
			}
			if len(ids) == maxTries {
				t.Skipf("no quota hit after %d x 100 MiB; relay quota is larger than this case probes", maxTries)
			}
			if int64(len(ids))*core.MaxFileBytes < 1<<30-core.MaxFileBytes {
				t.Fatalf("quota hit after %d x 100 MiB; relay-v1 requires at least 1 GiB", len(ids))
			}
			id, err := s.client.Blobs(other).Create(ctxT(t), rcpt.Public(), core.MaxFileBytes, core.MaxFileBytes/core.FileChunkBytes)
			if err != nil {
				t.Fatalf("quota must be per member: %v", err)
			}
			_ = s.client.Blobs(other).Delete(ctxT(t), id)
			if err := b.Delete(ctxT(t), ids[0]); err != nil {
				t.Fatal(err)
			}
			ids = ids[1:]
			id, err = b.Create(ctxT(t), rcpt.Public(), core.MaxFileBytes, core.MaxFileBytes/core.FileChunkBytes)
			if err != nil {
				t.Fatalf("deleting a blob must free quota: %v", err)
			}
			ids = append(ids, id)
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
