// Package blob is a content-addressed, zstd-compressed object store on disk.
// Blob identity is SHA-256 over the *plaintext*; compression is a storage detail
// that never affects the hash. See docs/SPEC.md §3.
package blob

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"
)

// compressionLevel is deliberately low: snapshots sit on the hot path and must
// feel instant, and zstd decompression is fast regardless of level so restore
// stays quick. See docs/SPEC.md §3.
const compressionLevel = zstd.SpeedDefault // level ~3

// Store writes and reads blobs under a single directory.
type Store struct {
	dir string // holds <sha256> object files
	tmp string // staging dir on the same filesystem, for temp→rename
}

// New returns a Store rooted at dir, creating dir and its temp staging area.
func New(dir, tmp string) (*Store, error) {
	for _, d := range []string{dir, tmp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	return &Store{dir: dir, tmp: tmp}, nil
}

// path returns the on-disk object path for a blob hash.
func (s *Store) path(hash string) string {
	return filepath.Join(s.dir, hash)
}

// Has reports whether a blob with the given hash already exists.
func (s *Store) Has(hash string) bool {
	_, err := os.Stat(s.path(hash))
	return err == nil
}

// Put streams r, computing SHA-256 over the plaintext while writing a
// zstd-compressed copy to a temp file, then atomically installs it at the
// content-addressed path and returns the hash. If the blob already exists it is
// not rewritten (dedup). The write is durable: fsync before rename.
func (s *Store) Put(r io.Reader) (hash string, err error) {
	tmp, err := os.CreateTemp(s.tmp, "blob-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we don't successfully install the blob.
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()

	h := sha256.New()
	enc, err := zstd.NewWriter(tmp, zstd.WithEncoderLevel(compressionLevel))
	if err != nil {
		tmp.Close()
		return "", err
	}

	// Tee the plaintext into the hasher as it is compressed into the temp file.
	if _, err = io.Copy(enc, io.TeeReader(r, h)); err != nil {
		enc.Close()
		tmp.Close()
		return "", err
	}
	if err = enc.Close(); err != nil {
		tmp.Close()
		return "", err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err = tmp.Close(); err != nil {
		return "", err
	}

	hash = hex.EncodeToString(h.Sum(nil))

	// Already stored: identical content → identical hash → nothing to do.
	if s.Has(hash) {
		_ = os.Remove(tmpName)
		return hash, nil
	}

	if err = os.Rename(tmpName, s.path(hash)); err != nil {
		return "", fmt.Errorf("installing blob %s: %w", hash, err)
	}
	return hash, nil
}

// Open returns a reader over the decompressed contents of a blob.
func (s *Store) Open(hash string) (io.ReadCloser, error) {
	f, err := os.Open(s.path(hash))
	if err != nil {
		return nil, err
	}
	dec, err := zstd.NewReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &blobReader{f: f, dec: dec}, nil
}

// blobReader couples the zstd decoder with its underlying file so both close.
type blobReader struct {
	f   *os.File
	dec *zstd.Decoder
}

func (b *blobReader) Read(p []byte) (int, error) { return b.dec.Read(p) }

func (b *blobReader) Close() error {
	b.dec.Close()
	return b.f.Close()
}

// ErrNotFound is returned when a blob is missing from the store.
var ErrNotFound = errors.New("blob not found")
