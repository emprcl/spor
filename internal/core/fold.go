package core

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/emprcl/spor/internal/db/gen"
	"github.com/emprcl/spor/internal/lock"
)

// FoldPlan previews what a fold would collapse, for the confirmation prompt.
type FoldPlan struct {
	From         string // the older boundary A
	To           string // the newer boundary B, whose content the fold keeps
	StatesFolded int    // states in the range A..B, all replaced by one
	HeadWillMove bool   // HEAD currently sits inside the range
}

// FoldPlan resolves the two boundaries and reports what folding the range A..B
// would collapse, without changing anything. It fails the same way Fold does when
// the range is degenerate, reversed, or not linear, so the CLI can surface the
// error before prompting.
func (e *Engine) FoldPlan(ctx context.Context, fromRef, toRef string) (FoldPlan, error) {
	from, to, err := e.resolveRange(ctx, fromRef, toRef)
	if err != nil {
		return FoldPlan{}, err
	}
	g, err := e.loadGraph(ctx)
	if err != nil {
		return FoldPlan{}, err
	}
	path, err := g.foldRange(from, to)
	if err != nil {
		return FoldPlan{}, err
	}
	plan := FoldPlan{From: from, To: to, StatesFolded: len(path)}
	head, err := e.q.GetHead(ctx)
	if err != nil {
		return FoldPlan{}, fmt.Errorf("reading HEAD: %w", err)
	}
	if head.Valid {
		for _, id := range path {
			if id == head.String {
				plan.HeadWillMove = true
				break
			}
		}
	}
	return plan, nil
}

// FoldResult reports the outcome of a fold.
type FoldResult struct {
	Folded      string // the new state holding B's content
	Dropped     int    // states removed (the whole A..B range)
	HeadMovedTo string // "" when HEAD did not move onto the folded state
	Settled     bool   // the force-settle recorded a pre-fold state
	SettledID   string
	Reclaimed   GCResult
}

// Fold squashes the linear range from the older state A to the newer state B into
// a single new state C with B's content and A's parent (docs/design-spec.md §5). Under
// the write lock it:
//
//  1. force-settles, so an in-flight edit is not lost (it becomes a child of B,
//     reattached to C below; an edit on an intermediate trips the linearity check);
//  2. requires the range linear: no state in it but B may have a child off to the
//     side, since only B's children are reparented (reparenting side branches is
//     out of scope for v1);
//  3. creates C = (content(B), parent(A)) and reattaches B's children to it;
//  4. if HEAD was inside the range, moves it onto C and materializes C's content;
//  5. deletes the whole range in one transaction, then GC-sweeps.
//
// Like drop/trim it is destructive but never rewriting: no surviving state
// changes, only which states exist and B's children's parent link.
func (e *Engine) Fold(ctx context.Context, fromRef, toRef string) (FoldResult, error) {
	wl, err := lock.AcquireWrite(ctx, e.writeLockPath())
	if err != nil {
		return FoldResult{}, err
	}
	defer func() { _ = wl.Release() }()

	// Resolve both boundaries before force-settling, so @~n / time refs mean what
	// the user saw in the log; the resulting opaque ids survive the settle.
	from, to, err := e.resolveRange(ctx, fromRef, toRef)
	if err != nil {
		return FoldResult{}, err
	}

	settle, err := e.snapLocked(ctx, SnapOptions{})
	if err != nil {
		return FoldResult{}, fmt.Errorf("force-settling before fold: %w", err)
	}

	g, err := e.loadGraph(ctx)
	if err != nil {
		return FoldResult{}, err
	}
	path, err := g.foldRange(from, to)
	if err != nil {
		return FoldResult{}, err
	}
	inRange := make(map[string]struct{}, len(path))
	for _, id := range path {
		inRange[id] = struct{}{}
	}

	head, err := e.q.GetHead(ctx)
	if err != nil {
		return FoldResult{}, fmt.Errorf("reading HEAD: %w", err)
	}
	headInRange := false
	if head.Valid {
		_, headInRange = inRange[head.String]
	}

	// C = (content(B), parent(A)).
	manifestHash, err := e.q.GetStateManifestHash(ctx, to)
	if err != nil {
		return FoldResult{}, fmt.Errorf("reading manifest hash: %w", err)
	}
	entries, err := e.q.ListManifestEntries(ctx, to)
	if err != nil {
		return FoldResult{}, fmt.Errorf("reading manifest: %w", err)
	}

	foldID := ulid.Make().String()
	if err := e.foldCreate(ctx, foldID, g.byID[from].ParentID, manifestHash, entries, g.children[to]); err != nil {
		return FoldResult{}, err
	}

	res := FoldResult{Folded: foldID, Dropped: len(path)}
	if settle.Created {
		res.Settled = true
		res.SettledID = settle.StateID
	}

	// C exists now, so it is safe to move HEAD off the doomed range: if HEAD is
	// inside it, relocate to C and materialize so the working tree matches C's
	// content (= B's). Done before the delete so materializeTo can still read the
	// old HEAD manifest to know which files to remove.
	if headInRange {
		if _, _, err := e.materializeTo(ctx, foldID); err != nil {
			return FoldResult{}, fmt.Errorf("moving HEAD onto the folded state: %w", err)
		}
		res.HeadMovedTo = foldID
	}

	// Delete the whole range, children-first (path is already in that order); B's
	// children are on C by now, so nothing dangles.
	if err := e.deleteStates(ctx, path); err != nil {
		return FoldResult{}, err
	}

	gc, err := e.gcLocked(ctx)
	if err != nil {
		return FoldResult{}, fmt.Errorf("reclaiming blobs: %w", err)
	}
	res.Reclaimed = gc
	return res, nil
}

