package core

import (
	"context"
	"testing"
)

// parentOf returns a state's parent id ("" for a root or missing state).
func parentOf(t *testing.T, eng *Engine, id string) string {
	t.Helper()
	g, err := eng.loadGraph(context.Background())
	if err != nil {
		t.Fatalf("loadGraph: %v", err)
	}
	s, ok := g.byID[id]
	if !ok {
		t.Fatalf("state %s not found", id)
	}
	if !s.ParentID.Valid {
		return ""
	}
	return s.ParentID.String
}

// TestKeepfromKeepsSubtreeDropsAncestors reroots to a mid state while HEAD is a
// descendant: ancestors are dropped, HEAD stays, and the target becomes a root.
func TestKeepfromKeepsSubtreeDropsAncestors(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "f.txt", "A")
	snap(t, eng)
	write(t, root, "f.txt", "B")
	s2 := snap(t, eng)
	write(t, root, "f.txt", "C")
	s3 := snap(t, eng) // s1 -> s2 -> s3, HEAD = s3

	plan, err := eng.KeepfromPlan(ctx, s2)
	if err != nil {
		t.Fatalf("KeepfromPlan: %v", err)
	}
	if plan.StatesToDrop != 1 || plan.StatesKept != 2 || plan.HeadWillMove || plan.IsNoop {
		t.Fatalf("plan = %+v, want drop 1 keep 2, HEAD staying", plan)
	}

	res, err := eng.Keepfrom(ctx, s2)
	if err != nil {
		t.Fatalf("Keepfrom: %v", err)
	}
	if res.Dropped != 1 || res.Kept != 2 || res.HeadMovedTo != "" {
		t.Fatalf("res = %+v, want Dropped 1 Kept 2, HEAD unchanged", res)
	}
	if headID(t, eng) != s3 {
		t.Errorf("HEAD = %s, want %s", headID(t, eng), s3)
	}
	if p := parentOf(t, eng, s2); p != "" {
		t.Errorf("s2 parent = %q, want empty (new root)", p)
	}
	if got := readWorking(t, root); got != "C" {
		t.Errorf("working f.txt = %q, want C (unchanged)", got)
	}
	if n := countStates(t, eng); n != 2 {
		t.Errorf("states = %d, want 2", n)
	}
	if res.Reclaimed.Removed < 1 {
		t.Errorf("expected the dropped ancestor's blob to be reclaimed, got %d", res.Reclaimed.Removed)
	}
	mustVerifyClean(t, eng)
}

// TestKeepfromRelocatesHeadOnDroppedBranch keeps from a state on a different branch
// than HEAD: HEAD is moved to the new root and the working tree re-materialized.
func TestKeepfromRelocatesHeadOnDroppedBranch(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "f.txt", "A")
	s1 := snap(t, eng)
	write(t, root, "f.txt", "B")
	s2 := snap(t, eng)
	if _, err := eng.Go(ctx, s1); err != nil {
		t.Fatalf("Go: %v", err)
	}
	write(t, root, "f.txt", "C")
	snap(t, eng) // s1 -> {s2, s3}, HEAD = s3

	plan, err := eng.KeepfromPlan(ctx, s2)
	if err != nil {
		t.Fatalf("KeepfromPlan: %v", err)
	}
	if !plan.HeadWillMove || plan.StatesKept != 1 || plan.StatesToDrop != 2 {
		t.Fatalf("plan = %+v, want HEAD moving, keep 1 drop 2", plan)
	}

	res, err := eng.Keepfrom(ctx, s2)
	if err != nil {
		t.Fatalf("Keepfrom: %v", err)
	}
	if res.HeadMovedTo != s2 || res.Kept != 1 || res.Dropped != 2 {
		t.Fatalf("res = %+v, want HEAD -> %s, keep 1 drop 2", res, s2)
	}
	if headID(t, eng) != s2 {
		t.Errorf("HEAD = %s, want %s", headID(t, eng), s2)
	}
	if got := readWorking(t, root); got != "B" {
		t.Errorf("working f.txt = %q, want B (materialized new root)", got)
	}
	if n := countStates(t, eng); n != 1 {
		t.Errorf("states = %d, want 1", n)
	}
	mustVerifyClean(t, eng)
}

// TestKeepfromNoopAtRoot reroots to the root of the whole history: nothing changes.
func TestKeepfromNoopAtRoot(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "f.txt", "A")
	s1 := snap(t, eng)
	write(t, root, "f.txt", "B")
	s2 := snap(t, eng)

	plan, err := eng.KeepfromPlan(ctx, s1)
	if err != nil {
		t.Fatalf("KeepfromPlan: %v", err)
	}
	if !plan.IsNoop {
		t.Fatalf("plan = %+v, want IsNoop", plan)
	}

	res, err := eng.Keepfrom(ctx, s1)
	if err != nil {
		t.Fatalf("Keepfrom: %v", err)
	}
	if res.Dropped != 0 {
		t.Errorf("Dropped = %d, want 0", res.Dropped)
	}
	if headID(t, eng) != s2 {
		t.Errorf("HEAD = %s, want %s (unchanged)", headID(t, eng), s2)
	}
	if n := countStates(t, eng); n != 2 {
		t.Errorf("states = %d, want 2", n)
	}
	mustVerifyClean(t, eng)
}
