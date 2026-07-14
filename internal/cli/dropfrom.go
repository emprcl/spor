package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
)

// newDropfromCmd builds `spor dropfrom <ref>`, which deletes a state and all its
// descendants (docs/design-spec.md §5, §6). It is destructive, so it confirms first and
// reports exactly what will be removed.
func newDropfromCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "dropfrom <ref>",
		Short: "Delete a state and all its descendants",
		Long: "Permanently delete a state and everything below it in the history tree. " +
			"On a leaf this drops just that state; on the current state after an undo it " +
			"drops the whole forward branch; on the root it wipes all history. If you are " +
			"on a state being deleted, HEAD moves to its parent (your working files are " +
			"re-materialized to match). This cannot be undone.\n\n" +
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

			plan, err := eng.DropfromPlan(ctx, ref)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if !yes {
				fmt.Fprintf(out, "Dropping from %s deletes %d %s.\n",
					abbrev(plan.Target), plan.StatesToDelete, plural(plan.StatesToDelete, "state", "states"))
				if plan.WipesEntireStore {
					fmt.Fprintln(out, "  This wipes ALL history; your working files are left untouched.")
				} else if plan.HeadWillMove {
					fmt.Fprintf(out, "  HEAD will move to %s and your working files will change to match.\n", abbrev(plan.HeadTarget))
				}
				fmt.Fprintln(out, "  This cannot be undone.")
				if !promptYesNo(cmd.InOrStdin(), out, "Drop?") {
					fmt.Fprintln(out, "Aborted; nothing was deleted.")
					return nil
				}
			}

			res, err := eng.Dropfrom(ctx, ref)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Dropped %d %s.\n", res.Deleted, plural(res.Deleted, "state", "states"))
			switch {
			case res.HeadCleared:
				fmt.Fprintln(out, "All history is gone; the next snap starts a fresh timeline.")
			case res.HeadMovedTo != "":
				fmt.Fprintf(out, "HEAD is now %s.\n", abbrev(res.HeadMovedTo))
			}
			reportReclaimed(out, res.Reclaimed)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	return cmd
}

// reportReclaimed prints a GC summary line when a history-editing op freed blobs.
func reportReclaimed(out io.Writer, gc core.GCResult) {
	if gc.Removed > 0 {
		fmt.Fprintf(out, "Reclaimed %s from %d unreferenced %s.\n",
			humanBytes(gc.Bytes), gc.Removed, plural(gc.Removed, "blob", "blobs"))
	}
}
