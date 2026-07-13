package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/emprcl/spor/internal/db/gen"
	"github.com/emprcl/spor/internal/lock"
	"github.com/emprcl/spor/internal/walk"
)

// SnapshotOptions configures a snapshot.
type SnapshotOptions struct {
	Label string // optional human name for the created state
}

// SnapshotResult reports the outcome. When Created is false the working tree
// matched HEAD and no state was recorded (no-op suppression). Warnings are
// non-fatal problems, files skipped or carried over unread (docs/SPEC.md §4),
// for the front-end to surface.
type SnapshotResult struct {
	Created  bool
	StateID  string
	Warnings []string
}

// Snapshot records the current working tree as a new state, per docs/SPEC.md §4.
// It walks the tree, stores any new blobs, and, unless the resulting manifest
// matches HEAD (no-op suppression), creates a state under HEAD and advances
// HEAD, all under the write lock. Blobs are written before the state is
// committed, so an incomplete state is never visible.
func (e *Engine) Snapshot(ctx context.Context, opts SnapshotOptions) (SnapshotResult, error) {
	wl, err := lock.AcquireWrite(ctx, e.writeLockPath())
	if err != nil {
		return SnapshotResult{}, err
	}
	defer func() { _ = wl.Release() }()

	// Read HEAD up front: it is the parent of any new state, the base for no-op
	// suppression, and (on Windows) the source of inherited exec bits. The write
	// lock keeps it stable for the rest of the operation.
	head, err := e.q.GetHead(ctx)
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("reading HEAD: %w", err)
	}

	// Walk → store blobs → build the manifest (in sorted path order).
	files, warnings, err := walk.Walk(e.root)
	if err != nil {
		return SnapshotResult{}, err
	}

	// HEAD's manifest, loaded lazily: only needed to carry over the entry of a
	// file that exists but cannot be read (docs/SPEC.md §4).
	var headManifest map[string]manifestEntry
	loadHeadManifest := func() error {
		if headManifest != nil || !head.Valid {
			return nil
		}
		rows, err := e.q.ListManifestEntries(ctx, head.String)
		if err != nil {
			return fmt.Errorf("reading HEAD manifest: %w", err)
		}
		headManifest = make(map[string]manifestEntry, len(rows))
		for _, r := range rows {
			headManifest[r.Path] = manifestEntry{path: r.Path, hash: r.BlobHash, exec: r.Executable != 0}
		}
		return nil
	}

	entries := make([]manifestEntry, 0, len(files))
	for _, f := range files {
		hash, storeErr := e.storeFile(f.Abs)
		switch {
		case storeErr == nil:
			entries = append(entries, manifestEntry{path: f.Rel, hash: hash, exec: f.Exec})
		case errors.Is(storeErr, fs.ErrNotExist):
			// Vanished since the walk (editor atomic saves): recorded as deleted.
		default:
			// Present but unreadable: inherit HEAD's entry so the file is not
			// spuriously recorded as a deletion; new files are skipped.
			if err := loadHeadManifest(); err != nil {
				return SnapshotResult{}, err
			}
			if prev, ok := headManifest[f.Rel]; ok {
				entries = append(entries, prev)
				warnings = append(warnings, fmt.Sprintf("%s: unreadable, kept previous version: %v", f.Rel, storeErr))
			} else {
				warnings = append(warnings, fmt.Sprintf("%s: unreadable, skipped: %v", f.Rel, storeErr))
			}
		}
	}
	// On platforms that cannot observe the execute bit, inherit it from HEAD so a
	// snapshot there does not flip inherited bits back off (docs/SPEC.md §4).
	if err := e.resolveExec(ctx, head, entries); err != nil {
		return SnapshotResult{}, err
	}
	manifestHash := hashManifest(entries)

	// No-op suppression: compare against HEAD's manifest hash.
	if head.Valid {
		prev, err := e.q.GetStateManifestHash(ctx, head.String)
		if err != nil {
			return SnapshotResult{}, fmt.Errorf("reading HEAD manifest: %w", err)
		}
		if prev == manifestHash {
			return SnapshotResult{Created: false, Warnings: warnings}, nil
		}
	}

	id := ulid.Make().String()
	if err := e.commitState(ctx, id, head, manifestHash, opts.Label, entries); err != nil {
		return SnapshotResult{}, err
	}
	return SnapshotResult{Created: true, StateID: id, Warnings: warnings}, nil
}

// commitState inserts the state row, its manifest entries, and advances HEAD in a
// single transaction. All referenced blobs are already written and verified.
func (e *Engine) commitState(
	ctx context.Context,
	id string,
	parent sql.NullString,
	manifestHash, label string,
	entries []manifestEntry,
) error {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	q := e.q.WithTx(tx)
	if err := q.CreateState(ctx, gen.CreateStateParams{
		ID:           id,
		CreatedAt:    time.Now().UnixMilli(),
		ParentID:     parent,
		ManifestHash: manifestHash,
		Label:        nullString(label),
	}); err != nil {
		return fmt.Errorf("creating state: %w", err)
	}
	for _, ent := range entries {
		if err := q.AddManifestEntry(ctx, gen.AddManifestEntryParams{
			StateID:    id,
			Path:       ent.path,
			BlobHash:   ent.hash,
			Executable: boolToInt(ent.exec),
		}); err != nil {
			return fmt.Errorf("adding manifest entry %s: %w", ent.path, err)
		}
	}
	if err := q.SetHead(ctx, sql.NullString{String: id, Valid: true}); err != nil {
		return fmt.Errorf("advancing HEAD: %w", err)
	}
	return tx.Commit()
}

// storeFile streams a file into the blob store and returns its content hash.
// PutFile writes nothing for content the store already holds.
func (e *Engine) storeFile(abs string) (string, error) {
	f, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return e.blobs.PutFile(f)
}

// manifestEntry is one path→(blob-hash, exec-bit) row, held in sorted path order.
type manifestEntry struct {
	path string
	hash string
	exec bool
}

// hashManifest computes the canonical manifest hash: SHA-256 over
// "path\0hash\0mode\n" for each entry, in the order given (walk returns sorted
// paths). mode is the execute bit ("1" or "0"), so a bare chmod +x, with no
// content change, still produces a distinct state. See docs/SPEC.md §2.
func hashManifest(entries []manifestEntry) string {
	h := sha256.New()
	for _, e := range entries {
		h.Write([]byte(e.path))
		h.Write([]byte{0})
		h.Write([]byte(e.hash))
		h.Write([]byte{0})
		h.Write([]byte{'0' + byte(boolToInt(e.exec))})
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// boolToInt maps the execute bit to its stored/hashed integer form.
func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// nullString wraps a possibly-empty label for storage.
func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
