package core

import (
	"context"
	"strings"
	"testing"
)

// TestFoldLinearRangeBelowHead folds an interior range while HEAD is a descendant
// below it: the intermediates collapse into one state holding B's content, HEAD and
// the working tree are untouched, and B's child reattaches to the folded state.
func TestFoldLinearRangeBelowHead(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "f.txt", "v1")
	s1 := snap(t, eng)
	write(t, root, "f.txt", "v2")
	snap(t, eng)
	write(t, root, "f.txt", "v3")
	s3 := snap(t, eng)
	write(t, root, "f.txt", "v4")
	s4 := snap(t, eng) // s1 -> s2 -> s3 -> s4, HEAD = s4

	plan, err := eng.FoldPlan(ctx, s1, s3)
	if err != nil {
		t.Fatalf("FoldPlan: %v", err)
	}
	if plan.StatesFolded != 3 || plan.HeadWillMove {
		t.Fatalf("plan = %+v, want 3 states folded, HEAD staying", plan)
	}

	res, err := eng.Fold(ctx, s1, s3)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if res.Dropped != 3 || res.HeadMovedTo != "" {
		t.Fatalf("res = %+v, want Dropped 3, HEAD unchanged", res)
	}
	if headID(t, eng) != s4 {
		t.Errorf("HEAD = %s, want %s (unchanged)", headID(t, eng), s4)
	}
	if got := readWorking(t, root); got != "v4" {
		t.Errorf("working f.txt = %q, want v4 (untouched)", got)
	}
	if n := countStates(t, eng); n != 2 {
		t.Errorf("states = %d, want 2 (folded state + s4)", n)
	}
	if p := parentOf(t, eng, s4); p != res.Folded {
		t.Errorf("s4 parent = %q, want folded state %s", p, res.Folded)
	}
	if p := parentOf(t, eng, res.Folded); p != "" {
		t.Errorf("folded state parent = %q, want empty (was root's parent)", p)
	}
	// The folded state carries B's (s3's) content: jump to it and check the tree.
	if _, err := eng.Go(ctx, res.Folded); err != nil {
		t.Fatalf("Go to folded state: %v", err)
	}
	if got := readWorking(t, root); got != "v3" {
		t.Errorf("folded state content = %q, want v3 (B's content)", got)
	}
	if res.Reclaimed.Removed < 1 {
		t.Errorf("expected the dropped intermediates' blobs to be reclaimed, got %d", res.Reclaimed.Removed)
	}
	mustVerifyClean(t, eng)
}

// TestFoldEndingAtHeadMovesHead folds a range ending at HEAD: HEAD moves onto the
// folded state and the whole range collapses to a single root.
func TestFoldEndingAtHeadMovesHead(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "f.txt", "v1")
	s1 := snap(t, eng)
	write(t, root, "f.txt", "v2")
	snap(t, eng)
	write(t, root, "f.txt", "v3")
	s3 := snap(t, eng) // s1 -> s2 -> s3, HEAD = s3

	plan, err := eng.FoldPlan(ctx, s1, s3)
	if err != nil {
		t.Fatalf("FoldPlan: %v", err)
	}
	if !plan.HeadWillMove || plan.StatesFolded != 3 {
		t.Fatalf("plan = %+v, want HEAD moving, 3 states folded", plan)
	}

	res, err := eng.Fold(ctx, s1, s3)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if res.Dropped != 3 || res.HeadMovedTo != res.Folded {
		t.Fatalf("res = %+v, want Dropped 3 and HEAD -> folded", res)
	}
	if headID(t, eng) != res.Folded {
		t.Errorf("HEAD = %s, want folded state %s", headID(t, eng), res.Folded)
	}
	if got := readWorking(t, root); got != "v3" {
		t.Errorf("working f.txt = %q, want v3 (folded content)", got)
	}
	if n := countStates(t, eng); n != 1 {
		t.Errorf("states = %d, want 1", n)
	}
	if p := parentOf(t, eng, res.Folded); p != "" {
		t.Errorf("folded state parent = %q, want empty (new root)", p)
	}
	mustVerifyClean(t, eng)
}

// TestFoldRefusesNonLinearRange refuses when an interior state of the range has a
// child off to the side, and leaves history untouched.
func TestFoldRefusesNonLinearRange(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "f.txt", "v1")
	s1 := snap(t, eng)
	write(t, root, "f.txt", "v2")
	s2 := snap(t, eng)
	write(t, root, "f.txt", "v3")
	s3 := snap(t, eng)
	if _, err := eng.Go(ctx, s2); err != nil {
		t.Fatalf("Go: %v", err)
	}
	write(t, root, "f.txt", "v2b")
	snap(t, eng) // s2 -> {s3, s4}, HEAD = s4

	if _, err := eng.FoldPlan(ctx, s1, s3); err == nil {
		t.Fatalf("FoldPlan: want error for non-linear range")
	}
	_, err := eng.Fold(ctx, s1, s3)
	if err == nil {
		t.Fatalf("Fold: want error for non-linear range")
	}
	if !strings.Contains(err.Error(), "not linear") {
		t.Errorf("error = %v, want it to mention the range is not linear", err)
	}
	if n := countStates(t, eng); n != 4 {
		t.Errorf("states = %d, want 4 (unchanged after refusal)", n)
	}
	mustVerifyClean(t, eng)
}

// TestFoldRejectsBadRange rejects a degenerate (a == b) range and a reversed one
// (a is a descendant of b, not an ancestor).
func TestFoldRejectsBadRange(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "f.txt", "v1")
	s1 := snap(t, eng)
	write(t, root, "f.txt", "v2")
	s2 := snap(t, eng)

	if _, err := eng.Fold(ctx, s1, s1); err == nil {
		t.Errorf("Fold(a, a): want error for a degenerate range")
	}
	if _, err := eng.Fold(ctx, s2, s1); err == nil {
		t.Errorf("Fold(descendant, ancestor): want error, a must be the older state")
	}
	if n := countStates(t, eng); n != 2 {
		t.Errorf("states = %d, want 2 (unchanged)", n)
	}
	mustVerifyClean(t, eng)
}
