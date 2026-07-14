package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
)

// newKeepfromCmd builds `spor keepfrom <ref>`, the dual of dropfrom: it keeps only
// the target and its descendants, dropping everything else (docs/design-spec.md §5, §6).
// It is destructive, so it confirms first and reports exactly what will be dropped.
func newKeepfromCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "keepfrom <ref>",
		Short: "Make a state the new root, dropping the rest",
		Long: "Keep a state and everything below it, and permanently drop everything " +
			"else: the state's ancestors and any side branches. This is how a long " +
			"project forgets old history and reclaims space while keeping everything " +
			"from a chosen point forward. If you are on a branch being dropped, HEAD " +
			"moves to the new root (your working files are re-materialized to match). " +
			"This cannot be undone.\n\n" +
			"A <ref> selects the state; see 'spor go --help' for the forms.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := strings.Join(args, " ")
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

			plan, err := eng.KeepfromPlan(ctx, ref)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if plan.IsNoop {
				fmt.Fprintf(out, "%s already contains the whole history; nothing to drop.\n", abbrev(plan.Target))
				return nil
			}

			if !yes {
				fmt.Fprintf(out, "Keeping from %s keeps %d %s and drops %d %s.\n",
					abbrev(plan.Target),
					plan.StatesKept, plural(plan.StatesKept, "state", "states"),
					plan.StatesToDrop, plural(plan.StatesToDrop, "state", "states"))
				if plan.HeadWillMove {
					fmt.Fprintln(out, "  You are on a branch being dropped; HEAD will move to the new root and your working files will change to match.")
				}
				fmt.Fprintln(out, "  This cannot be undone.")
				if !promptYesNo(cmd.InOrStdin(), out, "Keep from here?") {
					fmt.Fprintln(out, "Aborted; nothing was dropped.")
					return nil
				}
			}

			res, err := eng.Keepfrom(ctx, ref)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Kept from %s; dropped %d %s, kept %d.\n",
				abbrev(res.Target), res.Dropped, plural(res.Dropped, "state", "states"), res.Kept)
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
