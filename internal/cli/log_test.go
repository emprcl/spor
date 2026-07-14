package cli

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/emprcl/spor/internal/core"
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

	// Newest first: branchB(@) leads the trunk column; the branchA lane runs beside
	// it and merges back into the trunk at child1.
	want := []struct{ prefix, contains string }{
		{"● ", "branchB"},    // @ tip, newest, in the trunk column
		{"│ ● ", "branchA2"}, // the side branch, newest of it first
		{"│ ● ", "branchA"},
		{"├─╯", ""}, // branch merges back toward the trunk
		{"● ", "child1"},
		{"● ", "root"},
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(want), out)
	}
	for i, w := range want {
		if !strings.HasPrefix(lines[i], w.prefix) {
			t.Errorf("line %d = %q, want prefix %q", i, lines[i], w.prefix)
		}
		if !strings.Contains(lines[i], w.contains) {
			t.Errorf("line %d = %q, want to contain %q", i, lines[i], w.contains)
		}
	}
	if !strings.Contains(lines[0], "(@)") {
		t.Errorf("HEAD line should be marked (@): %q", lines[0])
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
			t.Errorf("expected state %c to be folded away:\n%s", c, out)
		}
	}
	if !strings.Contains(out, "3 snapshots folded") {
		t.Errorf("expected a fold summary of 3 snaps:\n%s", out)
	}
}

func TestRenderLogNoFoldWhenShort(t *testing.T) {
	// 5 linear states: @, root, and a 3-long interior run (not over the threshold),
	// so nothing folds and every state is shown.
	out := renderPlain(linearHistory(5))
	if strings.Contains(out, "folded") {
		t.Errorf("a 3-state run should not fold:\n%s", out)
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
	if got := shortLen([]string{"ABCDEFGHIJ", "BBCDEFGHIJ"}); got != 7 {
		t.Errorf("distinct-first-char shortLen = %d, want 7 (floor)", got)
	}
	if got := shortLen([]string{"AAAAAAAAA1", "AAAAAAAAA2"}); got != 10 {
		t.Errorf("shared-9-prefix shortLen = %d, want 10", got)
	}
}

func TestHumanizeSince(t *testing.T) {
	now := time.Now()
	cases := []struct {
		d    time.Duration
		want string
	}{
		{10 * time.Second, "just now"},
		{5 * time.Minute, "5 min ago"},
		{3 * time.Hour, "3h ago"},
		{50 * time.Hour, "2d ago"},
	}
	for _, c := range cases {
		if got := humanizeSince(now.Add(-c.d)); got != c.want {
			t.Errorf("humanizeSince(-%s) = %q, want %q", c.d, got, c.want)
		}
	}
}
