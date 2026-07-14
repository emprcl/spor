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

// GoResult reports the outcome of a restore. Settled is true when the
// force-settle step recorded a pre-restore state (i.e. there were uncommitted
// edits); SettledID names it. Written and Deleted count the files touched while
// materializing the target.
type GoResult struct {
	StateID   string // the restored state, now HEAD
	Settled   bool
	SettledID string
	Written   int
	Deleted   int
}

// Go materializes a past state into the working tree and points HEAD at it,
// per docs/design-spec.md §5. It runs entirely under the write lock:
//
//  1. force-settle by snapshotting the current tree, so an in-flight edit is not
//     lost and restore stays undoable (a no-op if nothing changed);
//  2. write every file in the target's manifest (applying the stored execute
//     bit) and delete every path present in HEAD's manifest but not the
//     target's, never touching untracked or ignored paths;
//  3. set HEAD to the target and journal the move.
//
// Go is not atomic: a crash mid-materialization leaves a mixed tree that
// re-running restore repairs, since step 1 already recorded the pre-restore tree.
func (e *Engine) Go(ctx context.Context, ref string) (GoResult, error) {
	wl, err := lock.AcquireWrite(ctx, e.writeLockPath())
	if err != nil {
		return GoResult{}, err
	}
	defer func() { _ = wl.Release() }()

	// Resolve the ref against the current HEAD *before* force-settling, so @~n and
	// time refs mean what the user saw in the log, not something shifted by the
	// pre-restore snapshot we are about to take. The result is an opaque, stable
	// id that survives the snapshot (docs/design-spec.md §5, §6).
	target, err := e.Resolve(ctx, ref)
	if err != nil {
		return GoResult{}, err
	}
	return e.goToLocked(ctx, target)
}

// goToLocked materializes an already-chosen target state into the working
// tree and moves HEAD to it. The caller must already hold the write lock and
// must have chosen target before this runs, since the force-settle here mutates
// history. It is the shared tail of restore, undo, and redo (docs/design-spec.md §5).
func (e *Engine) goToLocked(ctx context.Context, target string) (GoResult, error) {
	// Step 1: force-settle. A one-shot restore cannot drain another process's
	// debounce timer, so it snapshots itself.
	settle, err := e.snapLocked(ctx, SnapOptions{})
	if err != nil {
		return GoResult{}, fmt.Errorf("force-settling before restore: %w", err)
	}

	// Steps 2 and 3: materialize the target over the current tree and move HEAD.
	written, deleted, err := e.materializeTo(ctx, target)
	if err != nil {
		return GoResult{}, err
	}

	res := GoResult{StateID: target, Written: written, Deleted: deleted}
	if settle.Created {
		res.Settled = true
		res.SettledID = settle.StateID
	}
	return res, nil
}

// materializeTo writes target's manifest into the working tree and points HEAD at
// it, without force-settling first (the caller decides whether to settle). It is
// the shared tail of restore and of the HEAD relocation dropfrom/keepfrom perform when
// the current branch is about to be dropped. The caller must hold the write lock.
// The post-settle HEAD manifest is the authority for which files to delete, so
// callers that need the working tree to match HEAD should settle beforehand.
func (e *Engine) materializeTo(ctx context.Context, target string) (written, deleted int, err error) {
	head, err := e.q.GetHead(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("reading HEAD: %w", err)
	}
	targetManifest, err := e.q.ListManifestEntries(ctx, target)
	if err != nil {
		return 0, 0, fmt.Errorf("reading target manifest: %w", err)
	}
	var current []gen.ListManifestEntriesRow
	if head.Valid {
		current, err = e.q.ListManifestEntries(ctx, head.String)
		if err != nil {
			return 0, 0, fmt.Errorf("reading current manifest: %w", err)
		}
	}
	written, deleted, err = e.materialize(targetManifest, current)
	if err != nil {
		return written, deleted, err
	}
	if err := e.setHeadTo(ctx, target); err != nil {
		return written, deleted, err
	}
	return written, deleted, nil
}

// materialize writes the target manifest into the working tree and removes files
// the current manifest has but the target does not. Because a force-settle ran
// first, the current manifest exactly matches what is on disk, so a target entry
// whose path, blob, and execute bit are unchanged is already correct and skipped
// (keeping restore of a small change cheap; docs/design-spec.md §9).
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
// chmod (docs/design-spec.md §4).
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
// transaction (docs/design-spec.md §2).
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
