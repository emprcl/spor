package core

import (
	"context"
	"fmt"

	"github.com/emprcl/spor/internal/db/gen"
	"github.com/emprcl/spor/internal/lock"
)

// RerootPlan previews what a reroot would drop, for the confirmation prompt.
type RerootPlan struct {
	Target       string
	StatesToDrop int
	StatesKept   int
	HeadWillMove bool
	IsNoop       bool // target already contains every state
}

// RerootPlan resolves ref and reports what rerooting to it would drop, without
// changing anything.
func (e *Engine) RerootPlan(ctx context.Context, ref string) (RerootPlan, error) {
	target, err := e.Resolve(ctx, ref)
	if err != nil {
		return RerootPlan{}, err
	}
	g, err := e.loadGraph(ctx)
	if err != nil {
		return RerootPlan{}, err
	}
	surv := g.subtree(target)
	head, err := e.q.GetHead(ctx)
	if err != nil {
		return RerootPlan{}, fmt.Errorf("reading HEAD: %w", err)
	}

	plan := RerootPlan{
		Target:       target,
		StatesKept:   len(surv),
		StatesToDrop: len(g.states) - len(surv),
	}
	plan.IsNoop = plan.StatesToDrop == 0
	if head.Valid {
		if _, ok := surv[head.String]; !ok {
			plan.HeadWillMove = true
		}
	}
	return plan, nil
}

// RerootResult reports the outcome of a reroot.
type RerootResult struct {
	Target      string
	Dropped     int
	Kept        int
	HeadMovedTo string // "" when HEAD did not move
	Reclaimed   GCResult
}

// Reroot makes a state the new root, dropping everything not under it: the dual of
// prune (docs/SPEC.md §5). It force-settles first; if HEAD is on a dropped branch
// it is relocated to the new root and re-materialized. The new root's parent link
// is cleared and every non-survivor is deleted in one transaction, children before
// parents, then a GC sweep reclaims unreferenced blobs, all under the write lock.
// Rerooting at a state that already contains the whole history is a no-op.
func (e *Engine) Reroot(ctx context.Context, ref string) (RerootResult, error) {
	wl, err := lock.AcquireWrite(ctx, e.writeLockPath())
	if err != nil {
		return RerootResult{}, err
	}
	defer func() { _ = wl.Release() }()

	target, err := e.Resolve(ctx, ref)
	if err != nil {
		return RerootResult{}, err
	}
	g, err := e.loadGraph(ctx)
	if err != nil {
		return RerootResult{}, err
	}
	surv := g.subtree(target)
	res := RerootResult{Target: target, Kept: len(surv)}
	if len(surv) == len(g.states) {
		return res, nil // already the root of everything: nothing to drop
	}

	// Force-settle first so an in-flight edit is not lost.
	if _, err := e.snapshotLocked(ctx, SnapshotOptions{}); err != nil {
		return RerootResult{}, fmt.Errorf("force-settling before reroot: %w", err)
	}
	// The settle may have added a state; reload and recompute survivors.
	g, err = e.loadGraph(ctx)
	if err != nil {
		return RerootResult{}, err
	}
	surv = g.subtree(target)
	res.Kept = len(surv)

	head, err := e.q.GetHead(ctx)
	if err != nil {
		return RerootResult{}, fmt.Errorf("reading HEAD: %w", err)
	}
	if head.Valid {
		if _, ok := surv[head.String]; !ok {
			// HEAD is on a branch being dropped: move it to the new root and
			// materialize (already settled, so no second settle is needed).
			if _, _, err := e.materializeTo(ctx, target); err != nil {
				return RerootResult{}, fmt.Errorf("relocating HEAD before reroot: %w", err)
			}
			res.HeadMovedTo = target
		}
	}

	nonSurv := make(map[string]struct{}, len(g.states)-len(surv))
	for _, s := range g.states {
		if _, ok := surv[s.ID]; !ok {
			nonSurv[s.ID] = struct{}{}
		}
	}
	order := g.deletionOrder(nonSurv)
	if err := e.rerootCommit(ctx, target, order); err != nil {
		return RerootResult{}, err
	}
	res.Dropped = len(order)

	gc, err := e.gcLocked(ctx)
	if err != nil {
		return RerootResult{}, fmt.Errorf("reclaiming blobs: %w", err)
	}
	res.Reclaimed = gc
	return res, nil
}

// rerootCommit detaches the new root (parent = NULL) and deletes every
// non-survivor, in one transaction. The delete order must be children-first, and
// clearing the root's parent first keeps its now-doomed old parent deletable.
func (e *Engine) rerootCommit(ctx context.Context, target string, order []string) error {
	return e.inTx(ctx, func(q *gen.Queries) error {
		if err := q.SetStateParentNull(ctx, target); err != nil {
			return fmt.Errorf("detaching new root %s: %w", target, err)
		}
		for _, id := range order {
			if err := q.DeleteState(ctx, id); err != nil {
				return fmt.Errorf("deleting state %s: %w", id, err)
			}
		}
		return nil
	})
}
