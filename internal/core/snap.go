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
	"runtime"
	"sync/atomic"
	"time"

	"github.com/oklog/ulid/v2"
	"golang.org/x/sync/errgroup"

	"github.com/emprcl/spor/internal/blob"
	"github.com/emprcl/spor/internal/db/gen"
	"github.com/emprcl/spor/internal/lock"
	"github.com/emprcl/spor/internal/walk"
)

// syncBatchThreshold is the number of blobs a snapshot must store before it
// batches their durability into a single whole-store syncfs (see snapLocked and
// blob.Batch). Below it, blobs are fsynced inline: for the few-file snapshots
// that dominate while `spor watch` runs, that avoids forcing a filesystem-wide
// flush on every settle. Above it (the first snapshot, bulk changes), the one
// batched sync is far cheaper than one fsync per blob.
const syncBatchThreshold = 64

// SnapOptions configures a snapshot.
type SnapOptions struct {
	Label string // optional human name for the created state
	// OnProgress, if set, is called as the walk's files are processed, with the
	// number handled so far and the total tracked. It runs from the snapshot's
	// worker goroutines (concurrently), so it must be cheap and safe to call
	// from multiple goroutines. It exists so a front-end can show first-snap
	// progress instead of a blank screen (see cli watch).
	OnProgress func(done, total int)
}

// SnapResult reports the outcome. When Created is false the working tree
// matched HEAD and no state was recorded (no-op suppression).
type SnapResult struct {
	Created bool
	StateID string
}

// Snap records the current working tree as a new state, per docs/design-spec.md §4.
// It walks the tree, stores any new blobs, and, unless the resulting manifest
// matches HEAD (no-op suppression), creates a state under HEAD and advances
// HEAD, all under the write lock. Blobs are written before the state is
// committed, so an incomplete state is never visible.
func (e *Engine) Snap(ctx context.Context, opts SnapOptions) (SnapResult, error) {
	wl, err := lock.AcquireWrite(ctx, e.writeLockPath())
	if err != nil {
		return SnapResult{}, err
	}
	defer func() { _ = wl.Release() }()
	return e.snapLocked(ctx, opts)
}

