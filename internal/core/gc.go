package core

import (
	"context"
	"fmt"

	"github.com/emprcl/spor/internal/lock"
)

// GCResult reports what a GC sweep reclaimed.
type GCResult struct {
	Removed int   // blobs deleted
	Bytes   int64 // on-disk bytes reclaimed
}

// GC reclaims blobs no surviving state references, a mark-sweep over the blob
// store (docs/SPEC.md §8). It takes the write lock like any mutating operation,
// so it cannot race an in-flight snapshot (whose blobs land on disk before its
// state row commits); every on-disk blob absent from the reachable set is
// therefore a true orphan. Sweeping only ever deletes blobs, never state rows.
func (e *Engine) GC(ctx context.Context) (GCResult, error) {
	wl, err := lock.AcquireWrite(ctx, e.writeLockPath())
	if err != nil {
		return GCResult{}, err
	}
	defer func() { _ = wl.Release() }()
	return e.gcLocked(ctx)
}

// gcLocked is GC's mark-sweep body, assuming the caller already holds the write
// lock. Prune, reroot, and compact run it after removing states so newly
// unreferenced blobs are reclaimed in the same locked operation (docs/SPEC.md §8).
func (e *Engine) gcLocked(ctx context.Context) (GCResult, error) {
	referenced, err := e.referencedBlobHashes(ctx)
	if err != nil {
		return GCResult{}, err
	}
	onDisk, err := e.blobs.List()
	if err != nil {
		return GCResult{}, fmt.Errorf("listing blobs: %w", err)
	}

	var res GCResult
	for _, hash := range onDisk {
		if _, ok := referenced[hash]; ok {
			continue
		}
		size, err := e.blobs.Size(hash)
		if err != nil {
			size = 0 // vanished between list and stat; still attempt the remove
		}
		if err := e.blobs.Remove(hash); err != nil {
			return GCResult{}, fmt.Errorf("removing blob %s: %w", hash, err)
		}
		res.Removed++
		res.Bytes += size
	}
	return res, nil
}

// referencedBlobHashes is the reachable set: every blob hash named by any state's
// manifest. A full pass over all states is required before any blob is swept
// (docs/SPEC.md §8), and it is also what GC marks against.
func (e *Engine) referencedBlobHashes(ctx context.Context) (map[string]struct{}, error) {
	states, err := e.q.ListStates(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing states: %w", err)
	}
	set := make(map[string]struct{})
	for _, s := range states {
		entries, err := e.q.ListManifestEntries(ctx, s.ID)
		if err != nil {
			return nil, fmt.Errorf("reading manifest of %s: %w", s.ID, err)
		}
		for _, ent := range entries {
			set[ent.BlobHash] = struct{}{}
		}
	}
	return set, nil
}
