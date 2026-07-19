package core

import (
	"sort"
	"strings"
	"testing"

	"github.com/emprcl/spor/internal/remote"
)

// st builds a state for the merge tables. Parent "" means root; manifest hashes
// are derived from the id, since the merge only cares that they agree.
func st(id, parent, label string) remote.State {
	return remote.State{
		ID:           id,
		Parent:       parent,
		CreatedAt:    1,
		ManifestHash: "m-" + id,
		Label:        label,
	}
}

// graph indexes states by id, the shape mergeGraphs consumes.
func graph(states ...remote.State) map[string]remote.State {
	m := make(map[string]remote.State, len(states))
	for _, s := range states {
		m[s.ID] = s
	}
	return m
}

func noPins() map[string]struct{} { return map[string]struct{}{} }

// ids returns the merged state ids, sorted, for order-independent assertions.
func ids(m map[string]remote.State) []string {
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func mustMerge(t *testing.T, base, local, srv map[string]remote.State, pins map[string]struct{}) mergeResult {
	t.Helper()
	res, err := mergeGraphs(base, local, srv, pins, false)
	if err != nil {
		t.Fatalf("mergeGraphs: %v", err)
	}
	return res
}

// TestMergeAdditionsFromBothSidesBecomeABranch covers the ordinary offline case:
// each machine snapped, and the union is a branch the log already renders.
func TestMergeAdditionsFromBothSidesBecomeABranch(t *testing.T) {
	base := graph(st("a", "", ""), st("b", "a", ""))
	local := graph(st("a", "", ""), st("b", "a", ""), st("c", "b", ""))
	srv := graph(st("a", "", ""), st("b", "a", ""), st("d", "b", ""))

	res := mustMerge(t, base, local, srv, noPins())

	if len(res.Conflicts) != 0 {
		t.Fatalf("independent additions should not conflict, got %+v", res.Conflicts)
	}
	if got, want := ids(res.States), []string{"a", "b", "c", "d"}; !equal(got, want) {
		t.Errorf("merged ids = %v, want %v", got, want)
	}
	if res.States["c"].Parent != "b" || res.States["d"].Parent != "b" {
		t.Errorf("both additions should hang off b, got c=%q d=%q",
			res.States["c"].Parent, res.States["d"].Parent)
	}
}

// TestMergePropagatesRemoteDelete is the case the old additive-only design could
// not express: a thin on the other machine must actually remove states here,
// otherwise pull re-hydrates everything thin reclaimed.
func TestMergePropagatesRemoteDelete(t *testing.T) {
	base := graph(st("a", "", ""), st("b", "a", ""), st("c", "b", ""))
	local := graph(st("a", "", ""), st("b", "a", ""), st("c", "b", ""))
	// The server thinned b away and re-parented c onto a.
	srv := graph(st("a", "", ""), st("c", "a", ""))

	res := mustMerge(t, base, local, srv, noPins())

	if len(res.Conflicts) != 0 {
		t.Fatalf("unopposed delete should not conflict, got %+v", res.Conflicts)
	}
	if got, want := ids(res.States), []string{"a", "c"}; !equal(got, want) {
		t.Errorf("merged ids = %v, want %v", got, want)
	}
	if res.States["c"].Parent != "a" {
		t.Errorf("c should follow the server's re-parent, got %q", res.States["c"].Parent)
	}
}

// TestMergePropagatesLocalDelete is the mirror: a local thin must survive the
// pull rather than being undone by the server's older copy.
func TestMergePropagatesLocalDelete(t *testing.T) {
	base := graph(st("a", "", ""), st("b", "a", ""), st("c", "b", ""))
	local := graph(st("a", "", ""), st("c", "a", ""))
	srv := graph(st("a", "", ""), st("b", "a", ""), st("c", "b", ""))

	res := mustMerge(t, base, local, srv, noPins())

	if len(res.Conflicts) != 0 {
		t.Fatalf("unopposed delete should not conflict, got %+v", res.Conflicts)
	}
	if got, want := ids(res.States), []string{"a", "c"}; !equal(got, want) {
		t.Errorf("merged ids = %v, want %v", got, want)
	}
}

// TestMergeResurrectsAncestorOfLocalAddition locks in the rule that keeps a pull
// from orphaning work: the other machine thinned a state away without knowing
// this one had added a child beneath it. parent_id is ON DELETE RESTRICT, so the
// delete cannot simply win.
func TestMergeResurrectsAncestorOfLocalAddition(t *testing.T) {
	base := graph(st("a", "", ""), st("b", "a", ""))
	local := graph(st("a", "", ""), st("b", "a", ""), st("new", "b", ""))
	srv := graph(st("a", "", "")) // thinned b away

	res := mustMerge(t, base, local, srv, noPins())

	if len(res.Conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", res.Conflicts)
	}
	if _, ok := res.States["b"]; !ok {
		t.Fatal("b must be resurrected: the local addition hangs off it")
	}
	if got, want := res.Resurrected, []string{"b"}; !equal(got, want) {
		t.Errorf("Resurrected = %v, want %v", got, want)
	}
	if got, want := ids(res.States), []string{"a", "b", "new"}; !equal(got, want) {
		t.Errorf("merged ids = %v, want %v", got, want)
	}
}

// TestMergeResurrectsTransitively checks the fixpoint: reviving a state can
// require reviving its own parent.
func TestMergeResurrectsTransitively(t *testing.T) {
	base := graph(st("a", "", ""), st("b", "a", ""), st("c", "b", ""))
	local := graph(st("a", "", ""), st("b", "a", ""), st("c", "b", ""), st("new", "c", ""))
	srv := graph(st("a", "", "")) // thinned b and c away

	res := mustMerge(t, base, local, srv, noPins())

	if got, want := res.Resurrected, []string{"b", "c"}; !equal(got, want) {
		t.Errorf("Resurrected = %v, want %v", got, want)
	}
	if got, want := ids(res.States), []string{"a", "b", "c", "new"}; !equal(got, want) {
		t.Errorf("merged ids = %v, want %v", got, want)
	}
}

// TestMergePinsHeadAgainstRemoteDelete guards HEAD specifically. head is
// ON DELETE SET NULL and head_history is ON DELETE CASCADE, so letting a remote
// thin delete local HEAD would null it and prune the journal behind the user's back.
func TestMergePinsHeadAgainstRemoteDelete(t *testing.T) {
	base := graph(st("a", "", ""), st("b", "a", ""))
	local := graph(st("a", "", ""), st("b", "a", ""))
	srv := graph(st("a", "", "")) // thinned b away
	pins := map[string]struct{}{"b": {}}

	res := mustMerge(t, base, local, srv, pins)

	if _, ok := res.States["b"]; !ok {
		t.Fatal("local HEAD must survive a remote delete")
	}
}

// TestMergeReparentConflictRefuses is the both-machines-edited-history case. One
// user editing history on two machines at once is rare enough that guessing is
// worse than stopping.
func TestMergeReparentConflictRefuses(t *testing.T) {
	base := graph(st("a", "", ""), st("b", "", ""), st("c", "a", ""))
	local := graph(st("a", "", ""), st("b", "", ""), st("c", "b", ""))
	srv := graph(st("a", "", ""), st("b", "", ""), st("c", "", ""))

	res := mustMerge(t, base, local, srv, noPins())

	if len(res.Conflicts) != 1 {
		t.Fatalf("want exactly one conflict, got %+v", res.Conflicts)
	}
	c := res.Conflicts[0]
	if c.StateID != "c" || c.Field != "parent" {
		t.Errorf("conflict = %+v, want parent conflict on c", c)
	}
	if c.Local != "b" || c.Remote != "" {
		t.Errorf("conflict should carry both sides, got local=%q remote=%q", c.Local, c.Remote)
	}
}

// TestMergePreferRemoteResolvesConflicts covers pull --force: conflicts settle in
// the server's favor instead of stopping the pull.
func TestMergePreferRemoteResolvesConflicts(t *testing.T) {
	base := graph(st("a", "", ""), st("b", "", ""), st("c", "a", ""))
	local := graph(st("a", "", ""), st("b", "", ""), st("c", "b", ""))
	srv := graph(st("a", "", ""), st("b", "", ""), st("c", "", ""))

	res, err := mergeGraphs(base, local, srv, noPins(), true)
	if err != nil {
		t.Fatalf("mergeGraphs: %v", err)
	}
	if len(res.Conflicts) != 0 {
		t.Fatalf("force should resolve, not report: %+v", res.Conflicts)
	}
	if res.States["c"].Parent != "" {
		t.Errorf("server's parent should win, got %q", res.States["c"].Parent)
	}
	if got, want := res.ForcedRemote, []string{"c"}; !equal(got, want) {
		t.Errorf("ForcedRemote = %v, want %v", got, want)
	}
}

// TestMergePreferRemoteKeepsUndisputedLocalWork: force settles conflicts, it is
// not a licence to throw away states the server simply never knew about.
func TestMergePreferRemoteKeepsUndisputedLocalWork(t *testing.T) {
	base := graph(st("a", "", ""))
	local := graph(st("a", "", ""), st("mine", "a", ""))
	srv := graph(st("a", "", ""), st("theirs", "a", ""))

	res, err := mergeGraphs(base, local, srv, noPins(), true)
	if err != nil {
		t.Fatalf("mergeGraphs: %v", err)
	}
	if got, want := ids(res.States), []string{"a", "mine", "theirs"}; !equal(got, want) {
		t.Errorf("merged ids = %v, want %v", got, want)
	}
}

// TestDeleteOrderPlacesChildrenFirst mirrors insertOrder: parent_id is
// ON DELETE RESTRICT, so a parent removed before its child fails.
func TestDeleteOrderPlacesChildrenFirst(t *testing.T) {
	local := graph(st("a", "", ""), st("b", "a", ""), st("c", "b", ""))
	del := map[string]struct{}{"a": {}, "b": {}, "c": {}}

	order := deleteOrder(local, del)

	if got, want := order, []string{"c", "b", "a"}; !equal(got, want) {
		t.Errorf("deleteOrder = %v, want %v", got, want)
	}
}

// TestMergeOneSidedReparentWins: only one machine moved the state, so there is
// nothing to arbitrate.
func TestMergeOneSidedReparentWins(t *testing.T) {
	base := graph(st("a", "", ""), st("b", "", ""), st("c", "a", ""))

	local := graph(st("a", "", ""), st("b", "", ""), st("c", "b", ""))
	srv := graph(st("a", "", ""), st("b", "", ""), st("c", "a", ""))
	res := mustMerge(t, base, local, srv, noPins())
	if len(res.Conflicts) != 0 || res.States["c"].Parent != "b" {
		t.Errorf("local re-parent should win, got parent=%q conflicts=%+v",
			res.States["c"].Parent, res.Conflicts)
	}

	local = graph(st("a", "", ""), st("b", "", ""), st("c", "a", ""))
	srv = graph(st("a", "", ""), st("b", "", ""), st("c", "b", ""))
	res = mustMerge(t, base, local, srv, noPins())
	if len(res.Conflicts) != 0 || res.States["c"].Parent != "b" {
		t.Errorf("remote re-parent should win, got parent=%q conflicts=%+v",
			res.States["c"].Parent, res.Conflicts)
	}
}

// TestMergeDeleteVersusEditConflicts: dropping the state would silently discard
// the other machine's edit, so it stops instead.
func TestMergeDeleteVersusEditConflicts(t *testing.T) {
	base := graph(st("a", "", ""), st("b", "a", ""))
	local := graph(st("a", "", ""), st("b", "a", "keeper")) // labeled here
	srv := graph(st("a", "", ""))                           // deleted there

	res := mustMerge(t, base, local, srv, noPins())

	if len(res.Conflicts) != 1 {
		t.Fatalf("want one conflict, got %+v", res.Conflicts)
	}
	if c := res.Conflicts[0]; c.StateID != "b" || c.Field != "deleted" {
		t.Errorf("conflict = %+v, want deleted-vs-edit on b", c)
	}
	if !strings.Contains(res.Conflicts[0].Local, "keeper") {
		t.Errorf("conflict should name the edit, got %q", res.Conflicts[0].Local)
	}
}

// TestMergeUnopposedDeleteOfUneditedStateIsClean is the counterpart: the state
// was untouched here, so the delete is not ambiguous and must not warn.
func TestMergeUnopposedDeleteOfUneditedStateIsClean(t *testing.T) {
	base := graph(st("a", "", ""), st("b", "a", ""))
	local := graph(st("a", "", ""), st("b", "a", ""))
	srv := graph(st("a", "", ""))

	res := mustMerge(t, base, local, srv, noPins())

	if len(res.Conflicts) != 0 {
		t.Fatalf("untouched state deleted remotely should not conflict, got %+v", res.Conflicts)
	}
	if _, ok := res.States["b"]; ok {
		t.Error("b should be gone")
	}
}

// TestMergeLabelCollisionPrefersServer covers the UNIQUE index on label: the same
// name was given to different states on each machine. A blocked pull is worse
// than a cleared label, which is cheap to re-add.
func TestMergeLabelCollisionPrefersServer(t *testing.T) {
	base := graph(st("a", "", ""), st("b", "a", ""))
	local := graph(st("a", "", "v2"), st("b", "a", ""))
	srv := graph(st("a", "", ""), st("b", "a", "v2"))

	res := mustMerge(t, base, local, srv, noPins())

	if len(res.Conflicts) != 0 {
		t.Fatalf("label collision should resolve, not conflict: %+v", res.Conflicts)
	}
	if res.States["b"].Label != "v2" {
		t.Errorf("server's label should survive, got %q", res.States["b"].Label)
	}
	if res.States["a"].Label != "" {
		t.Errorf("local duplicate should be cleared, got %q", res.States["a"].Label)
	}
	if len(res.LabelsClears) != 1 {
		t.Fatalf("the clearing must be reported, got %+v", res.LabelsClears)
	}
	if c := res.LabelsClears[0]; c.Label != "v2" || c.Kept != "b" || c.Cleared != "a" {
		t.Errorf("labelClear = %+v, want v2 kept on b cleared from a", c)
	}
}

// TestMergeRejectsImmutableFieldDisagreement: created_at and manifest_hash never
// change for a given id, so a mismatch means corruption rather than a conflict.
func TestMergeRejectsImmutableFieldDisagreement(t *testing.T) {
	base := graph()
	local := graph(remote.State{ID: "a", CreatedAt: 1, ManifestHash: "one"})
	srv := graph(remote.State{ID: "a", CreatedAt: 1, ManifestHash: "two"})

	if _, err := mergeGraphs(base, local, srv, noPins(), false); err == nil {
		t.Fatal("differing manifest hashes for one id must be an error")
	}
}

// TestMergeIsDeterministic: map iteration must not leak into the output, or
// conflict reports and insert order would vary between runs.
func TestMergeIsDeterministic(t *testing.T) {
	base := graph(st("a", "", ""), st("b", "a", ""), st("c", "a", ""))
	local := graph(st("a", "", ""), st("b", "", ""), st("c", "", ""))
	srv := graph(st("a", "", ""), st("b", "a", ""), st("c", "a", ""))

	first := mustMerge(t, base, local, srv, noPins())
	for run := 0; run < 20; run++ {
		got := mustMerge(t, base, local, srv, noPins())
		if len(got.Conflicts) != len(first.Conflicts) {
			t.Fatalf("conflict count varies between runs")
		}
		for i := range got.Conflicts {
			if got.Conflicts[i] != first.Conflicts[i] {
				t.Fatalf("conflict order varies: %+v vs %+v", got.Conflicts[i], first.Conflicts[i])
			}
		}
	}
}

// TestInsertOrderPlacesParentsFirst: foreign keys are on and parent_id is
// ON DELETE RESTRICT, so a pull inserting a child before its parent fails.
func TestInsertOrderPlacesParentsFirst(t *testing.T) {
	states := graph(
		st("d", "c", ""), st("c", "b", ""), st("b", "a", ""), st("a", "", ""),
	)

	ordered, err := insertOrder(states)
	if err != nil {
		t.Fatalf("insertOrder: %v", err)
	}
	if len(ordered) != 4 {
		t.Fatalf("want 4 states, got %d", len(ordered))
	}

	seen := make(map[string]bool)
	for _, s := range ordered {
		if s.Parent != "" && !seen[s.Parent] {
			t.Fatalf("%s inserted before its parent %s", s.ID, s.Parent)
		}
		seen[s.ID] = true
	}
}

// TestInsertOrderDetectsCycle: a corrupt graph must not spin forever.
func TestInsertOrderDetectsCycle(t *testing.T) {
	states := graph(st("a", "b", ""), st("b", "a", ""))

	if _, err := insertOrder(states); err == nil {
		t.Fatal("a parent cycle must be reported")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
