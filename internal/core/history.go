package core

import (
	"context"
	"fmt"

	"github.com/emprcl/spor/internal/db/gen"
)

// stateGraph is an in-memory view of the state tree, used by the history-editing
// operations (drop, trim) to compute subtrees and safe deletion orders.
type stateGraph struct {
	states   []gen.ListStatesRow
	byID     map[string]gen.ListStatesRow
	children map[string][]string // parent id -> child ids, in listing order
}

// loadGraph reads every state and builds the parent/child index.
func (e *Engine) loadGraph(ctx context.Context) (*stateGraph, error) {
	states, err := e.q.ListStates(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing states: %w", err)
	}
	g := &stateGraph{
		states:   states,
		byID:     make(map[string]gen.ListStatesRow, len(states)),
		children: make(map[string][]string),
	}
	for _, s := range states {
		g.byID[s.ID] = s
	}
	for _, s := range states {
		if s.ParentID.Valid {
			if _, ok := g.byID[s.ParentID.String]; ok {
				g.children[s.ParentID.String] = append(g.children[s.ParentID.String], s.ID)
			}
		}
	}
	return g, nil
}

// subtree returns root and all of its descendants.
func (g *stateGraph) subtree(root string) map[string]struct{} {
	set := make(map[string]struct{})
	var walk func(id string)
	walk = func(id string) {
		set[id] = struct{}{}
		for _, c := range g.children[id] {
			walk(c)
		}
	}
	walk(root)
	return set
}

// rangePath returns the states on the path from descendant up to ancestor,
// inclusive, ordered descendant-first (so it doubles as a children-before-parents
// deletion order). ok is false when ancestor is not on descendant's ancestor line,
// i.e. the walk reaches a root without meeting it.
func (g *stateGraph) rangePath(ancestor, descendant string) (path []string, ok bool) {
	for cur := descendant; ; {
		path = append(path, cur)
		if cur == ancestor {
			return path, true
		}
		row, exists := g.byID[cur]
		if !exists || !row.ParentID.Valid {
			return nil, false
		}
		cur = row.ParentID.String
	}
}

// deletionOrder returns the ids in del ordered children-before-parents, so the
// ON DELETE RESTRICT self-foreign-key on states.parent_id is never violated. It
// is a post-order over the whole forest, filtered to del.
func (g *stateGraph) deletionOrder(del map[string]struct{}) []string {
	var roots []string
	for _, s := range g.states {
		if !s.ParentID.Valid || g.byID[s.ParentID.String].ID == "" {
			roots = append(roots, s.ID)
		}
	}
	order := make([]string, 0, len(del))
	var walk func(id string)
	walk = func(id string) {
		for _, c := range g.children[id] {
			walk(c)
		}
		if _, ok := del[id]; ok {
			order = append(order, id)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return order
}

// deleteStates removes the given states in one transaction, in the order given
// (which must be children-before-parents). Manifest entries and head-journal rows
// are removed by ON DELETE CASCADE; HEAD is set NULL by its own foreign key if it
// pointed into the deleted set.
func (e *Engine) deleteStates(ctx context.Context, order []string) error {
	return e.inTx(ctx, func(q *gen.Queries) error {
		for _, id := range order {
			if err := q.DeleteState(ctx, id); err != nil {
				return fmt.Errorf("deleting state %s: %w", id, err)
			}
		}
		return nil
	})
}

// inTx runs fn inside a transaction, committing on success and rolling back on
// error.
func (e *Engine) inTx(ctx context.Context, fn func(q *gen.Queries) error) error {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit
	if err := fn(e.q.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit()
}
