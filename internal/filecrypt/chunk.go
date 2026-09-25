// Package filecrypt encrypts file chunks with XChaCha20-Poly1305.
package filecrypt

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/cravv/cravv-connect/internal/core"
	"golang.org/x/crypto/chacha20poly1305"
)

// KeySize is the per-file key length.
const KeySize = chacha20poly1305.KeySize

// Overhead is the ciphertext expansion per chunk (the Poly1305 tag).
const Overhead = chacha20poly1305.Overhead

// NewKey returns a random 32-byte file key.
func NewKey() ([]byte, error) {
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		return nil, fmt.Errorf("filecrypt: new key: %w", err)
	}
	return k, nil
}

// ChunkCount is ceil(size / core.FileChunkBytes), and 1 for an empty file.
func ChunkCount(size int64) uint32 {
	if size <= 0 {
		return 1
	}
	return uint32((size + core.FileChunkBytes - 1) / core.FileChunkBytes)
}

// nonce is SHA-256(fileID)[0:20] || uint32 big-endian index (24 bytes).
func nonce(fileID string, index uint32) []byte {
	sum := sha256.Sum256([]byte(fileID))
	n := make([]byte, chacha20poly1305.NonceSizeX)
	copy(n, sum[:20])
	binary.BigEndian.PutUint32(n[20:], index)
	return n
}

// aad is fileID || uint32 big-endian index || byte(last).
func aad(fileID string, index uint32, last bool) []byte {
	a := make([]byte, 0, len(fileID)+5)
	a = append(a, fileID...)
	a = binary.BigEndian.AppendUint32(a, index)
	if last {
		return append(a, 1)
	}
	return append(a, 0)
}

// EncryptChunk seals one plaintext chunk of at most core.FileChunkBytes.
func EncryptChunk(key []byte, fileID string, index uint32, last bool, pt []byte) ([]byte, error) {
	if len(pt) > core.FileChunkBytes {
		return nil, fmt.Errorf("filecrypt: chunk of %d bytes: %w", len(pt), core.ErrTooLarge)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("filecrypt: %w", err)
	}
	return aead.Seal(nil, nonce(fileID, index), pt, aad(fileID, index, last)), nil
}

// DecryptChunk opens one chunk. It fails if the chunk was sealed for another
// file, another index, or a different last flag (so truncation, reordering,
// and swapping are detected).
func DecryptChunk(key []byte, fileID string, index uint32, last bool, ct []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("filecrypt: %w", err)
	}
	pt, err := aead.Open(nil, nonce(fileID, index), ct, aad(fileID, index, last))
	if err != nil {
		return nil, fmt.Errorf("filecrypt: chunk %d of %s failed authentication: %w", index, fileID, err)
	}
	return pt, nil
}
