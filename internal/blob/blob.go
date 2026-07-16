// Package blob is a content-addressed, zstd-compressed object store on disk.
// Blob identity is SHA-256 over the *plaintext*; compression is a storage detail
// that never affects the hash. See docs/design-spec.md §3.
package blob

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"
)

// compressionLevel is deliberately low: snapshots sit on the hot path and must
// feel instant, and zstd decompression is fast regardless of level so restore
// stays quick. See docs/design-spec.md §3.
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

// path returns the on-disk object path for a blob hash, fanned out git-style
// under a directory named by the hash's first two hex chars (docs/design-spec.md §3),
// so no single directory accumulates an unbounded number of entries.
func (s *Store) path(hash string) string {
	return filepath.Join(s.dir, hash[:2], hash[2:])
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
	return s.put(r, false)
}

// put is Put's body, optionally in batch mode. Outside a batch it is durable on
// return: the temp file is fsynced before the rename and the containing
// directory is fsynced after, per docs/design-spec.md §8. In batch mode on a
// platform with syncfs (see batchUsesSyncFS) both fsyncs are skipped, because
// Batch.Flush issues a single whole-store sync that makes every blob's content
// and rename durable at once, before the caller commits the referencing state.
// The §8 invariant, no committed state ever references a non-durable blob, holds
// either way; batching only defers the flush, it never removes it.
func (s *Store) put(r io.Reader, batched bool) (hash string, err error) {
	deferSync := batched && batchUsesSyncFS
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
	// Flush the blob's content before it is installed. In batch mode the single
	// syncfs at Flush covers this, so skip the per-blob fsync (the whole point of
	// the batch: thousands of first-snap fsyncs collapse into one).
	if !deferSync {
		if err = tmp.Sync(); err != nil {
			tmp.Close()
			return "", err
		}
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

	target := s.path(hash)
	fanDir := filepath.Dir(target)
	fanDirCreated := false
	switch mkErr := os.Mkdir(fanDir, 0o755); {
	case mkErr == nil:
		fanDirCreated = true
	case !errors.Is(mkErr, fs.ErrExist):
		return "", fmt.Errorf("creating blob directory %s: %w", fanDir, mkErr)
	}
	if err = os.Rename(tmpName, target); err != nil {
		return "", fmt.Errorf("installing blob %s: %w", hash, err)
	}
	// Persist the rename itself: fsync the fan-out directory, and the store root
	// too when the fan-out directory is new, before the caller commits a state
	// that references this blob (docs/design-spec.md §8). No-op on Windows. In
	// batch mode the syncfs at Flush persists every rename at once, so skip it.
	if deferSync {
		return hash, nil
	}
	if err = syncDir(fanDir); err != nil {
		return "", fmt.Errorf("syncing blob directory %s: %w", fanDir, err)
	}
	if fanDirCreated {
		if err = syncDir(s.dir); err != nil {
			return "", fmt.Errorf("syncing blob store root: %w", err)
		}
	}
	return hash, nil
}

// Batch stores blobs with their durability fsyncs batched into a single
// whole-store sync at Flush, instead of one fsync (plus a directory fsync) per
// blob. This is the decisive win for a first snapshot, where every file is a
// miss: thousands of per-blob fsyncs, which dominate the wall clock, collapse
// into one syncfs. It relies on a platform whole-filesystem sync
// (batchUsesSyncFS); where none exists, Put falls back to per-blob fsyncs, so a
// Batch is always correct, just not always faster. A Batch is used for a single
// snapshot and MUST be Flushed before the state referencing its blobs is
// committed (docs/design-spec.md §8). Its methods are safe to call concurrently
// (the underlying store writes are).
type Batch struct {
	s *Store
}

// NewBatch begins a batch over the store.
func (s *Store) NewBatch() *Batch { return &Batch{s: s} }

// Put stores r through the batch (single pass: compress and hash together).
func (b *Batch) Put(r io.Reader) (string, error) { return b.s.put(r, true) }

// PutFile stores an on-disk file through the batch, dedup-skipping the write
// when the content is already stored, exactly like Store.PutFile.
func (b *Batch) PutFile(f *os.File) (string, error) { return b.s.putFile(f, true) }

// Flush makes every blob the batch stored durable, content and rename alike,
// with a single whole-store sync (syncfs on Linux). Where the batch already
// fsynced each blob inline (no syncfs available), it is a no-op. Call it before
// committing any state that references the batch's blobs.
func (b *Batch) Flush() error {
	if !batchUsesSyncFS {
		return nil // blobs were fsynced per-blob in put; nothing deferred
	}
	return syncFS(b.s.dir)
}

// PutFile stores an on-disk file, skipping all writes when its content is
// already stored: a first pass streams the plaintext through SHA-256 only, and
// the compress-and-install path (Put) runs solely on a miss. Unchanged files,
// the overwhelming majority of every snapshot, therefore cost one read and zero
// writes. The install happens under the hash Put recomputes on its own pass, so
// a file mutating between the two passes still stores whatever was read, never
// a wrong hash; the next snapshot reconciles (walk is the source of truth).
func (s *Store) PutFile(f *os.File) (string, error) {
	return s.putFile(f, false)
}

// putFile is PutFile's body, optionally in batch mode (see put). On a cold store
// (a first snapshot), where the pre-hash pass can never hit, prefer
// Put/Batch.Put, which reads the file once instead of twice.
func (s *Store) putFile(f *os.File, batched bool) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	hash := hex.EncodeToString(h.Sum(nil))
	if s.Has(hash) {
		return hash, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return s.put(f, batched)
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

// List returns the hash of every blob in the store, reconstructed from the
// git-style fanout (a two-char directory plus the remaining filename). It is the
// disk side of GC's mark-sweep (docs/design-spec.md §8). A store with no blobs directory
// yet returns no hashes.
func (s *Store) List() ([]string, error) {
	fans, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var hashes []string
	for _, fan := range fans {
		if !fan.IsDir() || len(fan.Name()) != 2 {
			continue
		}
		objs, err := os.ReadDir(filepath.Join(s.dir, fan.Name()))
		if err != nil {
			return nil, err
		}
		for _, obj := range objs {
			if !obj.IsDir() {
				hashes = append(hashes, fan.Name()+obj.Name())
			}
		}
	}
	return hashes, nil
}

// Size returns the on-disk (compressed) size of a blob.
func (s *Store) Size(hash string) (int64, error) {
	info, err := os.Stat(s.path(hash))
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// Remove deletes a blob and best-effort prunes its now-possibly-empty fanout
// directory. A missing blob is not an error, so GC's sweep is idempotent.
func (s *Store) Remove(hash string) error {
	if err := os.Remove(s.path(hash)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	_ = os.Remove(filepath.Dir(s.path(hash))) // only succeeds once the dir is empty
	return nil
}

// Verify reports whether a stored blob's decompressed content still hashes to its
// name. It returns ErrNotFound when the blob is absent; an unreadable or
// undecodable blob (corruption) is reported as ok == false, not an error, so a
// caller can treat it as a corrupt object rather than aborting.
func (s *Store) Verify(hash string) (ok bool, err error) {
	r, err := s.Open(hash)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, ErrNotFound
		}
		return false, err
	}
	defer r.Close()
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return false, nil // undecodable content: corrupt, not a store failure
	}
	return hex.EncodeToString(h.Sum(nil)) == hash, nil
}

// ErrNotFound is returned when a blob is missing from the store.
var ErrNotFound = errors.New("blob not found")
