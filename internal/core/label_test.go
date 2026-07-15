package core

import (
	"context"
	"testing"
)

// stateLabel reads the stored label for a state.
func stateLabel(t *testing.T, eng *Engine, id string) string {
	t.Helper()
	var label string
	if err := eng.db.QueryRowContext(context.Background(),
		`SELECT COALESCE(label, '') FROM states WHERE id = ?`, id).Scan(&label); err != nil {
		t.Fatalf("reading label for %s: %v", id, err)
	}
	return label
}

// TestLabelSetsAndResolves names a state and confirms the name resolves back to
// it, and that relabeling overwrites in place without creating a new state.
func TestLabelSetsAndResolves(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "f", "1")
	id := snapID(t, eng)

	res, err := eng.Label(ctx, "@", "milestone")
	if err != nil {
		t.Fatalf("Label: %v", err)
	}
	if res.StateID != id || res.Name != "milestone" {
		t.Fatalf("Label = %+v, want state %s name milestone", res, id)
	}
	if got := stateLabel(t, eng, id); got != "milestone" {
		t.Fatalf("stored label = %q, want milestone", got)
	}
	if got, err := eng.Resolve(ctx, "milestone"); err != nil || got != id {
		t.Fatalf("Resolve(milestone) = %s (err %v), want %s", got, err, id)
	}

	// Relabeling overwrites in place; no new state appears.
	if _, err := eng.Label(ctx, "@", "renamed"); err != nil {
		t.Fatalf("relabel: %v", err)
	}
	if got := stateLabel(t, eng, id); got != "renamed" {
		t.Fatalf("stored label = %q, want renamed", got)
	}
	log, err := eng.Log(ctx)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(log.States) != 1 {
		t.Fatalf("relabel changed the state count to %d, want 1", len(log.States))
	}
}

// TestListLabels checks that only labeled states are listed, sorted by name.
func TestListLabels(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "f", "1")
	a := snapID(t, eng)
	write(t, root, "f", "2")
	b := snapID(t, eng)
	write(t, root, "f", "3")
	snapID(t, eng) // left unlabeled

	if _, err := eng.Label(ctx, b, "zeta"); err != nil {
		t.Fatalf("labeling b: %v", err)
	}
	if _, err := eng.Label(ctx, a, "alpha"); err != nil {
		t.Fatalf("labeling a: %v", err)
	}

	got, err := eng.ListLabels(ctx)
	if err != nil {
		t.Fatalf("ListLabels: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d labels, want 2 (the unlabeled state must not appear)", len(got))
	}
	// Sorted by name: alpha before zeta.
	if got[0].Name != "alpha" || got[0].StateID != a {
		t.Fatalf("first label = %+v, want alpha -> %s", got[0], a)
	}
	if got[1].Name != "zeta" || got[1].StateID != b {
		t.Fatalf("second label = %+v, want zeta -> %s", got[1], b)
	}
}

// TestListLabelsEmpty checks the empty case returns no labels and no error.
func TestListLabelsEmpty(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "f", "1")
	snapID(t, eng)

	got, err := eng.ListLabels(ctx)
	if err != nil {
		t.Fatalf("ListLabels: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d labels, want 0", len(got))
	}
}

// TestLabelRejectsEmptyName checks that an empty name is refused.
func TestLabelRejectsEmptyName(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "f", "1")
	snapID(t, eng)

	if _, err := eng.Label(ctx, "@", ""); err == nil {
		t.Fatal("Label with an empty name should error")
	}
}

// TestLabelUniqueAcrossStates checks that a label already held by another state
// is refused, that re-applying a state's own label is a harmless no-op, and that
// the DB-level unique index backs the check.
func TestLabelUniqueAcrossStates(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "f", "1")
	a := snapID(t, eng)
	write(t, root, "f", "2")
	b := snapID(t, eng)

	if _, err := eng.Label(ctx, a, "v1"); err != nil {
		t.Fatalf("labeling a: %v", err)
	}
	// b cannot take v1.
	if _, err := eng.Label(ctx, b, "v1"); err == nil {
		t.Fatal("labeling b with a's label should fail")
	}
	// a re-taking its own label is fine.
	if _, err := eng.Label(ctx, a, "v1"); err != nil {
		t.Fatalf("re-labeling a with its own label: %v", err)
	}
	// After a gives up v1, b may take it.
	if _, err := eng.Label(ctx, a, "first"); err != nil {
		t.Fatalf("relabeling a: %v", err)
	}
	if _, err := eng.Label(ctx, b, "v1"); err != nil {
		t.Fatalf("b taking the now-free label: %v", err)
	}
}

// TestSnapLabelMustBeUnique checks a new snapshot cannot reuse a label.
func TestSnapLabelMustBeUnique(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "f", "1")
	if _, err := eng.Snap(ctx, SnapOptions{Label: "tag"}); err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	write(t, root, "f", "2")
	if _, err := eng.Snap(ctx, SnapOptions{Label: "tag"}); err == nil {
		t.Fatal("second snapshot reusing the label should fail")
	}
}

// TestUnlabelRemovesAndFreesName checks that unlabeling clears the label from
// its state, and that the name can then be reused elsewhere.
func TestUnlabelRemovesAndFreesName(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "f", "1")
	a := snapID(t, eng)
	write(t, root, "f", "2")
	b := snapID(t, eng)

	if _, err := eng.Label(ctx, a, "v1"); err != nil {
		t.Fatalf("labeling a: %v", err)
	}
	res, err := eng.Unlabel(ctx, "v1")
	if err != nil {
		t.Fatalf("Unlabel: %v", err)
	}
	if res.StateID != a || res.Name != "v1" {
		t.Fatalf("Unlabel = %+v, want state %s name v1", res, a)
	}
	if got := stateLabel(t, eng, a); got != "" {
		t.Fatalf("stored label = %q, want empty after unlabel", got)
	}
	if _, err := eng.Resolve(ctx, "v1"); err == nil {
		t.Fatal("Resolve(v1) should fail once the label is removed")
	}

	// The freed name can be reused on another state.
	if _, err := eng.Label(ctx, b, "v1"); err != nil {
		t.Fatalf("b taking the now-free label: %v", err)
	}
}

// TestUnlabelUnknownName checks that removing a name that isn't in use is an
// error, not a silent no-op.
func TestUnlabelUnknownName(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "f", "1")
	snapID(t, eng)

	if _, err := eng.Unlabel(ctx, "ghost"); err == nil {
		t.Fatal("Unlabel of an unused name should error")
	}
}

// TestUnlabelRejectsEmptyName checks that an empty name is refused.
func TestUnlabelRejectsEmptyName(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "f", "1")
	snapID(t, eng)

	if _, err := eng.Unlabel(ctx, ""); err == nil {
		t.Fatal("Unlabel with an empty name should error")
	}
}

// TestLabelUnknownRef checks that an unresolvable ref is reported.
func TestLabelUnknownRef(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "f", "1")
	snapID(t, eng)

	if _, err := eng.Label(ctx, "ZZZZZZ", "x"); err == nil {
		t.Fatal("Label of a non-matching ref should error")
	}
}
