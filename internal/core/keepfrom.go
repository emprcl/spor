package core

import (
	"context"
	"fmt"

	"github.com/emprcl/spor/internal/db/gen"
	"github.com/emprcl/spor/internal/lock"
)

// KeepfromPlan previews what a keepfrom would drop, for the confirmation prompt.
type KeepfromPlan struct {
	Target       string
	StatesToDrop int
	StatesKept   int
	HeadWillMove bool
	IsNoop       bool // target already contains every state
}

// KeepfromPlan resolves ref and reports what rerooting to it would drop, without
// changing anything.
func (e *Engine) KeepfromPlan(ctx context.Context, ref string) (KeepfromPlan, error) {
	target, err := e.Resolve(ctx, ref)
	if err != nil {
		return KeepfromPlan{}, err
	}
	g, err := e.loadGraph(ctx)
	if err != nil {
		return KeepfromPlan{}, err
	}
	surv := g.subtree(target)
	head, err := e.q.GetHead(ctx)
	if err != nil {
		return KeepfromPlan{}, fmt.Errorf("reading HEAD: %w", err)
	}

	plan := KeepfromPlan{
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

// KeepfromResult reports the outcome of a keepfrom.
type KeepfromResult struct {
	Target      string
	Dropped     int
	Kept        int
	HeadMovedTo string // "" when HEAD did not move
	Reclaimed   GCResult
}

// Keepfrom makes a state the new root, dropping everything not under it: the dual of
// dropfrom (docs/design-spec.md §5). It force-settles first; if HEAD is on a dropped branch
// it is relocated to the new root and re-materialized. The new root's parent link
// is cleared and every non-survivor is deleted in one transaction, children before
// parents, then a GC sweep reclaims unreferenced blobs, all under the write lock.
// Keeping from a state that already contains the whole history is a no-op.
func (e *Engine) Keepfrom(ctx context.Context, ref string) (KeepfromResult, error) {
	wl, err := lock.AcquireWrite(ctx, e.writeLockPath())
	if err != nil {
		return KeepfromResult{}, err
	}
	defer func() { _ = wl.Release() }()

	target, err := e.Resolve(ctx, ref)
	if err != nil {
		return KeepfromResult{}, err
	}
	g, err := e.loadGraph(ctx)
	if err != nil {
		return KeepfromResult{}, err
	}
	surv := g.subtree(target)
	res := KeepfromResult{Target: target, Kept: len(surv)}
	if len(surv) == len(g.states) {
		return res, nil // already the root of everything: nothing to drop
	}

	// Force-settle first so an in-flight edit is not lost.
	if _, err := e.snapLocked(ctx, SnapOptions{}); err != nil {
		return KeepfromResult{}, fmt.Errorf("force-settling before keepfrom: %w", err)
	}
	// The settle may have added a state; reload and recompute survivors.
	g, err = e.loadGraph(ctx)
	if err != nil {
		return KeepfromResult{}, err
	}
	surv = g.subtree(target)
	res.Kept = len(surv)

	head, err := e.q.GetHead(ctx)
	if err != nil {
		return KeepfromResult{}, fmt.Errorf("reading HEAD: %w", err)
	}
	if head.Valid {
		if _, ok := surv[head.String]; !ok {
			// HEAD is on a branch being dropped: move it to the new root and
			// materialize (already settled, so no second settle is needed).
			if _, _, err := e.materializeTo(ctx, target); err != nil {
				return KeepfromResult{}, fmt.Errorf("relocating HEAD before keepfrom: %w", err)
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
	if err := e.keepfromCommit(ctx, target, order); err != nil {
		return KeepfromResult{}, err
	}
	res.Dropped = len(order)

	gc, err := e.gcLocked(ctx)
	if err != nil {
		return KeepfromResult{}, fmt.Errorf("reclaiming blobs: %w", err)
	}
	res.Reclaimed = gc
	return res, nil
}

// keepfromCommit detaches the new root (parent = NULL) and deletes every
// non-survivor, in one transaction. The delete order must be children-first, and
// clearing the root's parent first keeps its now-doomed old parent deletable.
func (e *Engine) keepfromCommit(ctx context.Context, target string, order []string) error {
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
