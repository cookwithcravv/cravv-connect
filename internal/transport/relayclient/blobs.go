package relayclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/relayproto"
	"github.com/cravv/cravv-connect/internal/transport"
)

type blobs struct {
	c *Client
	s transport.Signer
}

// do sends a signed request and returns the status and at most limit bytes of body.
func (b blobs) do(ctx context.Context, method, path string, body []byte, limit int64) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, b.c.origin+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	relayproto.SignRequest(req, b.c.origin, b.s.Sign, b.s.Public(), b.c.clock.Now().Unix(), body)
	resp, err := b.c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("relay: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return 0, nil, fmt.Errorf("relay: read body: %w", err)
	}
	return resp.StatusCode, out, nil
}

func chunkPath(id string, n uint32) string {
	return relayproto.PathBlobs + "/" + url.PathEscape(id) + "/chunks/" + strconv.FormatUint(uint64(n), 10)
}

func (b blobs) Create(ctx context.Context, recipient ed25519.PublicKey, size int64, chunks uint32) (string, error) {
	body, err := json.Marshal(relayproto.BlobCreateRequest{Size: size, Chunks: chunks, Recipient: relayproto.B64(recipient)})
	if err != nil {
		return "", err
	}
	code, out, err := b.do(ctx, http.MethodPost, relayproto.PathBlobs, body, 4<<10)
	if err != nil {
		return "", err
	}
	if code != http.StatusCreated {
		return "", newHTTPError(code, out)
	}
	var r relayproto.BlobCreateResponse
	if err := json.Unmarshal(out, &r); err != nil || r.BlobID == "" {
		return "", fmt.Errorf("%w: bad blob create response", ErrProtocol)
	}
	return r.BlobID, nil
}

func (b blobs) PutChunk(ctx context.Context, blobID string, n uint32, data []byte) error {
	code, out, err := b.do(ctx, http.MethodPut, chunkPath(blobID, n), data, 4<<10)
	if err != nil {
		return err
	}
	if code != http.StatusNoContent {
		return newHTTPError(code, out)
	}
	return nil
}

func (b blobs) GetChunk(ctx context.Context, blobID string, n uint32) ([]byte, error) {
	limit := int64(core.FileChunkBytes + relayproto.ChunkOverhead)
	code, out, err := b.do(ctx, http.MethodGet, chunkPath(blobID, n), nil, limit+1)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, newHTTPError(code, out)
	}
	if int64(len(out)) > limit {
		return nil, fmt.Errorf("%w: chunk larger than %d bytes", ErrProtocol, limit)
	}
	return out, nil
}

func (b blobs) Delete(ctx context.Context, blobID string) error {
	code, out, err := b.do(ctx, http.MethodDelete, relayproto.PathBlobs+"/"+url.PathEscape(blobID), nil, 4<<10)
	if err != nil {
		return err
	}
	if code != http.StatusNoContent {
		return newHTTPError(code, out)
	}
	return nil
}
