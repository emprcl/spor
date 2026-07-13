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

	// Fan-out layout: the object lives under blobs/<first 2 hex chars>/<rest>.
	if _, err := os.Stat(filepath.Join(s.dir, hash[:2], hash[2:])); err != nil {
		t.Fatalf("blob not at fan-out path: %v", err)
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
	if n := countObjects(t, s); n != 1 {
		t.Fatalf("expected 1 blob after dedup, got %d", n)
	}
	if !s.Has(h1) {
		t.Fatalf("Has(%s) = false, want true", h1)
	}
}

// countObjects counts blob files across the fan-out directories.
func countObjects(t *testing.T, s *Store) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(s.dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPutFileDedupSkipsWrites(t *testing.T) {
	s := newStore(t)
	data := []byte("file content")
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	open := func() *os.File {
		t.Helper()
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}

	f1 := open()
	h1, err := s.PutFile(f1)
	f1.Close()
	if err != nil {
		t.Fatalf("PutFile #1: %v", err)
	}
	sum := sha256.Sum256(data)
	if h1 != hex.EncodeToString(sum[:]) {
		t.Fatalf("hash = %s, want plaintext sha256", h1)
	}

	// Second PutFile of identical content must dedup, write no temp files, and
	// not disturb the stored object.
	before, err := os.Stat(s.path(h1))
	if err != nil {
		t.Fatal(err)
	}
	f2 := open()
	h2, err := s.PutFile(f2)
	f2.Close()
	if err != nil {
		t.Fatalf("PutFile #2: %v", err)
	}
	if h2 != h1 {
		t.Fatalf("hashes differ: %s != %s", h1, h2)
	}
	after, err := os.Stat(s.path(h1))
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatal("stored object was rewritten on a dedup hit")
	}
	if ents, err := os.ReadDir(s.tmp); err != nil || len(ents) != 0 {
		t.Fatalf("temp dir not empty after dedup hit (err=%v, entries=%d)", err, len(ents))
	}
	if n := countObjects(t, s); n != 1 {
		t.Fatalf("expected 1 blob, got %d", n)
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
