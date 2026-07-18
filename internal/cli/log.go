package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
	"github.com/emprcl/spor/internal/view"
)

// newLogCmd builds `spor log`, which renders the project history as a tree
// (docs/design-spec.md §6). History is a tree (single parent, no merges), so it draws
// cleanly with box-drawing connectors.
func newLogCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "log",
		Short: "Show the project history",
		Long: "Show the history newest first. Each branch keeps its own column, and long " +
			"unbranched stretches are hidden down to their most recent few. The snapshot " +
			"you are on is marked (@).",
		Example: `  # Show the history
  spor log

  # Page it or search it like any other output
  spor log | less`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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

			res, err := eng.Log(ctx)
			if err != nil {
				return err
			}
			// styledOut downsamples or strips the styles' ANSI to match the
			// destination (full color on a terminal, plain when piped).
			renderLog(styledOut(cmd), res)
			return nil
		},
	}
}

// renderLog draws the history through the shared layout (internal/view), so
// `spor log` and the TUI tree stay identical. Callers wrap w in a
// colorprofile.Writer so the styled output is colored on a terminal and plain
// under test or a pipe.
func renderLog(w io.Writer, res core.LogResult) {
	if len(res.States) == 0 {
		fmt.Fprintln(w, "No snapshots yet. Run 'spor snap' to create one.")
		return
	}
	for _, row := range view.LogRows(th, res, nil) {
		fmt.Fprintln(w, row.Text)
	}
}
