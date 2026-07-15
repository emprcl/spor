package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
)

// newDropCmd builds `spor drop <ref>`, which deletes a state and all its
// descendants (docs/design-spec.md §5, §6). It is destructive, so it confirms first and
// reports exactly what will be removed.
func newDropCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "drop <ref>",
		Short: "Delete a snapshot and everything after it",
		Long: "Permanently delete a snapshot and everything that came after it. On the " +
			"newest snapshot this drops just that one; after an undo it drops the whole " +
			"forward branch; on the very first snapshot it wipes all history. If you are " +
			"on a snapshot being deleted, you move to the one before it and your files " +
			"change to match. This cannot be undone.\n\n" +
			"A <ref> selects the snapshot; see 'spor go --help' for the forms.",
		Example: `  # Delete the current state and everything after it
  spor drop @

  # Delete a branch by its id
  spor drop 01ARZ7

  # Skip the confirmation prompt
  spor drop @ -y`,
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

			plan, err := eng.DropPlan(ctx, ref)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if !yes {
				fmt.Fprintf(out, "Dropping from %s deletes %d %s.\n",
					abbrev(plan.Target), plan.StatesToDelete, plural(plan.StatesToDelete, "snapshot", "snapshots"))
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

			res, err := eng.Drop(ctx, ref)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Dropped %d %s.\n", res.Deleted, plural(res.Deleted, "snapshot", "snapshots"))
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
