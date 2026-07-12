package blob

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	s, err := New(filepath.Join(root, "blobs"), filepath.Join(root, "tmp"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestPutRoundTripAndPlaintextHash(t *testing.T) {
	s := newStore(t)
	data := []byte("hello, spor\n")

	hash, err := s.Put(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// The identifier is SHA-256 over the *plaintext*, not the compressed bytes.
	sum := sha256.Sum256(data)
	if hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("hash = %s, want plaintext sha256 %s", hash, hex.EncodeToString(sum[:]))
	}

	// Round-trip: Open returns the original bytes.
	rc, err := s.Open(hash)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, data)
	}
}

func TestPutDedup(t *testing.T) {
	s := newStore(t)
	data := []byte("same content")

	h1, err := s.Put(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Put #1: %v", err)
	}
	h2, err := s.Put(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Put #2: %v", err)
	}
	if h1 != h2 {
		t.Fatalf("identical content produced different hashes: %s != %s", h1, h2)
	}

	// Only one object on disk.
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 {
		t.Fatalf("expected 1 blob after dedup, got %d", len(ents))
	}
	if !s.Has(h1) {
		t.Fatalf("Has(%s) = false, want true", h1)
	}
}

func TestPutLeavesNoTempFiles(t *testing.T) {
	s := newStore(t)
	if _, err := s.Put(bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(s.tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 0 {
		t.Fatalf("expected temp dir empty after Put, got %d entries", len(ents))
	}
}
