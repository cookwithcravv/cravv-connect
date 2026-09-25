package filecrypt

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/cravv/cravv-connect/internal/core"
)

func TestChunkCount(t *testing.T) {
	tests := []struct {
		size int64
		want uint32
	}{
		{0, 1},
		{1, 1},
		{core.FileChunkBytes - 1, 1},
		{core.FileChunkBytes, 1},
		{core.FileChunkBytes + 1, 2},
		{3 * core.FileChunkBytes, 3},
		{core.MaxFileBytes, 100},
	}
	for _, tt := range tests {
		if got := ChunkCount(tt.size); got != tt.want {
			t.Errorf("ChunkCount(%d) = %d, want %d", tt.size, got, tt.want)
		}
	}
}

func TestNewKey(t *testing.T) {
	a, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewKey()
	if len(a) != 32 || bytes.Equal(a, b) {
		t.Fatal("keys must be 32 random bytes")
	}
}

// encryptFile splits data into chunks and encrypts each one.
func encryptFile(t *testing.T, key []byte, fileID string, data []byte) [][]byte {
	t.Helper()
	n := ChunkCount(int64(len(data)))
	var out [][]byte
	for i := uint32(0); i < n; i++ {
		start := int(i) * core.FileChunkBytes
		end := min(start+core.FileChunkBytes, len(data))
		ct, err := EncryptChunk(key, fileID, i, i == n-1, data[start:end])
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, ct)
	}
	return out
}

func TestRoundTripMultiChunk(t *testing.T) {
	key, _ := NewKey()
	data := make([]byte, 2*core.FileChunkBytes+123)
	rand.Read(data)
	cts := encryptFile(t, key, "file-1", data)
	if len(cts) != 3 {
		t.Fatalf("got %d chunks, want 3", len(cts))
	}
	var back []byte
	for i, ct := range cts {
		if len(ct) > core.FileChunkBytes+Overhead {
			t.Fatalf("chunk %d is %d bytes, above chunk size plus overhead", i, len(ct))
		}
		pt, err := DecryptChunk(key, "file-1", uint32(i), i == len(cts)-1, ct)
		if err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
		back = append(back, pt...)
	}
	if !bytes.Equal(back, data) {
		t.Fatal("decrypted file differs")
	}
}

func TestRoundTripEmptyFile(t *testing.T) {
	key, _ := NewKey()
	ct, err := EncryptChunk(key, "empty", 0, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := DecryptChunk(key, "empty", 0, true, ct)
	if err != nil || len(pt) != 0 {
		t.Fatalf("empty chunk: %q, %v", pt, err)
	}
}

func TestTamperDetected(t *testing.T) {
	key, _ := NewKey()
	otherKey, _ := NewKey()
	data := make([]byte, 2*core.FileChunkBytes+10)
	rand.Read(data)
	cts := encryptFile(t, key, "file-A", data)
	// Chunk 0 of a different file under the same key, for cross-file swaps.
	foreign := encryptFile(t, key, "file-B", data)

	tests := []struct {
		name  string
		key   []byte
		file  string
		index uint32
		last  bool
		ct    []byte
	}{
		{"reorder: chunk 1 presented as 0", key, "file-A", 0, false, cts[1]},
		{"reorder: chunk 0 presented as 1", key, "file-A", 1, false, cts[0]},
		{"truncation: middle chunk claimed last", key, "file-A", 1, true, cts[1]},
		{"extension: last chunk claimed not last", key, "file-A", 2, false, cts[2]},
		{"swap from another file", key, "file-A", 0, false, foreign[0]},
		{"wrong file id", key, "file-B", 0, false, cts[0]},
		{"wrong key", otherKey, "file-A", 0, false, cts[0]},
		{"flipped bit", key, "file-A", 0, false, flip(cts[0])},
		{"cut short", key, "file-A", 0, false, cts[0][:len(cts[0])-1]},
	}
	for _, tt := range tests {
		if _, err := DecryptChunk(tt.key, tt.file, tt.index, tt.last, tt.ct); err == nil {
			t.Errorf("%s: decrypt succeeded", tt.name)
		}
	}
}

func TestLimitsAndBadKeys(t *testing.T) {
	key, _ := NewKey()
	if _, err := EncryptChunk(key, "f", 0, true, make([]byte, core.FileChunkBytes+1)); !errors.Is(err, core.ErrTooLarge) {
		t.Fatalf("oversized chunk: %v, want ErrTooLarge", err)
	}
	if _, err := EncryptChunk(key[:16], "f", 0, true, []byte("x")); err == nil {
		t.Fatal("short key accepted for encrypt")
	}
	if _, err := DecryptChunk(key[:16], "f", 0, true, []byte("x")); err == nil {
		t.Fatal("short key accepted for decrypt")
	}
}

func TestNonceAndAADLayout(t *testing.T) {
	n := nonce("abc", 0x01020304)
	if len(n) != 24 || !bytes.Equal(n[20:], []byte{1, 2, 3, 4}) {
		t.Fatalf("nonce = %x", n)
	}
	if got := aad("abc", 7, true); !bytes.Equal(got, []byte{'a', 'b', 'c', 0, 0, 0, 7, 1}) {
		t.Fatalf("aad = %x", got)
	}
	if got := aad("abc", 7, false); got[len(got)-1] != 0 {
		t.Fatalf("aad last flag = %x", got)
	}
}

func flip(b []byte) []byte {
	c := append([]byte(nil), b...)
	c[len(c)/2] ^= 0x80
	return c
}
