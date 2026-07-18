package core

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/emprcl/spor/internal/db/gen"
	"github.com/emprcl/spor/internal/lock"
)

// PickResult reports the outcome of a single-path pick. Settled and
// SettledID mirror GoResult: they name the pre-pick snapshot when there were
// uncommitted edits. Written counts the files brought back; Created and StateID
// describe the snapshot recording the picked tree (Created is false when the
// requested content already matched the working tree, so nothing was written).
type PickResult struct {
	Target    string // the snapshot the content came from
	Settled   bool
	SettledID string
	Written   int
	Created   bool
	StateID   string
}

// Files returns the slash-separated, root-relative paths tracked by ref's
// snapshot, in manifest (sorted) order. It exists so a front-end can offer the
// actual pickable paths (see the TUI's pick overlay) instead of a blind text
// field.
func (e *Engine) Files(ctx context.Context, ref string) ([]string, error) {
	target, err := e.Resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	entries, err := e.q.ListManifestEntries(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("reading manifest: %w", err)
	}
	paths := make([]string, len(entries))
	for i, ent := range entries {
		paths[i] = ent.Path
	}
	return paths, nil
}

// Pick brings one file, or one directory subtree, back from a past state
// without moving HEAD and without touching any other path: the file-level
// counterpart of Go (docs/design-spec.md §5, §6). Under the write lock it:
//
//  1. resolves ref against the current HEAD, before the settle mutates history
//     (the same ordering rationale as Go);
//  2. force-settles, so the pre-pick tree is recorded and the pick stays
//     undoable;
//  3. writes every entry of the target's manifest whose path is relPath or lies
//     under relPath/, skipping entries already on disk with the right content
//     and mode. It never deletes anything;
//  4. snapshots again, so the picked tree is itself recorded.
//
// relPath is slash-separated and relative to the project root.
func (e *Engine) Pick(ctx context.Context, ref, relPath string) (PickResult, error) {
	relPath = strings.TrimSuffix(path.Clean(strings.TrimSpace(relPath)), "/")
	if relPath == "" || relPath == "." || relPath == ".." || strings.HasPrefix(relPath, "../") {
		return PickResult{}, fmt.Errorf("pick needs a file or directory path inside the project, got %q", relPath)
	}

	wl, err := lock.AcquireWrite(ctx, e.writeLockPath())
	if err != nil {
		return PickResult{}, err
	}
	defer func() { _ = wl.Release() }()

	target, err := e.Resolve(ctx, ref)
	if err != nil {
		return PickResult{}, err
	}
	res := PickResult{Target: target}

	entries, err := e.q.ListManifestEntries(ctx, target)
	if err != nil {
		return PickResult{}, fmt.Errorf("reading target manifest: %w", err)
	}
	var matched []gen.ListManifestEntriesRow
	for _, ent := range entries {
		if ent.Path == relPath || strings.HasPrefix(ent.Path, relPath+"/") {
			matched = append(matched, ent)
		}
	}
	if len(matched) == 0 {
		return PickResult{}, fmt.Errorf("no file or directory %q in %s", relPath, target)
	}

	settle, err := e.snapLocked(ctx, SnapOptions{})
	if err != nil {
		return PickResult{}, fmt.Errorf("force-settling before pick: %w", err)
	}
	if settle.Created {
		res.Settled = true
		res.SettledID = settle.StateID
	}

	// The settle above made HEAD's manifest match the disk exactly, so it tells
	// which matched entries are already correct and can be skipped (the same
	// already-on-disk shortcut materialize uses).
	onDisk := make(map[string]gen.ListManifestEntriesRow)
	head, err := e.q.GetHead(ctx)
	if err != nil {
		return PickResult{}, fmt.Errorf("reading HEAD: %w", err)
	}
	if head.Valid {
		current, err := e.q.ListManifestEntries(ctx, head.String)
		if err != nil {
			return PickResult{}, fmt.Errorf("reading current manifest: %w", err)
		}
		for _, ent := range current {
			onDisk[ent.Path] = ent
		}
	}
	for _, ent := range matched {
		if cur, ok := onDisk[ent.Path]; ok &&
			cur.BlobHash == ent.BlobHash && cur.Executable == ent.Executable {
			continue // already on disk with the right content and mode
		}
		if err := e.writeWorkingFile(ent); err != nil {
			return res, err
		}
		res.Written++
	}
	if res.Written == 0 {
		return res, nil // everything already matched; nothing to record
	}

	after, err := e.snapLocked(ctx, SnapOptions{})
	if err != nil {
		return res, fmt.Errorf("recording the picked tree: %w", err)
	}
	res.Created = after.Created
	res.StateID = after.StateID
	return res, nil
}
