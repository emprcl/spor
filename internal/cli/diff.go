package cli

import (
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
	"github.com/emprcl/spor/internal/view"
)

// newDiffCmd builds `spor diff`, which shows the changes between two states
// (docs/design-spec.md §5, §6). With one ref it compares that state to the current one
// (`@`); with two it compares them directly. It never diffs the working tree.
func newDiffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff <ref> [<ref>]",
		Short: "Show what changed between two snapshots",
		Long: "Compare two points in history. With one <ref>, show what changed from " +
			"that snapshot to the current one (@). With two, compare the first to the " +
			"second.\n\n" +
			"diff only ever compares recorded snapshots, never your uncommitted edits.",
		Example: `  # What changed since 2 hours ago
  spor diff 2h

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

// renderDiff prints a diff through the shared renderer (internal/view), so
// `spor diff` and the TUI overlay stay identical.
func renderDiff(w io.Writer, res core.DiffResult) {
	view.WriteDiff(th, w, res)
}
