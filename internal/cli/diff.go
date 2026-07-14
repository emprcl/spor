package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
)

// newDiffCmd builds `spor diff`, which shows the changes between two states
// (docs/design-spec.md §5, §6). With one ref it compares that state to the current one
// (`@`); with two it compares them directly. It never diffs the working tree.
func newDiffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff <ref> [<ref>]",
		Short: "Show what changed between two states",
		Long: "Compare two points in history. With one <ref>, show what changed from " +
			"that state to the current one (@). With two, compare the first to the " +
			"second.\n\n" +
			"diff only ever compares recorded states, never your uncommitted edits. A " +
			"multi-word time ref must be quoted, e.g. spor diff \"2h ago\".",
		Example: `  # What changed since 2 hours ago
  spor diff "2h ago"

  # Compare two named states
  spor diff v1.0 v2.0`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			from, to := args[0], "@"
			if len(args) == 2 {
				to = args[1]
			}

			root, err := os.Getwd()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			eng, err := core.OpenExisting(ctx, root)
			if err != nil {
				return err
			}
			defer eng.Close()

			res, err := eng.Diff(ctx, from, to)
			if err != nil {
				return err
			}
			// Page long diffs like git, so the output can be scrolled; short diffs
			// and piped output print directly.
			withPager(cmd.OutOrStdout(), func(w io.Writer) { renderDiff(w, res) })
			return nil
		},
	}
}

// renderDiff prints a diff result as a colored, unified-style diff. Callers wrap
// w in a colorprofile.Writer so the output is colored on a terminal and plain
// under a pipe or test.
func renderDiff(w io.Writer, res core.DiffResult) {
	if len(res.Files) == 0 {
		fmt.Fprintf(w, "no changes between %s and %s\n", abbrev(res.From), abbrev(res.To))
		return
	}

	fmt.Fprintln(w, styleDiffMeta.Render(abbrev(res.From)+" -> "+abbrev(res.To)))
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
		renderFileDiff(w, f)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, styleDiffMeta.Render(
		fmt.Sprintf("%d added, %d modified, %d removed", added, modified, removed)))
}

// renderFileDiff prints one file's header and, when displayable, its hunks.
func renderFileDiff(w io.Writer, f core.FileDiff) {
	fmt.Fprintln(w, styleDiffHead.Render(kindLabel(f.Kind)+f.Path))

	switch {
	case f.ModeOnly():
		fmt.Fprintln(w, styleDiffMeta.Render("  "+modeNote(f.OldExec, f.NewExec)))
		return
	case f.Binary:
		fmt.Fprintln(w, styleDiffMeta.Render("  binary file"+kindVerb(f.Kind)))
		return
	case f.Truncated:
		fmt.Fprintln(w, styleDiffMeta.Render("  file too large to display"))
		return
	}

	// A content change that also flips the execute bit notes it before the hunks.
	if f.Kind == core.Modified && f.OldExec != f.NewExec {
		fmt.Fprintln(w, styleDiffMeta.Render("  "+modeNote(f.OldExec, f.NewExec)))
	}
	if len(f.Hunks) == 0 {
		if f.Kind == core.Modified {
			fmt.Fprintln(w, styleDiffMeta.Render("  (no line changes)"))
		} else {
			fmt.Fprintln(w, styleDiffMeta.Render("  (empty file)"))
		}
		return
	}
	for _, h := range f.Hunks {
		fmt.Fprintln(w, styleDiffHunk.Render(
			fmt.Sprintf("@@ -%d,%d +%d,%d @@", h.OldStart, h.OldLines, h.NewStart, h.NewLines)))
		for _, ln := range h.Lines {
			switch ln.Op {
			case core.OpAdd:
				fmt.Fprintln(w, styleDiffAdd.Render("+"+ln.Text))
			case core.OpDel:
				fmt.Fprintln(w, styleDiffDel.Render("-"+ln.Text))
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

// abbrev shortens a state id for display headers. Unlike the log's adaptive
// shortening, a fixed prefix is enough here since only two ids are shown.
func abbrev(id string) string {
	const n = 10
	if len(id) > n {
		return id[:n]
	}
	return id
}
