package core

import (
	"context"
	"testing"
)

// TestUndoRedoRoundTrip checks that undo steps back and materializes the older
// state, and redo returns forward to the original tip.
func TestUndoRedoRoundTrip(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "f", "A")
	a := snapID(t, eng)
	write(t, root, "f", "B")
	snapID(t, eng)
	write(t, root, "f", "C")
	c := snapID(t, eng)

	// undo 2 lands on A and rewrites the working tree.
	res, err := eng.Undo(ctx, 2)
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if res.Steps != 2 || res.StateID != a {
		t.Fatalf("Undo = %+v, want 2 steps to %s", res, a)
	}
	if got := readFile(t, root, "f"); got != "A" {
		t.Fatalf("f = %q, want A", got)
	}
	if h := mustHead(t, eng); h != a {
		t.Fatalf("HEAD = %s, want %s", h, a)
	}

	// redo 2 returns to C.
	res, err = eng.Redo(ctx, 2)
	if err != nil {
		t.Fatalf("Redo: %v", err)
	}
	if res.Steps != 2 || res.StateID != c {
		t.Fatalf("Redo = %+v, want 2 steps to %s", res, c)
	}
	if got := readFile(t, root, "f"); got != "C" {
		t.Fatalf("f = %q, want C", got)
	}
}

// TestUndoClampsAtRoot checks that overshooting undo lands on the root and
// reports the real step count, and a further undo is a no-op that records
// nothing.
func TestUndoClampsAtRoot(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "f", "A")
	a := snapID(t, eng)
	write(t, root, "f", "B")
	snapID(t, eng)
	write(t, root, "f", "C")
	snapID(t, eng)

	res, err := eng.Undo(ctx, 100)
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if res.Steps != 2 || res.StateID != a {
		t.Fatalf("Undo(100) = %+v, want 2 steps to root %s", res, a)
	}

	before, err := eng.Log(ctx)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	res, err = eng.Undo(ctx, 1)
	if err != nil {
		t.Fatalf("Undo at root: %v", err)
	}
	if res.Steps != 0 || res.StateID != a {
		t.Fatalf("Undo at root = %+v, want 0 steps staying on %s", res, a)
	}
	after, err := eng.Log(ctx)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(after.States) != len(before.States) {
		t.Fatalf("a boundary undo changed the state count %d -> %d",
			len(before.States), len(after.States))
	}
}

// TestRedoAtTipIsNoOp checks redo at a leaf reports zero steps.
func TestRedoAtTipIsNoOp(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "f", "A")
	snapID(t, eng)

	res, err := eng.Redo(ctx, 1)
	if err != nil {
		t.Fatalf("Redo: %v", err)
	}
	if res.Steps != 0 {
		t.Fatalf("Redo at tip = %+v, want 0 steps", res)
	}
}

// TestUndoForceSettlesEdit checks that an uncommitted edit is recorded as its own
// state before undo moves away, so nothing is lost.
func TestUndoForceSettlesEdit(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "f", "A")
	a := snapID(t, eng)
	write(t, root, "f", "B")
	b := snapID(t, eng)

	// Edit without snapshotting, then undo.
	write(t, root, "f", "uncommitted")
	res, err := eng.Undo(ctx, 1)
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if res.Steps != 1 || res.StateID != a {
		t.Fatalf("Undo = %+v, want 1 step to %s", res, a)
	}
	if !res.Settled || res.SettledID == "" {
		t.Fatalf("the uncommitted edit should have been force-settled: %+v", res)
	}
	if got := readFile(t, root, "f"); got != "A" {
		t.Fatalf("f = %q, want A", got)
	}
	// The settled state holds the edit and hangs off b (the pre-undo tip).
	if got := execManifestContent(t, eng, res.SettledID, "f"); got != "uncommitted" {
		t.Fatalf("settled state f = %q, want the uncommitted edit", got)
	}
	if p := stateParent(t, eng, res.SettledID); p != b {
		t.Fatalf("settled state parent = %s, want %s", p, b)
	}
}

// TestRedoFollowsMostRecentBranch checks that when a state has several children,
// redo returns to the one most recently visited, not just the oldest.
func TestRedoFollowsMostRecentBranch(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "f", "A")
	a := snapID(t, eng)
	write(t, root, "f", "B")
	snapID(t, eng) // first child of A: B

	// Undo to A, then create a second child C by editing and snapshotting.
	if _, err := eng.Undo(ctx, 1); err != nil {
		t.Fatalf("Undo to A: %v", err)
	}
	write(t, root, "f", "C")
	c := snapID(t, eng) // second child of A: C, now the most recently visited

	// Undo back to A; redo must pick C (most recently visited child), not B.
	if _, err := eng.Undo(ctx, 1); err != nil {
		t.Fatalf("Undo to A again: %v", err)
	}
	if h := mustHead(t, eng); h != a {
		t.Fatalf("HEAD = %s, want %s (A)", h, a)
	}
	res, err := eng.Redo(ctx, 1)
	if err != nil {
		t.Fatalf("Redo: %v", err)
	}
	if res.StateID != c {
		t.Fatalf("Redo landed on %s, want most-recent child %s", res.StateID, c)
	}
	if got := readFile(t, root, "f"); got != "C" {
		t.Fatalf("f = %q, want C", got)
	}
}

// stateParent reads the parent id of a state.
func stateParent(t *testing.T, eng *Engine, id string) string {
	t.Helper()
	var parent string
	if err := eng.db.QueryRowContext(context.Background(),
		`SELECT COALESCE(parent_id, '') FROM states WHERE id = ?`, id).Scan(&parent); err != nil {
		t.Fatalf("reading parent of %s: %v", id, err)
	}
	return parent
}
