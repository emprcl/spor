package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
	"github.com/emprcl/spor/internal/textfmt"
)

// newThinCmd builds `spor thin`, which reduces the whole history to its
// structural skeleton: it keeps every tip, branch point, and labeled snapshot
// (and @), and drops the linear in-between snapshots, reconnecting the survivors
// (docs/design-spec.md §5). It is the persistent form of the folding `spor log`
// shows. It is destructive, so it confirms first, but it never touches the
// working tree.
func newThinCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "thin",
		Short: "Reduce history to its tips and branch points",
		Long: "Reduce the whole history to its structural skeleton: keep every tip (the " +
			"last snapshot of each timeline), every branch point where timelines diverge, " +
			"and every snapshot you named, and permanently drop the linear in-between " +
			"snapshots, reconnecting what survives. It is the lasting form of the folding " +
			"'spor log' already shows for long linear runs. Your working files are never " +
			"touched and where you are (@) never moves. This cannot be undone.\n\n" +
			"A purely linear history, with no branches, reduces to just its latest snapshot.",
		Example: `  # Collapse linear runs, keeping tips, branch points, and labels
  spor thin`,
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

			plan, err := eng.ThinPlan(ctx)
			if err != nil {
				return err
			}
			out := styledOut(cmd)
			if plan.IsNoop {
				fmt.Fprintln(out, th.Muted.Render("Nothing to thin; the history is already all tips and branch points."))
				return nil
			}

			if !yes {
				fmt.Fprintf(out, "Thinning keeps %s and drops %s.\n",
					th.Good.Render(fmt.Sprintf("%d %s", plan.StatesKept, textfmt.Plural(plan.StatesKept, "snapshot", "snapshots"))),
					th.Bad.Render(fmt.Sprintf("%d linear %s", plan.StatesToDrop, textfmt.Plural(plan.StatesToDrop, "snapshot", "snapshots"))))
				fmt.Fprintln(out, th.Muted.Render("  Tips, branch points, labels, and @ are kept; your working files are left untouched."))
				fmt.Fprintln(out, th.Bad.Render("  This cannot be undone."))
				if !promptYesNo(cmd.InOrStdin(), out, "Thin?") {
					fmt.Fprintln(out, th.Bad.Render("Aborted; nothing was dropped."))
					return nil
				}
			}

			res, err := eng.Thin(ctx)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Thinned; dropped %s, kept %s.\n",
				th.Bad.Render(fmt.Sprintf("%d %s", res.Dropped, textfmt.Plural(res.Dropped, "snapshot", "snapshots"))),
				th.Good.Render(fmt.Sprintf("%d", res.Kept)))
			reportReclaimed(out, res.Reclaimed)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	return cmd
}
