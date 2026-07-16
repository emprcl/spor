package core

import (
	"context"
	"testing"
)

// TestThinKeepsBranchPointsAndTips reduces a branching history: the two tips and
// the single branch point survive, the linear in-between states are dropped, and
// the survivors reconnect, all without moving HEAD or the working tree.
func TestThinKeepsBranchPointsAndTips(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "f.txt", "A")
	s1 := snap(t, eng)
	write(t, root, "f.txt", "B")
	s2 := snap(t, eng)
	write(t, root, "f.txt", "C")
	s3 := snap(t, eng)
	write(t, root, "f.txt", "D")
	s4 := snap(t, eng)
	// Branch off s3 to make it a branch point.
	if _, err := eng.Go(ctx, s3); err != nil {
		t.Fatalf("Go: %v", err)
	}
	write(t, root, "f.txt", "E")
	s5 := snap(t, eng)
	write(t, root, "f.txt", "F")
	s6 := snap(t, eng)
	// Tree: s1 -> s2 -> s3 -> s4 (tip), s3 -> s5 -> s6 (tip); HEAD = s6.
	// Keep s3 (branch), s4 and s6 (tips); drop s1, s2, s5 (linear interior).

	plan, err := eng.ThinPlan(ctx)
	if err != nil {
		t.Fatalf("ThinPlan: %v", err)
	}
	if plan.StatesKept != 3 || plan.StatesToDrop != 3 || plan.IsNoop {
		t.Fatalf("plan = %+v, want keep 3 drop 3", plan)
	}

	res, err := eng.Thin(ctx)
	if err != nil {
		t.Fatalf("Thin: %v", err)
	}
	if res.Dropped != 3 || res.Kept != 3 {
		t.Fatalf("res = %+v, want Dropped 3 Kept 3", res)
	}
	if headID(t, eng) != s6 {
		t.Errorf("HEAD = %s, want %s (unchanged)", headID(t, eng), s6)
	}
	if got := readWorking(t, root); got != "F" {
		t.Errorf("working f.txt = %q, want F (untouched)", got)
	}
	if n := countStates(t, eng); n != 3 {
		t.Errorf("states = %d, want 3", n)
	}
	// The branch point is now a root; both tips hang directly off it.
	if p := parentOf(t, eng, s3); p != "" {
		t.Errorf("s3 parent = %q, want empty (new root)", p)
	}
	if p := parentOf(t, eng, s4); p != s3 {
		t.Errorf("s4 parent = %q, want %s", p, s3)
	}
	if p := parentOf(t, eng, s6); p != s3 {
		t.Errorf("s6 parent = %q, want %s", p, s3)
	}
	if res.Reclaimed.Removed < 1 {
		t.Errorf("expected dropped states' blobs to be reclaimed, got %d", res.Reclaimed.Removed)
	}
	mustVerifyClean(t, eng)

	// Idempotent: nothing left to thin.
	plan2, err := eng.ThinPlan(ctx)
	if err != nil {
		t.Fatalf("ThinPlan #2: %v", err)
	}
	if !plan2.IsNoop {
		t.Errorf("second ThinPlan = %+v, want IsNoop", plan2)
	}
	res2, err := eng.Thin(ctx)
	if err != nil {
		t.Fatalf("Thin #2: %v", err)
	}
	if res2.Dropped != 0 {
		t.Errorf("second Thin Dropped = %d, want 0", res2.Dropped)
	}
	_ = s1
	_ = s2
	_ = s5
}

// TestThinKeepsLabeledInterior keeps a labeled single-child state that the plain
// tip/branch rule would otherwise drop, mirroring how `spor log` never folds a
// named snapshot away.
func TestThinKeepsLabeledInterior(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "f.txt", "A")
	s1 := snap(t, eng)
	write(t, root, "f.txt", "B")
	s2 := snap(t, eng)
	write(t, root, "f.txt", "C")
	s3 := snap(t, eng)
	if _, err := eng.Label(ctx, s2, "mid"); err != nil {
		t.Fatalf("Label: %v", err)
	}
	// Linear s1 -> s2 ("mid") -> s3; HEAD = s3. Keep s2 (labeled) and s3 (tip),
	// drop s1.

	res, err := eng.Thin(ctx)
	if err != nil {
		t.Fatalf("Thin: %v", err)
	}
	if res.Dropped != 1 || res.Kept != 2 {
		t.Fatalf("res = %+v, want Dropped 1 Kept 2", res)
	}
	if _, ok := findState(t, eng, s2); !ok {
		t.Errorf("labeled state %s was dropped, want kept", s2)
	}
	if p := parentOf(t, eng, s2); p != "" {
		t.Errorf("s2 parent = %q, want empty (new root)", p)
	}
	if p := parentOf(t, eng, s3); p != s2 {
		t.Errorf("s3 parent = %q, want %s", p, s2)
	}
	mustVerifyClean(t, eng)
	_ = s1
}

// TestThinLinearCollapsesToTip reduces a branch-free history to just its latest
// snapshot, leaving the working tree untouched.
func TestThinLinearCollapsesToTip(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "f.txt", "A")
	snap(t, eng)
	write(t, root, "f.txt", "B")
	snap(t, eng)
	write(t, root, "f.txt", "C")
	s3 := snap(t, eng) // s1 -> s2 -> s3, HEAD = s3

	res, err := eng.Thin(ctx)
	if err != nil {
		t.Fatalf("Thin: %v", err)
	}
	if res.Dropped != 2 || res.Kept != 1 {
		t.Fatalf("res = %+v, want Dropped 2 Kept 1", res)
	}
	if n := countStates(t, eng); n != 1 {
		t.Errorf("states = %d, want 1", n)
	}
	if headID(t, eng) != s3 {
		t.Errorf("HEAD = %s, want %s (unchanged)", headID(t, eng), s3)
	}
	if p := parentOf(t, eng, s3); p != "" {
		t.Errorf("s3 parent = %q, want empty (sole root)", p)
	}
	if got := readWorking(t, root); got != "C" {
		t.Errorf("working f.txt = %q, want C (untouched)", got)
	}
	mustVerifyClean(t, eng)
}

// findState reports whether a state id still exists in the graph.
func findState(t *testing.T, eng *Engine, id string) (string, bool) {
	t.Helper()
	g, err := eng.loadGraph(context.Background())
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	_, ok := g.byID[id]
	return id, ok
}
