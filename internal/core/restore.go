package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/emprcl/spor/internal/db/gen"
	"github.com/emprcl/spor/internal/lock"
)

// RestoreResult reports the outcome of a restore. Settled is true when the
// force-settle step recorded a pre-restore state (i.e. there were uncommitted
// edits); SettledID names it. Written and Deleted count the files touched while
// materializing the target.
type RestoreResult struct {
	StateID   string // the restored state, now HEAD
	Settled   bool
	SettledID string
	Written   int
	Deleted   int
}

// Restore materializes a past state into the working tree and points HEAD at it,
// per docs/SPEC.md §5. It runs entirely under the write lock:
//
//  1. force-settle by snapshotting the current tree, so an in-flight edit is not
//     lost and restore stays undoable (a no-op if nothing changed);
//  2. write every file in the target's manifest (applying the stored execute
//     bit) and delete every path present in HEAD's manifest but not the
//     target's, never touching untracked or ignored paths;
//  3. set HEAD to the target and journal the move.
//
// Restore is not atomic: a crash mid-materialization leaves a mixed tree that
// re-running restore repairs, since step 1 already recorded the pre-restore tree.
func (e *Engine) Restore(ctx context.Context, ref string) (RestoreResult, error) {
	wl, err := lock.AcquireWrite(ctx, e.writeLockPath())
	if err != nil {
		return RestoreResult{}, err
	}
	defer func() { _ = wl.Release() }()

	// Resolve the ref against the current HEAD *before* force-settling, so @~n and
	// time refs mean what the user saw in the log, not something shifted by the
	// pre-restore snapshot we are about to take. The result is an opaque, stable
	// id that survives the snapshot (docs/SPEC.md §5, §6).
	target, err := e.Resolve(ctx, ref)
	if err != nil {
		return RestoreResult{}, err
	}

	// Step 1: force-settle. A one-shot restore cannot drain another process's
	// debounce timer, so it snapshots itself.
	settle, err := e.snapshotLocked(ctx, SnapshotOptions{})
	if err != nil {
		return RestoreResult{}, fmt.Errorf("force-settling before restore: %w", err)
	}

	// The post-settle HEAD manifest is exactly what is on disk now, so it is the
	// authority for which files to delete.
	head, err := e.q.GetHead(ctx)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("reading HEAD: %w", err)
	}
	targetManifest, err := e.q.ListManifestEntries(ctx, target)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("reading target manifest: %w", err)
	}
	var current []gen.ListManifestEntriesRow
	if head.Valid {
		current, err = e.q.ListManifestEntries(ctx, head.String)
		if err != nil {
			return RestoreResult{}, fmt.Errorf("reading current manifest: %w", err)
		}
	}

	// Step 2: materialize the target over the current tree.
	written, deleted, err := e.materialize(targetManifest, current)
	if err != nil {
		return RestoreResult{}, err
	}

	// Step 3: point HEAD at the restored state and journal the move.
	if err := e.setHeadTo(ctx, target); err != nil {
		return RestoreResult{}, err
	}

	res := RestoreResult{StateID: target, Written: written, Deleted: deleted}
	if settle.Created {
		res.Settled = true
		res.SettledID = settle.StateID
	}
	return res, nil
}

// materialize writes the target manifest into the working tree and removes files
// the current manifest has but the target does not. Because a force-settle ran
// first, the current manifest exactly matches what is on disk, so a target entry
// whose path, blob, and execute bit are unchanged is already correct and skipped
// (keeping restore of a small change cheap; docs/SPEC.md §9).
func (e *Engine) materialize(target, current []gen.ListManifestEntriesRow) (written, deleted int, err error) {
	onDisk := make(map[string]gen.ListManifestEntriesRow, len(current))
	for _, ent := range current {
		onDisk[ent.Path] = ent
	}

	targetPaths := make(map[string]struct{}, len(target))
	for _, ent := range target {
		targetPaths[ent.Path] = struct{}{}
		if cur, ok := onDisk[ent.Path]; ok &&
			cur.BlobHash == ent.BlobHash && cur.Executable == ent.Executable {
			continue // already on disk with the right content and mode
		}
		if err := e.writeWorkingFile(ent); err != nil {
			return written, deleted, err
		}
		written++
	}

	for _, ent := range current {
		if _, keep := targetPaths[ent.Path]; keep {
			continue
		}
		abs := e.workingPath(ent.Path)
		if err := os.Remove(abs); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return written, deleted, fmt.Errorf("deleting %s: %w", ent.Path, err)
		}
		removeEmptyParents(e.root, abs)
		deleted++
	}
	return written, deleted, nil
}

// writeWorkingFile writes one manifest entry into the working tree from its blob,
// creating parent directories and applying the stored execute bit. OpenFile does
// not change an existing file's mode, so a chmod-only change needs the explicit
// chmod (docs/SPEC.md §4).
func (e *Engine) writeWorkingFile(ent gen.ListManifestEntriesRow) error {
	abs := e.workingPath(ent.Path)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("creating directory for %s: %w", ent.Path, err)
	}
	r, err := e.blobs.Open(ent.BlobHash)
	if err != nil {
		return fmt.Errorf("opening blob for %s: %w", ent.Path, err)
	}
	defer r.Close()

	mode := os.FileMode(0o644)
	if ent.Executable != 0 {
		mode = 0o755
	}
	f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("writing %s: %w", ent.Path, err)
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return fmt.Errorf("writing %s: %w", ent.Path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", ent.Path, err)
	}
	if err := os.Chmod(abs, mode); err != nil {
		return fmt.Errorf("setting mode on %s: %w", ent.Path, err)
	}
	return nil
}

// setHeadTo moves HEAD to id and appends the move to the journal, in one
// transaction (docs/SPEC.md §2).
func (e *Engine) setHeadTo(ctx context.Context, id string) error {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit
	q := e.q.WithTx(tx)
	if err := q.SetHead(ctx, sql.NullString{String: id, Valid: true}); err != nil {
		return fmt.Errorf("setting HEAD: %w", err)
	}
	if err := q.AppendHeadHistory(ctx, gen.AppendHeadHistoryParams{
		StateID: id,
		MovedAt: time.Now().UnixMilli(),
	}); err != nil {
		return fmt.Errorf("appending HEAD journal: %w", err)
	}
	return tx.Commit()
}

// workingPath maps a slash-separated manifest path to its absolute location in
// the working tree.
func (e *Engine) workingPath(rel string) string {
	return filepath.Join(e.root, filepath.FromSlash(rel))
}

// removeEmptyParents removes directories left empty by a deletion, walking up
// from the deleted file toward root. os.Remove refuses a non-empty directory, so
// the walk stops at the first directory still holding an untracked sibling, and
// it never ascends to or past root. Errors are ignored: a kept directory is
// harmless.
func removeEmptyParents(root, abs string) {
	rootClean := filepath.Clean(root)
	for dir := filepath.Dir(abs); len(dir) > len(rootClean) && dir != rootClean; dir = filepath.Dir(dir) {
		if err := os.Remove(dir); err != nil {
			return
		}
	}
}
