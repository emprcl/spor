package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
)

// newFoldCmd builds `spor fold <a> <b>`, which squashes the linear range from the
// older state a to the newer state b into a single state holding b's content
// (docs/design-spec.md §5, §6). The intermediates are lost, so it confirms first.
func newFoldCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "fold <a> <b>",
		Short: "Squash a run of snapshots into one",
		Long: "Collapse the history from an older snapshot (a) up to a newer one (b) into a " +
			"single snapshot that keeps b's contents. The snapshots in between are permanently " +
			"lost; only the point before a and b's final contents survive. The range " +
			"must be a straight line: no snapshot in it may have a branch off to the side. " +
			"This cannot be undone.\n\n" +
			"a and b are snapshot refs, e.g. spor fold 2h @.",
		Example: `  # Squash the last five states into one
  spor fold @~5 @

  # Squash everything between two named states
  spor fold v1.0 v2.0`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
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

			plan, err := eng.FoldPlan(ctx, args[0], args[1])
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if !yes {
				lost := plan.StatesFolded - 1
				fmt.Fprintf(out, "Folding %s..%s squashes %d %s into one, losing %d intermediate %s.\n",
					abbrev(plan.From), abbrev(plan.To),
					plan.StatesFolded, plural(plan.StatesFolded, "snapshot", "snapshots"),
					lost, plural(lost, "snapshot", "snapshots"))
				if plan.HeadWillMove {
					fmt.Fprintln(out, "  HEAD will move to the folded snapshot and your working files will change to match.")
				}
				fmt.Fprintln(out, "  This cannot be undone.")
				if !promptYesNo(cmd.InOrStdin(), out, "Fold?") {
					fmt.Fprintln(out, "Aborted; nothing was folded.")
					return nil
				}
			}

			res, err := eng.Fold(ctx, args[0], args[1])
			if err != nil {
				return err
			}
			if res.Settled {
				fmt.Fprintf(out, "recorded current changes as %s\n", res.SettledID)
			}
			fmt.Fprintf(out, "Folded %d %s into %s.\n",
				res.Dropped, plural(res.Dropped, "snapshot", "snapshots"), abbrev(res.Folded))
			if res.HeadMovedTo != "" {
				fmt.Fprintf(out, "HEAD is now %s.\n", abbrev(res.HeadMovedTo))
			}
			reportReclaimed(out, res.Reclaimed)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	return cmd
}
