package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/emprcl/spor/internal/core"
)

// TestRenderDiffFormatsHunks checks the unified-style rendering of a modified
// file: a header, a hunk header, and +/-/context line prefixes.
func TestRenderDiffFormatsHunks(t *testing.T) {
	res := core.DiffResult{
		From: "01ARZ7FROM0000000000000000",
		To:   "01ARZ7TO000000000000000000",
		Files: []core.FileDiff{{
			Path: "keep.txt",
			Kind: core.Modified,
			Hunks: []core.Hunk{{
				OldStart: 1, OldLines: 3, NewStart: 1, NewLines: 3,
				Lines: []core.DiffLine{
					{Op: core.OpContext, Text: "one"},
					{Op: core.OpDel, Text: "two"},
					{Op: core.OpAdd, Text: "two changed"},
					{Op: core.OpContext, Text: "three"},
				},
			}},
		}},
	}

	var buf bytes.Buffer
	renderDiff(&buf, res)
	out := buf.String()

	for _, want := range []string{
		"modified keep.txt",
		"@@ -1,3 +1,3 @@",
		"-two",
		"+two changed",
		" one",
		" three",
		"0 added, 1 modified, 0 removed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

// TestRenderDiffCoarseAndMode checks the one-line reports for binary content and
// an execute-bit-only change, and the empty-result message.
func TestRenderDiffCoarseAndMode(t *testing.T) {
	var buf bytes.Buffer
	renderDiff(&buf, core.DiffResult{
		From: "aaaaaaaaaaaa", To: "bbbbbbbbbbbb",
		Files: []core.FileDiff{
			{Path: "img.png", Kind: core.Modified, Binary: true},
			{Path: "run.sh", Kind: core.Modified, NewExec: true},
		},
	})
	out := buf.String()
	if !strings.Contains(out, "binary file changed") {
		t.Errorf("missing binary report: %s", out)
	}
	if !strings.Contains(out, "mode: execute bit set") {
		t.Errorf("missing mode note: %s", out)
	}

	buf.Reset()
	renderDiff(&buf, core.DiffResult{From: "aaaaaaaaaaaa", To: "aaaaaaaaaaaa"})
	if got := buf.String(); !strings.Contains(got, "no changes between") {
		t.Errorf("empty diff message = %q", got)
	}
}
