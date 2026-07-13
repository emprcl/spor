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
// matched HEAD and no state was recorded (no-op suppression).
type SnapshotResult struct {
	Created bool
	StateID string
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
	return e.snapshotLocked(ctx, opts)
}

// snapshotLocked is Snapshot's body, assuming the caller already holds the write
// lock. Restore force-settles (docs/SPEC.md §5) by calling this under the single
// lock it holds for the whole operation, so the pre-restore snapshot and the
// materialization can never interleave with another front-end.
func (e *Engine) snapshotLocked(ctx context.Context, opts SnapshotOptions) (SnapshotResult, error) {
	// Read HEAD up front: it is the parent of any new state, the base for no-op
	// suppression, and (on Windows) the source of inherited exec bits. The write
	// lock keeps it stable for the rest of the operation.
	head, err := e.q.GetHead(ctx)
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("reading HEAD: %w", err)
	}

	// Labels are unique aliases (docs/SPEC.md §2), so reject a taken one up front,
	// before doing the walk, rather than failing on the insert.
	if opts.Label != "" {
		if owner, err := e.labelOwner(ctx, opts.Label); err != nil {
			return SnapshotResult{}, err
		} else if owner != "" {
			return SnapshotResult{}, fmt.Errorf("label %q is already used by state %s", opts.Label, owner)
		}
	}

	// Walk → store blobs → build the manifest (in sorted path order).
	files, err := walk.Walk(e.root)
	if err != nil {
		return SnapshotResult{}, err
	}

	// The stat cache (docs/SPEC.md §4) elides re-reading unchanged files. A row
	// is trusted only when size, mtime, and inode all match, the mtime is
	// strictly older than the row's recording time (the racily-clean rule), and
	// the blob it names is actually on disk. snapStart is taken before any file
	// is read, so content mutating mid-snapshot always lands at or after it and
	// the affected row is distrusted next time.
	cacheRows, err := e.q.ListStatCache(ctx)
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("reading stat cache: %w", err)
	}
	cache := make(map[string]gen.StatCache, len(cacheRows))
	for _, r := range cacheRows {
		cache[r.Path] = r
	}
	snapStart := time.Now().UnixNano()
	var upserts []gen.UpsertStatCacheEntryParams
	walked := make(map[string]bool, len(files))

	entries := make([]manifestEntry, 0, len(files))
	for _, f := range files {
		walked[f.Rel] = true
		if row, ok := cache[f.Rel]; ok &&
			row.Size == f.Size && row.MtimeNs == f.MtimeNs && row.Inode == int64(f.Inode) &&
			f.MtimeNs < row.RecordedAt &&
			e.blobs.Has(row.BlobHash) {
			entries = append(entries, manifestEntry{path: f.Rel, hash: row.BlobHash, exec: f.Exec})
			continue
		}
		hash, storeErr := e.storeFile(f.Abs)
		switch {
		case storeErr == nil:
			entries = append(entries, manifestEntry{path: f.Rel, hash: hash, exec: f.Exec})
			upserts = append(upserts, gen.UpsertStatCacheEntryParams{
				Path:       f.Rel,
				Size:       f.Size,
				MtimeNs:    f.MtimeNs,
				Inode:      int64(f.Inode),
				BlobHash:   hash,
				RecordedAt: snapStart,
			})
		case errors.Is(storeErr, fs.ErrNotExist):
			// Vanished since the walk (editor atomic saves): recorded as deleted.
		default:
			return SnapshotResult{}, fmt.Errorf("storing %s: %w", f.Rel, storeErr)
		}
	}
	var cacheDeletes []string
	for p := range cache {
		if !walked[p] {
			cacheDeletes = append(cacheDeletes, p)
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
			// Still refresh the cache: a suppressed snapshot may have warmed it
			// (e.g. the first run over an existing store), and skipping the write
			// would make every future no-op re-read the whole project.
			if err := e.updateStatCache(ctx, upserts, cacheDeletes); err != nil {
				return SnapshotResult{}, err
			}
			return SnapshotResult{Created: false}, nil
		}
	}

	id := ulid.Make().String()
	if err := e.commitState(ctx, id, head, manifestHash, opts.Label, entries, upserts, cacheDeletes); err != nil {
		return SnapshotResult{}, err
	}
	return SnapshotResult{Created: true, StateID: id}, nil
}

// applyStatCache writes the pending stat-cache changes through q (which may be
// transaction-scoped). Rows are only written for files that were actually read
// during this snapshot; untouched rows stay as they are.
func applyStatCache(
	ctx context.Context,
	q *gen.Queries,
	upserts []gen.UpsertStatCacheEntryParams,
	deletes []string,
) error {
	for _, u := range upserts {
		if err := q.UpsertStatCacheEntry(ctx, u); err != nil {
			return fmt.Errorf("updating stat cache for %s: %w", u.Path, err)
		}
	}
	for _, p := range deletes {
		if err := q.DeleteStatCacheEntry(ctx, p); err != nil {
			return fmt.Errorf("clearing stat cache for %s: %w", p, err)
		}
	}
	return nil
}

// updateStatCache applies stat-cache changes outside a state creation (the
// no-op suppression path), in a transaction of its own.
func (e *Engine) updateStatCache(
	ctx context.Context,
	upserts []gen.UpsertStatCacheEntryParams,
	deletes []string,
) error {
	if len(upserts) == 0 && len(deletes) == 0 {
		return nil
	}
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit
	if err := applyStatCache(ctx, e.q.WithTx(tx), upserts, deletes); err != nil {
		return err
	}
	return tx.Commit()
}

// commitState inserts the state row, its manifest entries, and the stat-cache
// changes, and advances HEAD, in a single transaction. All referenced blobs are
// already written and verified.
func (e *Engine) commitState(
	ctx context.Context,
	id string,
	parent sql.NullString,
	manifestHash, label string,
	entries []manifestEntry,
	cacheUpserts []gen.UpsertStatCacheEntryParams,
	cacheDeletes []string,
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
	if err := applyStatCache(ctx, q, cacheUpserts, cacheDeletes); err != nil {
		return err
	}
	if err := q.SetHead(ctx, sql.NullString{String: id, Valid: true}); err != nil {
		return fmt.Errorf("advancing HEAD: %w", err)
	}
	// Every HEAD move lands in the journal (docs/SPEC.md §2); it is what lets
	// redo find "the state I just left".
	if err := q.AppendHeadHistory(ctx, gen.AppendHeadHistoryParams{
		StateID: id,
		MovedAt: time.Now().UnixMilli(),
	}); err != nil {
		return fmt.Errorf("appending HEAD journal: %w", err)
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
