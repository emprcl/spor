package view

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/emprcl/spor/internal/core"
	"github.com/emprcl/spor/internal/textfmt"
)

// WriteDiff prints a diff result as a colored, unified-style diff. Callers wrap
// w in a colorprofile.Writer so the output is colored on a terminal and plain
// under a pipe or test.
func WriteDiff(th *Theme, w io.Writer, res core.DiffResult) {
	if len(res.Files) == 0 {
		fmt.Fprintf(w, "no changes between %s and %s\n", textfmt.Abbrev(res.From), textfmt.Abbrev(res.To))
		return
	}

	fmt.Fprintln(w, th.DiffMeta.Render(textfmt.Abbrev(res.From)+" -> "+textfmt.Abbrev(res.To)))
	var added, modified, removed int
	for _, f := range res.Files {
		switch f.Kind {
		case core.Added:
			added++
		case core.Modified:
			modified++
		case core.Removed:
			removed++
		}
		fmt.Fprintln(w)
		writeFileDiff(th, w, f)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, th.DiffMeta.Render(
		fmt.Sprintf("%d added, %d modified, %d removed", added, modified, removed)))
}

// DiffLines renders a diff to styled lines, for the TUI's scrollable overlay.
func DiffLines(th *Theme, res core.DiffResult) []string {
	var buf bytes.Buffer
	WriteDiff(th, &buf, res)
	return strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
}

// writeFileDiff prints one file's header and, when displayable, its hunks.
func writeFileDiff(th *Theme, w io.Writer, f core.FileDiff) {
	fmt.Fprintln(w, th.DiffHead.Render(kindLabel(f.Kind)+f.Path))

	switch {
	case f.ModeOnly():
		fmt.Fprintln(w, th.DiffMeta.Render("  "+modeNote(f.OldExec, f.NewExec)))
		return
	case f.Binary:
		fmt.Fprintln(w, th.DiffMeta.Render("  binary file"+kindVerb(f.Kind)))
		return
	case f.Truncated:
		fmt.Fprintln(w, th.DiffMeta.Render("  file too large to display"))
		return
	}

	// A content change that also flips the execute bit notes it before the hunks.
	if f.Kind == core.Modified && f.OldExec != f.NewExec {
		fmt.Fprintln(w, th.DiffMeta.Render("  "+modeNote(f.OldExec, f.NewExec)))
	}
	if len(f.Hunks) == 0 {
		if f.Kind == core.Modified {
			fmt.Fprintln(w, th.DiffMeta.Render("  (no line changes)"))
		} else {
			fmt.Fprintln(w, th.DiffMeta.Render("  (empty file)"))
		}
		return
	}
	for _, h := range f.Hunks {
		fmt.Fprintln(w, th.DiffHunk.Render(
			fmt.Sprintf("@@ -%d,%d +%d,%d @@", h.OldStart, h.OldLines, h.NewStart, h.NewLines)))
		for _, ln := range h.Lines {
			switch ln.Op {
			case core.OpAdd:
				fmt.Fprintln(w, th.DiffAdd.Render("+"+ln.Text))
			case core.OpDel:
				fmt.Fprintln(w, th.DiffDel.Render("-"+ln.Text))
			case core.OpContext:
				fmt.Fprintln(w, " "+ln.Text)
			}
		}
	}
}

// kindLabel is the fixed-width file-header prefix for a change kind.
func kindLabel(k core.ChangeKind) string {
	switch k {
	case core.Added:
		return "added    "
	case core.Removed:
		return "removed  "
	default:
		return "modified "
	}
}

// kindVerb is the trailing verb for a coarse (binary) one-liner.
func kindVerb(k core.ChangeKind) string {
	switch k {
	case core.Added:
		return " added"
	case core.Removed:
		return " removed"
	default:
		return " changed"
	}
}

// modeNote describes an execute-bit transition.
func modeNote(oldExec, newExec bool) string {
	switch {
	case !oldExec && newExec:
		return "mode: execute bit set"
	case oldExec && !newExec:
		return "mode: execute bit cleared"
	default:
		return "mode changed"
	}
}
