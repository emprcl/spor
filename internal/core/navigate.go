package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/emprcl/spor/internal/lock"
)

// MoveResult reports a HEAD move by undo or redo. Steps is how many states it
// actually moved: fewer than requested when the history boundary was reached,
// and 0 when already at the boundary (in which case nothing was recorded and the
// embedded GoResult only carries the current StateID).
type MoveResult struct {
	GoResult
	Steps int
}

// Undo steps HEAD back n states along @'s ancestor line and materializes the
// result, staying reversible via redo (docs/design-spec.md §6). It clamps: asking for
// more than the history holds simply lands on the root. Like restore it
// force-settles first, so an uncommitted edit becomes a branch rather than being
// lost. n < 1 is treated as 1.
func (e *Engine) Undo(ctx context.Context, n int) (MoveResult, error) {
	return e.move(ctx, n, e.undoTargetLocked)
}

// Redo steps HEAD forward n states, each step following the most-recently-visited
// child of the current state as recorded by the HEAD journal (docs/design-spec.md §2,
// §6). It clamps at a leaf. Because editing after an undo starts a new branch,
// redo reaches only the branch it last left; other branches are reached via log +
// restore. n < 1 is treated as 1.
func (e *Engine) Redo(ctx context.Context, n int) (MoveResult, error) {
	return e.move(ctx, n, e.redoTargetLocked)
}

// move is the shared body of Undo and Redo: under the write lock it computes a
// target with the given walker (before force-settling, so the walker sees the
// pre-move tree and journal), then reuses the restore machinery to get there. A
// zero-step walk is a no-op: no snapshot, no HEAD move, no journal entry.
func (e *Engine) move(
	ctx context.Context,
	n int,
	target func(context.Context, int) (string, int, error),
) (MoveResult, error) {
	if n < 1 {
		n = 1
	}
	wl, err := lock.AcquireWrite(ctx, e.writeLockPath())
	if err != nil {
		return MoveResult{}, err
	}
	defer func() { _ = wl.Release() }()

	id, steps, err := target(ctx, n)
	if err != nil {
		return MoveResult{}, err
	}
	if steps == 0 {
		return MoveResult{GoResult: GoResult{StateID: id}}, nil
	}
	res, err := e.goToLocked(ctx, id)
	if err != nil {
		return MoveResult{}, err
	}
	return MoveResult{GoResult: res, Steps: steps}, nil
}

// undoTargetLocked walks up to n parent links from HEAD, stopping early at the
// root. It returns the state landed on and how many steps it took (0 when HEAD is
// already the root or unset).
func (e *Engine) undoTargetLocked(ctx context.Context, n int) (string, int, error) {
	head, err := e.q.GetHead(ctx)
	if err != nil {
		return "", 0, fmt.Errorf("reading HEAD: %w", err)
	}
	if !head.Valid {
		return "", 0, nil
	}
	cur := head.String
	steps := 0
	for steps < n {
		parent, err := e.q.GetStateParent(ctx, cur)
		if err != nil {
			return "", 0, fmt.Errorf("reading parent of %s: %w", cur, err)
		}
		if !parent.Valid { // reached the root
			break
		}
		cur = parent.String
		steps++
	}
	return cur, steps, nil
}

// redoTargetLocked walks up to n steps forward from HEAD, each time taking the
// most-recently-visited child, stopping early at a leaf. It returns the state
// landed on and how many steps it took (0 when HEAD is already a leaf or unset).
func (e *Engine) redoTargetLocked(ctx context.Context, n int) (string, int, error) {
	head, err := e.q.GetHead(ctx)
	if err != nil {
		return "", 0, fmt.Errorf("reading HEAD: %w", err)
	}
	if !head.Valid {
		return "", 0, nil
	}
	cur := head.String
	steps := 0
	for steps < n {
		child, err := e.q.MostRecentChild(ctx, sql.NullString{String: cur, Valid: true})
		if errors.Is(err, sql.ErrNoRows) { // reached a leaf
			break
		}
		if err != nil {
			return "", 0, fmt.Errorf("reading child of %s: %w", cur, err)
		}
		cur = child
		steps++
	}
	return cur, steps, nil
}
