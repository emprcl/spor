package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// headID returns the current HEAD state id ("" when unset).
func headID(t *testing.T, eng *Engine) string {
	t.Helper()
	h, err := eng.q.GetHead(context.Background())
	if err != nil {
		t.Fatalf("GetHead: %v", err)
	}
	if !h.Valid {
		return ""
	}
	return h.String
}

// readWorking reads the "f.txt" working-tree file used by the history tests.
func readWorking(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "f.txt"))
	if err != nil {
		t.Fatalf("read f.txt: %v", err)
	}
	return string(b)
}

// countStates returns how many states remain.
func countStates(t *testing.T, eng *Engine) int {
	t.Helper()
	res, err := eng.Log(context.Background())
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	return len(res.States)
}

func mustVerifyClean(t *testing.T, eng *Engine) {
	t.Helper()
	res, err := eng.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !res.OK() {
		t.Fatalf("store not intact after operation: %+v", res.Issues)
	}
}

// TestPruneLeafMovesHeadToParent prunes the current leaf state: HEAD moves to the
// parent, the working tree is re-materialized, and the leaf's blob is reclaimed.
func TestPruneLeafMovesHeadToParent(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "f.txt", "A")
	s1 := snap(t, eng)
	write(t, root, "f.txt", "B")
	s2 := snap(t, eng)

	plan, err := eng.PrunePlan(ctx, s2)
	if err != nil {
		t.Fatalf("PrunePlan: %v", err)
	}
	if plan.StatesToDelete != 1 || !plan.HeadWillMove || plan.HeadTarget != s1 {
		t.Fatalf("plan = %+v, want delete 1, HEAD -> %s", plan, s1)
	}

	res, err := eng.Prune(ctx, s2)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if res.Deleted != 1 || res.HeadMovedTo != s1 {
		t.Fatalf("res = %+v, want Deleted 1, HeadMovedTo %s", res, s1)
	}
	if headID(t, eng) != s1 {
		t.Errorf("HEAD = %s, want %s", headID(t, eng), s1)
	}
	if got := readWorking(t, root); got != "A" {
		t.Errorf("working f.txt = %q, want A (materialized parent)", got)
	}
	if n := countStates(t, eng); n != 1 {
		t.Errorf("states = %d, want 1", n)
	}
	if res.Reclaimed.Removed < 1 {
		t.Errorf("expected the pruned leaf's blob to be reclaimed, got %d", res.Reclaimed.Removed)
	}
	mustVerifyClean(t, eng)
}

// TestPruneSiblingBranchKeepsHead prunes a branch HEAD is not on: HEAD and the
// working tree are untouched, only the sibling subtree is removed.
func TestPruneSiblingBranchKeepsHead(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "f.txt", "A")
	s1 := snap(t, eng)
	write(t, root, "f.txt", "B")
	s2 := snap(t, eng)
	if _, err := eng.Restore(ctx, s1); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	write(t, root, "f.txt", "C")
	s3 := snap(t, eng) // s1 -> {s2, s3}, HEAD = s3

	res, err := eng.Prune(ctx, s2)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if res.Deleted != 1 || res.HeadMovedTo != "" {
		t.Fatalf("res = %+v, want Deleted 1 and HEAD unchanged", res)
	}
	if headID(t, eng) != s3 {
		t.Errorf("HEAD = %s, want %s (unchanged)", headID(t, eng), s3)
	}
	if got := readWorking(t, root); got != "C" {
		t.Errorf("working f.txt = %q, want C (untouched)", got)
	}
	if n := countStates(t, eng); n != 2 {
		t.Errorf("states = %d, want 2", n)
	}
	mustVerifyClean(t, eng)
}

// TestPruneRootWipesHistoryKeepsFiles prunes the root while HEAD is on it: all
// history is removed and HEAD clears, but the working files are left alone.
func TestPruneRootWipesHistoryKeepsFiles(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "f.txt", "A")
	s1 := snap(t, eng)
	write(t, root, "f.txt", "B")
	snap(t, eng)

	plan, err := eng.PrunePlan(ctx, s1)
	if err != nil {
		t.Fatalf("PrunePlan: %v", err)
	}
	if !plan.WipesEntireStore || plan.StatesToDelete != 2 {
		t.Fatalf("plan = %+v, want WipesEntireStore with 2 states", plan)
	}

	res, err := eng.Prune(ctx, s1)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if res.Deleted != 2 || !res.HeadCleared {
		t.Fatalf("res = %+v, want Deleted 2 and HeadCleared", res)
	}
	if headID(t, eng) != "" {
		t.Errorf("HEAD = %s, want empty after wiping history", headID(t, eng))
	}
	if n := countStates(t, eng); n != 0 {
		t.Errorf("states = %d, want 0", n)
	}
	if got := readWorking(t, root); got != "B" {
		t.Errorf("working f.txt = %q, want B (files untouched)", got)
	}
	mustVerifyClean(t, eng)
}
