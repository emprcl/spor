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
	now := time.Now()
	mk := func(id, parent, label string) core.StateInfo {
		return core.StateInfo{ID: id, Parent: parent, Label: label, CreatedAt: now}
	}
	// root -> child1 -> {branchA, branchB}; branchA -> branchA2; HEAD = branchB.
	root := strings.Repeat("A", 26)
	child1 := strings.Repeat("B", 26)
	a := strings.Repeat("C", 26)
	a2 := strings.Repeat("D", 26)
	b := strings.Repeat("E", 26)
	res := core.LogResult{
		Head: b,
		States: []core.StateInfo{
			mk(root, "", "root"),
			mk(child1, root, "child1"),
			mk(a, child1, "branchA"),
			mk(a2, a, "branchA2"),
			mk(b, child1, "branchB"),
		},
	}

	out := renderPlain(res)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

	want := []struct{ prefix, contains string }{
		{"● ", "root"},       // linear trunk
		{"● ", "child1"},     // still trunk (single child)
		{"├─● ", "branchA"},  // branch point
		{"│ ● ", "branchA2"}, // linear continuation inside the branch
		{"└─● ", "branchB"},  // last branch
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
	if !strings.Contains(lines[4], "(@)") {
		t.Errorf("HEAD line should be marked (@): %q", lines[4])
	}
}

func TestRenderLogEmpty(t *testing.T) {
	if out := renderPlain(core.LogResult{}); !strings.Contains(out, "No snaps yet") {
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