// snapLocked is Snap's body, assuming the caller already holds the write
// lock. Go force-settles (docs/design-spec.md §5) by calling this under the single
// lock it holds for the whole operation, so the pre-restore snapshot and the
// materialization can never interleave with another front-end.
func (e *Engine) snapLocked(ctx context.Context, opts SnapOptions) (SnapResult, error) {
	// Read HEAD up front: it is the parent of any new state, the base for no-op
	// suppression, and (on Windows) the source of inherited exec bits. The write
	// lock keeps it stable for the rest of the operation.
	head, err := e.q.GetHead(ctx)
	if err != nil {
		return SnapResult{}, fmt.Errorf("reading HEAD: %w", err)
	}

	// Labels are unique aliases (docs/design-spec.md §2), so reject a taken one up front,
	// before doing the walk, rather than failing on the insert.
	if opts.Label != "" {
		if owner, err := e.labelOwner(ctx, opts.Label); err != nil {
			return SnapResult{}, err
		} else if owner != "" {
			return SnapResult{}, fmt.Errorf("label %q is already used by state %s", opts.Label, owner)
		}
	}

	// Walk → store blobs → build the manifest (in sorted path order).
	files, err := walk.Walk(e.root)
	if err != nil {
		return SnapResult{}, err
	}

	// The stat cache (docs/design-spec.md §4) elides re-reading unchanged files. A row
	// is trusted only when size, mtime, and inode all match, the mtime is
	// strictly older than the row's recording time (the racily-clean rule), and
	// the blob it names is actually on disk. snapStart is taken before any file
	// is read, so content mutating mid-snapshot always lands at or after it and
	// the affected row is distrusted next time.
	cacheRows, err := e.q.ListStatCache(ctx)
	if err != nil {
		return SnapResult{}, fmt.Errorf("reading stat cache: %w", err)
	}
	cache := make(map[string]gen.StatCache, len(cacheRows))
	for _, r := range cacheRows {
		cache[r.Path] = r
	}
	snapStart := time.Now().UnixNano()
	// A cold store (empty cache: the first snapshot) can never hit the blob
	// dedup pre-hash, so store misses in a single read+compress pass instead of
	// hashing every file twice.
	cold := len(cache) == 0

	// snapFile is one file's outcome, kept index-aligned with files so the
	// manifest stays in the walk's sorted order no matter which worker finishes
	// first. has is false for a file that vanished mid-snapshot (recorded as
	// deleted); upsert is set only for a freshly stored miss.
	type snapFile struct {
		entry  manifestEntry
		upsert *gen.UpsertStatCacheEntryParams
		has    bool
	}
	results := make([]snapFile, len(files))
	walked := make(map[string]bool, len(files))

	// Classify serially (cheap: a stat cache lookup and, on a hit, a blob stat).
	// Hits are resolved here; misses are queued for the concurrent store below.
	var toStore []int
	for i, f := range files {
		walked[f.Rel] = true
		if row, ok := cache[f.Rel]; ok &&
			row.Size == f.Size && row.MtimeNs == f.MtimeNs && row.Inode == int64(f.Inode) &&
			f.MtimeNs < row.RecordedAt &&
			e.blobs.Has(row.BlobHash) {
			results[i] = snapFile{entry: manifestEntry{path: f.Rel, hash: row.BlobHash, exec: f.Exec}, has: true}
			continue
		}
		toStore = append(toStore, i)
	}

	// Store the misses concurrently: hashing and zstd compression are CPU-bound
	// and the file reads overlap, so a bounded pool turns a big cold snapshot
	// from a serial crawl into a multi-core one.
	total := len(files)
	var done int64
	if opts.OnProgress != nil {
		// Hits are already handled; count them so progress covers the whole tree.
		atomic.AddInt64(&done, int64(total-len(toStore)))
	}
	report := func() {
		if opts.OnProgress != nil {
			opts.OnProgress(int(atomic.AddInt64(&done, 1)), total)
		}
	}

	// Batch the durability fsyncs into one whole-store sync (Flush) only when
	// enough blobs are being stored to pay for it: the first snapshot and bulk
	// changes. A small snapshot, the common case while `spor watch` runs,
	// fsyncs its handful of blobs inline instead, so it doesn't force a
	// filesystem-wide flush every few seconds (docs/design-spec.md §8). nil
	// batch means the inline path (storeFile handles both).
	var batch *blob.Batch
	if len(toStore) >= syncBatchThreshold {
		batch = e.blobs.NewBatch()
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.GOMAXPROCS(0))
	for _, idx := range toStore {
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err // ctx cancelled (e.g. Ctrl+C during a big first snap)
			}
			f := files[idx]
			hash, storeErr := e.storeFile(batch, f.Abs, cold)
			switch {
			case storeErr == nil:
				results[idx] = snapFile{
					entry: manifestEntry{path: f.Rel, hash: hash, exec: f.Exec},
					upsert: &gen.UpsertStatCacheEntryParams{
						Path:       f.Rel,
						Size:       f.Size,
						MtimeNs:    f.MtimeNs,
						Inode:      int64(f.Inode),
						BlobHash:   hash,
						RecordedAt: snapStart,
					},
					has: true,
				}
			case errors.Is(storeErr, fs.ErrNotExist):
				// Vanished since the walk (editor atomic saves): recorded as deleted.
			default:
				return fmt.Errorf("storing %s: %w", f.Rel, storeErr)
			}
			report()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return SnapResult{}, err
	}
	// Make every blob the batch stored durable (content and rename) before the
	// state that references them is committed (docs/design-spec.md §8). A nil
	// batch already fsynced each blob inline, so there is nothing to flush.
	if batch != nil {
		if err := batch.Flush(); err != nil {
			return SnapResult{}, err
		}
	}

	entries := make([]manifestEntry, 0, len(files))
	var upserts []gen.UpsertStatCacheEntryParams
	for i := range results {
		if r := results[i]; r.has {
			entries = append(entries, r.entry)
			if r.upsert != nil {
				upserts = append(upserts, *r.upsert)
			}
		}
	}
	var cacheDeletes []string
	for p := range cache {
		if !walked[p] {
			cacheDeletes = append(cacheDeletes, p)
		}
	}
	// On platforms that cannot observe the execute bit, inherit it from HEAD so a
	// snapshot there does not flip inherited bits back off (docs/design-spec.md §4).
	if err := e.resolveExec(ctx, head, entries); err != nil {
		return SnapResult{}, err
	}
	manifestHash := hashManifest(entries)

	// No-op suppression: compare against HEAD's manifest hash.
	if head.Valid {
		prev, err := e.q.GetStateManifestHash(ctx, head.String)
		if err != nil {
			return SnapResult{}, fmt.Errorf("reading HEAD manifest: %w", err)
		}
		if prev == manifestHash {
			// Still refresh the cache: a suppressed snapshot may have warmed it
			// (e.g. the first run over an existing store), and skipping the write
			// would make every future no-op re-read the whole project.
			if err := e.updateStatCache(ctx, upserts, cacheDeletes); err != nil {
				return SnapResult{}, err
			}
			return SnapResult{Created: false}, nil
		}
	}

	id := ulid.Make().String()
	if err := e.commitState(ctx, id, head, manifestHash, opts.Label, entries, upserts, cacheDeletes); err != nil {
		return SnapResult{}, err
	}
	return SnapResult{Created: true, StateID: id}, nil
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
	// Every HEAD move lands in the journal (docs/design-spec.md §2); it is what lets
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
// When batch is non-nil the store's durability fsync is deferred to the batch's
// Flush; when nil the blob is fsynced inline. On a cold store (the first
// snapshot) it uses a single read+compress pass; otherwise it uses the dedup
// pre-hash path, which writes nothing for content the store already holds.
func (e *Engine) storeFile(batch *blob.Batch, abs string, cold bool) (string, error) {
	f, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	defer f.Close()
	switch {
	case batch != nil && cold:
		return batch.Put(f)
	case batch != nil:
		return batch.PutFile(f)
	case cold:
		return e.blobs.Put(f)
	default:
		return e.blobs.PutFile(f)
	}
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
// content change, still produces a distinct state. See docs/design-spec.md §2.
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
