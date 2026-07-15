package core

import (
	"context"
	"fmt"

	"github.com/emprcl/spor/internal/lock"
)

// DropPlan previews what a drop would destroy, for the confirmation prompt.
type DropPlan struct {
	Target           string
	StatesToDelete   int
	HeadWillMove     bool
	HeadTarget       string // parent id HEAD moves to; "" when it does not move
	WipesEntireStore bool   // the drop removes every state
}

// DropPlan resolves ref and reports what pruning it would remove, without
// changing anything.
func (e *Engine) DropPlan(ctx context.Context, ref string) (DropPlan, error) {
	target, err := e.Resolve(ctx, ref)
	if err != nil {
		return DropPlan{}, err
	}
	g, err := e.loadGraph(ctx)
	if err != nil {
		return DropPlan{}, err
	}
	sub := g.subtree(target)
	head, err := e.q.GetHead(ctx)
	if err != nil {
		return DropPlan{}, fmt.Errorf("reading HEAD: %w", err)
	}

	plan := DropPlan{
		Target:           target,
		StatesToDelete:   len(sub),
		WipesEntireStore: len(sub) == len(g.states),
	}
	if head.Valid {
		if _, inSub := sub[head.String]; inSub {
			if parent := g.byID[target].ParentID; parent.Valid {
				plan.HeadWillMove = true
				plan.HeadTarget = parent.String
			}
		}
	}
	return plan, nil
}

// DropResult reports the outcome of a drop.
type DropResult struct {
	Target      string
	Deleted     int
	HeadMovedTo string // "" when HEAD did not move
	HeadCleared bool   // HEAD became empty (pruned the root you were on)
	Reclaimed   GCResult
}

// Drop deletes a state and its whole subtree (docs/design-spec.md §5). If HEAD is inside
// the subtree it is first moved to the target's parent and re-materialized
// (force-settling so an in-flight edit is not lost); pruning the root you are on
// clears HEAD and leaves the working tree untouched. The subtree is deleted in one
// transaction, then a GC sweep reclaims the newly unreferenced blobs, all under
// the write lock.
func (e *Engine) Drop(ctx context.Context, ref string) (DropResult, error) {
	wl, err := lock.AcquireWrite(ctx, e.writeLockPath())
	if err != nil {
		return DropResult{}, err
	}
	defer func() { _ = wl.Release() }()

	target, err := e.Resolve(ctx, ref)
	if err != nil {
		return DropResult{}, err
	}
	g, err := e.loadGraph(ctx)
	if err != nil {
		return DropResult{}, err
	}
	sub := g.subtree(target)

	head, err := e.q.GetHead(ctx)
	if err != nil {
		return DropResult{}, fmt.Errorf("reading HEAD: %w", err)
	}
	res := DropResult{Target: target}

	headInSub := false
	if head.Valid {
		_, headInSub = sub[head.String]
	}
	if headInSub {
		if parent := g.byID[target].ParentID; parent.Valid {
			// Relocate HEAD to the target's parent, force-settling and materializing
			// so the working tree matches the surviving state.
			if _, err := e.goToLocked(ctx, parent.String); err != nil {
				return DropResult{}, fmt.Errorf("relocating HEAD before drop: %w", err)
			}
			res.HeadMovedTo = parent.String
			// The force-settle may have recorded a state under the old HEAD (inside
			// the subtree); reload so it is deleted too.
			g, err = e.loadGraph(ctx)
			if err != nil {
				return DropResult{}, err
			}
			sub = g.subtree(target)
		} else {
			// Pruning the root you are on: no parent to move to. HEAD clears via its
			// ON DELETE SET NULL foreign key, and working files are left untouched.
			res.HeadCleared = true
		}
	}

	order := g.deletionOrder(sub)
	if err := e.deleteStates(ctx, order); err != nil {
		return DropResult{}, err
	}
	res.Deleted = len(order)

	gc, err := e.gcLocked(ctx)
	if err != nil {
		return DropResult{}, fmt.Errorf("reclaiming blobs: %w", err)
	}
	res.Reclaimed = gc
	return res, nil
}
