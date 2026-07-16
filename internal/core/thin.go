package core

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/emprcl/spor/internal/db/gen"
	"github.com/emprcl/spor/internal/lock"
)

// ThinPlan previews what a thin would drop, for the confirmation prompt.
type ThinPlan struct {
	StatesKept   int
	StatesToDrop int
	IsNoop       bool // history is already all tips, branch points, and labels
}

// ThinPlan reports what thinning the whole history would drop, without changing
// anything.
func (e *Engine) ThinPlan(ctx context.Context) (ThinPlan, error) {
	g, err := e.loadGraph(ctx)
	if err != nil {
		return ThinPlan{}, err
	}
	head, err := e.q.GetHead(ctx)
	if err != nil {
		return ThinPlan{}, fmt.Errorf("reading HEAD: %w", err)
	}
	keep := g.thinKeepSet(head)
	plan := ThinPlan{
		StatesKept:   len(keep),
		StatesToDrop: len(g.states) - len(keep),
	}
	plan.IsNoop = plan.StatesToDrop == 0
	return plan, nil
}

// ThinResult reports the outcome of a thin.
type ThinResult struct {
	Dropped   int
	Kept      int
	Reclaimed GCResult
}

// Thin reduces the history to its structural skeleton (docs/design-spec.md §5):
// it keeps every tip (a state with no children), every branch point (two or more
// children), every labeled state, and HEAD, and drops the linear in-between
// states, reparenting each survivor onto its nearest surviving ancestor. It is
// the persistent form of the folding `spor log` already does at display time,
// and it spares exactly the states that folding does (labeled and @).
//
// Because HEAD is always kept, thin never moves HEAD and never touches the
// working tree, so unlike drop/trim/fold it needs no force-settle or
// materialize. Like them it is destructive but never rewriting: no surviving
// state's contents change, only which states exist and the survivors' parent
// links. It is idempotent: a second run finds only tips and branch points (plus
// labels and @) and drops nothing.
func (e *Engine) Thin(ctx context.Context) (ThinResult, error) {
	wl, err := lock.AcquireWrite(ctx, e.writeLockPath())
	if err != nil {
		return ThinResult{}, err
	}
	defer func() { _ = wl.Release() }()

	g, err := e.loadGraph(ctx)
	if err != nil {
		return ThinResult{}, err
	}
	head, err := e.q.GetHead(ctx)
	if err != nil {
		return ThinResult{}, fmt.Errorf("reading HEAD: %w", err)
	}
	keep := g.thinKeepSet(head)

	drop := make(map[string]struct{}, len(g.states)-len(keep))
	for _, s := range g.states {
		if _, ok := keep[s.ID]; !ok {
			drop[s.ID] = struct{}{}
		}
	}
	if len(drop) == 0 {
		return ThinResult{Kept: len(keep)}, nil // already thinned
	}

	// Each surviving state whose parent is being dropped reparents onto its
	// nearest surviving ancestor, or becomes a root when the whole chain above it
	// is dropped. Computed against the pre-thin graph; only survivors move, so the
	// dropped nodes' own parent/child links (which drive the deletion order below)
	// stay accurate.
	reparents := make(map[string]sql.NullString)
	for id := range keep {
		parent := g.byID[id].ParentID
		if !parent.Valid {
			continue // already a root
		}
		if _, dropped := drop[parent.String]; !dropped {
			continue // parent survives; the link is unchanged
		}
		reparents[id] = g.nearestKept(parent, drop)
	}

	order := g.deletionOrder(drop)
	if err := e.thinCommit(ctx, reparents, order); err != nil {
		return ThinResult{}, err
	}

	res := ThinResult{Dropped: len(order), Kept: len(keep)}
	gc, err := e.gcLocked(ctx)
	if err != nil {
		return ThinResult{}, fmt.Errorf("reclaiming blobs: %w", err)
	}
	res.Reclaimed = gc
	return res, nil
}

// thinKeepSet returns the ids thin preserves: every tip (no children) and branch
// point (two or more children), plus every labeled state and HEAD (mirroring the
// nodes `spor log` never folds away). Everything else has exactly one child and
// is dropped.
func (g *stateGraph) thinKeepSet(head sql.NullString) map[string]struct{} {
	keep := make(map[string]struct{})
	for _, s := range g.states {
		isHead := head.Valid && s.ID == head.String
		if len(g.children[s.ID]) != 1 || s.Label.Valid || isHead {
			keep[s.ID] = struct{}{}
		}
	}
	return keep
}

// nearestKept walks up from a dropped ancestor to the first state not in drop,
// the new parent for a survivor whose immediate parent is being removed. It
// returns a null parent (a new root) when the whole ancestor chain is dropped.
func (g *stateGraph) nearestKept(from sql.NullString, drop map[string]struct{}) sql.NullString {
	for p := from; p.Valid; p = g.byID[p.String].ParentID {
		if _, dropped := drop[p.String]; !dropped {
			return p
		}
	}
	return sql.NullString{}
}

// thinCommit reparents the survivors and deletes the dropped states in one
// transaction. Reparenting runs first so that when the dropped states are
// removed (children before parents, per order), no surviving child still points
// at one and the parent_id foreign key is never violated.
func (e *Engine) thinCommit(ctx context.Context, reparents map[string]sql.NullString, order []string) error {
	return e.inTx(ctx, func(q *gen.Queries) error {
		for id, parent := range reparents {
			if parent.Valid {
				if err := q.SetStateParent(ctx, gen.SetStateParentParams{ParentID: parent, ID: id}); err != nil {
					return fmt.Errorf("reparenting %s: %w", id, err)
				}
			} else if err := q.SetStateParentNull(ctx, id); err != nil {
				return fmt.Errorf("detaching %s: %w", id, err)
			}
		}
		for _, id := range order {
			if err := q.DeleteState(ctx, id); err != nil {
				return fmt.Errorf("deleting state %s: %w", id, err)
			}
		}
		return nil
	})
}
