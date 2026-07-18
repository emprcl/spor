package cli

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/emprcl/spor/internal/core"
	"github.com/emprcl/spor/internal/view"
)

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

// renderPlain renders the log and strips all ANSI styling so tests can assert on
// the tree structure alone.
func renderPlain(res core.LogResult) string {
	var buf bytes.Buffer
	renderLog(&buf, res)
	return ansiRe.ReplaceAllString(buf.String(), "")
}

func TestRenderLogBranching(t *testing.T) {
	base := time.Now()
	mk := func(id, parent, label string, ageMin int) core.StateInfo {
		return core.StateInfo{ID: id, Parent: parent, Label: label, CreatedAt: base.Add(time.Duration(ageMin) * time.Minute)}
	}
	// root -> child1 -> {branchA -> branchA2, branchB(@)}, drawn newest first.
	root := strings.Repeat("A", 26)
	child1 := strings.Repeat("B", 26)
	a := strings.Repeat("C", 26)
	a2 := strings.Repeat("D", 26)
	b := strings.Repeat("E", 26)
	res := core.LogResult{
		Head: b,
		States: []core.StateInfo{
			mk(root, "", "root", 0),
			mk(child1, root, "child1", 1),
			mk(a, child1, "branchA", 2),
			mk(a2, a, "branchA2", 3),
			mk(b, child1, "branchB", 4),
		},
	}

	out := renderPlain(res)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

	// Metadata leads each row, the graph trails it. Newest first, and columns are
	// fixed by tree shape, not recency: the original line (root -> child1 -> branchA
	// -> branchA2) is the trunk in column 0, and the later branchB offshoot keeps its
	// own column even though it is the newest state. Rows are trimmed of trailing
	// space, so a node row ends in the rightmost active lane's glyph: "│" while the
	// offshoot lane runs alongside the trunk, "●" once only the trunk remains.
	want := []struct{ label, endsWith string }{
		{"branchB", "●"},  // @ tip, newest, in its own offshoot column
		{"branchA2", "│"}, // trunk node with the branchB lane still beside it
		{"branchA", "│"},
		{"", "╯"}, // the offshoot merges back into the trunk at child1
		{"child1", "●"},
		{"root", "●"},
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(want), out)
	}
	for i, w := range want {
		if w.label != "" && !strings.Contains(lines[i], w.label) {
			t.Errorf("line %d = %q, want to contain %q", i, lines[i], w.label)
		}
		if !strings.HasSuffix(lines[i], w.endsWith) {
			t.Errorf("line %d = %q, want to end with %q", i, lines[i], w.endsWith)
		}
	}
	if !strings.Contains(lines[0], "(@)") {
		t.Errorf("HEAD line should be marked (@): %q", lines[0])
	}
}

// dotRuneIndex returns the rune index of the node marker "●" in a rendered row,
// or -1 if absent. Rune index (not byte index) maps directly to graph column,
// since box-drawing glyphs are multibyte.
func dotRuneIndex(line string) int {
	for i, r := range []rune(line) {
		if r == '●' {
			return i
		}
	}
	return -1
}

// lineFor returns the rendered (ANSI-stripped) row whose metadata contains sub.
func lineFor(t *testing.T, res core.LogResult, sub string) string {
	t.Helper()
	for _, ln := range strings.Split(renderPlain(res), "\n") {
		if strings.Contains(ln, sub) {
			return ln
		}
	}
	t.Fatalf("no line containing %q in:\n%s", sub, renderPlain(res))
	return ""
}

