package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
)

// newTrimCmd builds `spor trim <ref>`, the dual of drop: it keeps only
// the target and its descendants, dropping everything else (docs/design-spec.md §5, §6).
// It is destructive, so it confirms first and reports exactly what will be dropped.
func newTrimCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "trim <ref>",
		Short: "Drop everything before a snapshot, keeping it and what follows",
		Long: "Trim old history: keep a snapshot and everything after it, and permanently " +
			"drop everything before, including any side branches. This is how a " +
			"long-running project forgets old history and reclaims disk space while " +
			"keeping everything from a chosen point forward. If you are on a snapshot " +
			"being dropped, you move to the kept snapshot and your files change to " +
			"match. This cannot be undone.\n\n" +
			"A <ref> selects the snapshot; see 'spor go --help' for the forms.",
		Example: `  # Forget everything before v1.0, keeping it and all later work
  spor trim v1.0`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := args[0]
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

			plan, err := eng.TrimPlan(ctx, ref)
			if err != nil {
				return err
			}
			out := styledOut(cmd)
			if plan.IsNoop {
				fmt.Fprintln(out, styleMuted.Render(fmt.Sprintf("%s already contains the whole history; nothing to drop.", abbrev(plan.Target))))
				return nil
			}

			if !yes {
				fmt.Fprintf(out, "Trimming to %s keeps %s and drops %s.\n",
					styleAccent.Render(abbrev(plan.Target)),
					styleGood.Render(fmt.Sprintf("%d %s", plan.StatesKept, plural(plan.StatesKept, "snapshot", "snapshots"))),
					styleBad.Render(fmt.Sprintf("%d %s", plan.StatesToDrop, plural(plan.StatesToDrop, "snapshot", "snapshots"))))
				if plan.HeadWillMove {
					fmt.Fprintln(out, styleMuted.Render("  You are on a branch being dropped; HEAD will move to the new root and your working files will change to match."))
				}
				fmt.Fprintln(out, styleBad.Render("  This cannot be undone."))
				if !promptYesNo(cmd.InOrStdin(), out, "Trim?") {
					fmt.Fprintln(out, styleBad.Render("Aborted; nothing was dropped."))
					return nil
				}
			}

			res, err := eng.Trim(ctx, ref)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Trimmed to %s; dropped %s, kept %s.\n",
				styleAccent.Render(abbrev(res.Target)),
				styleBad.Render(fmt.Sprintf("%d %s", res.Dropped, plural(res.Dropped, "snapshot", "snapshots"))),
				styleGood.Render(fmt.Sprintf("%d", res.Kept)))
			if res.HeadMovedTo != "" {
				fmt.Fprintf(out, "HEAD is now %s.\n", styleAccent.Render(abbrev(res.HeadMovedTo)))
			}
			reportReclaimed(out, res.Reclaimed)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	return cmd
}