// resolveRange resolves the two boundary refs and rejects a degenerate range where
// both name the same state (nothing to fold).
func (e *Engine) resolveRange(ctx context.Context, fromRef, toRef string) (from, to string, err error) {
	if from, err = e.Resolve(ctx, fromRef); err != nil {
		return "", "", err
	}
	if to, err = e.Resolve(ctx, toRef); err != nil {
		return "", "", err
	}
	if from == to {
		return "", "", fmt.Errorf("nothing to fold: a and b both resolve to %s", from)
	}
	return from, to, nil
}

// foldRange returns the states on the path from descendant B up to ancestor A,
// inclusive and children-first, after checking that the range is foldable: A must
// be an ancestor of B, and every state in the range except B must have all its
// children in the range (a child outside it is a side branch fold cannot reparent).
func (g *stateGraph) foldRange(from, to string) ([]string, error) {
	path, ok := g.rangePath(from, to)
	if !ok {
		return nil, fmt.Errorf("%s is not an ancestor of %s: fold squashes a range from an older state (a) up to a newer one (b)", from, to)
	}
	inRange := make(map[string]struct{}, len(path))
	for _, id := range path {
		inRange[id] = struct{}{}
	}
	for _, id := range path {
		if id == to {
			continue // B's children are reattached to the folded state
		}
		for _, child := range g.children[id] {
			if _, ok := inRange[child]; !ok {
				return nil, fmt.Errorf("the range %s..%s is not linear: %s has a branch outside it, and folding side branches is out of scope for v1", from, to, id)
			}
		}
	}
	return path, nil
}

// foldCreate inserts the folded state C (B's content under A's parent), copies B's
// manifest onto it, and reattaches B's children to it, in one transaction. C is
// created before the range is deleted, so the reattached children never dangle.
func (e *Engine) foldCreate(
	ctx context.Context,
	id string,
	parent sql.NullString,
	manifestHash string,
	entries []gen.ListManifestEntriesRow,
	children []string,
) error {
	return e.inTx(ctx, func(q *gen.Queries) error {
		if err := q.CreateState(ctx, gen.CreateStateParams{
			ID:           id,
			CreatedAt:    time.Now().UnixMilli(),
			ParentID:     parent,
			ManifestHash: manifestHash,
		}); err != nil {
			return fmt.Errorf("creating folded state: %w", err)
		}
		for _, ent := range entries {
			if err := q.AddManifestEntry(ctx, gen.AddManifestEntryParams{
				StateID:    id,
				Path:       ent.Path,
				BlobHash:   ent.BlobHash,
				Executable: ent.Executable,
			}); err != nil {
				return fmt.Errorf("copying manifest entry %s: %w", ent.Path, err)
			}
		}
		for _, child := range children {
			if err := q.SetStateParent(ctx, gen.SetStateParentParams{
				ParentID: sql.NullString{String: id, Valid: true},
				ID:       child,
			}); err != nil {
				return fmt.Errorf("reattaching child %s: %w", child, err)
			}
		}
		return nil
	})
}