// TestRenderLogStableColumns checks the core property behind the fixed-column
// layout: a timeline keeps its horizontal column no matter which timeline is the
// newest/active one. The offshoot branchB must sit in the same column whether it
// or the trunk carries the newest state.
func TestRenderLogStableColumns(t *testing.T) {
	base := time.Now()
	mk := func(id, parent string, ageMin int) core.StateInfo {
		return core.StateInfo{ID: id, Parent: parent, CreatedAt: base.Add(time.Duration(ageMin) * time.Minute)}
	}
	root := strings.Repeat("A", 26)
	child1 := strings.Repeat("B", 26)
	a := strings.Repeat("C", 26)
	a2 := strings.Repeat("D", 26)
	b := strings.Repeat("E", 26)
	a3 := strings.Repeat("F", 26)

	// branchB (age 4) is the newest state: the trunk holds column 0, branchB its own.
	bNewest := core.LogResult{
		Head: b,
		States: []core.StateInfo{
			mk(root, "", 0), mk(child1, root, 1), mk(a, child1, 2), mk(a2, a, 3), mk(b, child1, 4),
		},
	}
	// Extend the trunk with branchA3 (age 5) so the trunk now carries the newest
	// state. branchB is unchanged and must not move.
	trunkNewest := core.LogResult{
		Head:   a3,
		States: append(append([]core.StateInfo{}, bNewest.States...), mk(a3, a2, 5)),
	}

	// The graph trails the metadata, so measure each node's column relative to where
	// the graph begins: the always-col-0 root fixes the graph's left edge, and
	// branchB's "●" must sit one column (two runes) past it. Whichever timeline is
	// newest, that offset stays 2; only the column-0 lane above branchB changes
	// (blank when branchB leads, "│" when the trunk's newer tip sits above it).
	rootShort, bShort := strings.Repeat("A", 7), strings.Repeat("E", 7)
	for name, res := range map[string]core.LogResult{"branchB newest": bNewest, "trunk newest": trunkNewest} {
		graphStart := dotRuneIndex(lineFor(t, res, rootShort)) // root is column 0
		got := lineFor(t, res, bShort)
		if off := dotRuneIndex(got) - graphStart; off != 2 {
			t.Errorf("%s: branchB moved column, node %d runes into the graph want 2 in %q", name, off, got)
		}
	}
}

// linearHistory builds n states in a single chain, oldest first, with the newest
// as HEAD and no labels; state k uses the (k+1)-th letter repeated to a full id.
func linearHistory(n int) core.LogResult {
	base := time.Now()
	states := make([]core.StateInfo, n)
	var parent string
	for i := 0; i < n; i++ {
		id := strings.Repeat(string(rune('A'+i)), 26)
		states[i] = core.StateInfo{ID: id, Parent: parent, CreatedAt: base.Add(time.Duration(i) * time.Minute)}
		parent = id
	}
	return core.LogResult{Head: states[n-1].ID, States: states}
}

func TestRenderLogFoldsLongRun(t *testing.T) {
	// 8 linear states: @ (newest) and the root are always shown; the 6 interior
	// states form one foldable run, so the 3 most recent survive and 3 fold away.
	out := renderPlain(linearHistory(8))
	short := 7 // 8 distinct first letters -> shortLen floor of 7

	shown := []byte{'H', 'G', 'F', 'E', 'A'} // @, 3 most recent interior, root
	for _, c := range shown {
		if !strings.Contains(out, strings.Repeat(string(c), short)) {
			t.Errorf("expected state %c to be shown:\n%s", c, out)
		}
	}
	for _, c := range []byte{'D', 'C', 'B'} { // the folded interior states
		if strings.Contains(out, strings.Repeat(string(c), short)) {
			t.Errorf("expected state %c to be hidden away:\n%s", c, out)
		}
	}
	if !strings.Contains(out, "3 snaps hidden") {
		t.Errorf("expected a hidden-run summary of 3 snaps:\n%s", out)
	}
}

func TestRenderLogNoFoldWhenShort(t *testing.T) {
	// 5 linear states: @, root, and a 3-long interior run (not over the threshold),
	// so nothing folds and every state is shown.
	out := renderPlain(linearHistory(5))
	if strings.Contains(out, "hidden") {
		t.Errorf("a 3-state run should not be hidden:\n%s", out)
	}
	for i := 0; i < 5; i++ {
		if !strings.Contains(out, strings.Repeat(string(rune('A'+i)), 7)) {
			t.Errorf("state %c should be shown:\n%s", rune('A'+i), out)
		}
	}
}

func TestRenderLogEmpty(t *testing.T) {
	if out := renderPlain(core.LogResult{}); !strings.Contains(out, "No snapshots yet") {
		t.Fatalf("empty log = %q", out)
	}
}

func TestShortLen(t *testing.T) {
	if got := view.ShortLen([]string{"ABCDEFGHIJ", "BBCDEFGHIJ"}); got != 7 {
		t.Errorf("distinct-first-char shortLen = %d, want 7 (floor)", got)
	}
	if got := view.ShortLen([]string{"AAAAAAAAA1", "AAAAAAAAA2"}); got != 10 {
		t.Errorf("shared-9-prefix shortLen = %d, want 10", got)
	}
}
