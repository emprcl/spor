package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
	"github.com/emprcl/spor/internal/textfmt"
)

// newPushCmd builds `spor push`, which sends this machine's history to the
// configured server (docs/design-spec.md §7).
func newPushCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Send your history to the server",
		Long: "Send your snapshots and their contents to the sync server.\n\n" +
			"Everything travels: snapshots you added, and snapshots you removed with " +
			"drop, trim, fold, or thin. The server ends up matching this machine.\n\n" +
			"If another of your machines has pushed since you last synced, push stops and " +
			"asks you to pull first, so its work is never overwritten by accident.",
		Example: `  # Send everything new
  spor push

  # Overwrite the server with this machine's history
  spor push --force`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			eng, err := openHere(ctx)
			if err != nil {
				return err
			}
			defer eng.Close()

			res, err := eng.Push(ctx, core.SyncOptions{Force: force})
			if err != nil {
				return err
			}

			out := styledOut(cmd)
			if res.NoOp {
				fmt.Fprintln(out, th.Muted.Render("Already up to date; nothing to send."))
				return nil
			}

			fmt.Fprintf(out, "Pushed %s.\n", th.Good.Render(fmt.Sprintf("%d %s",
				res.States, textfmt.Plural(res.States, "snapshot", "snapshots"))))
			if res.BlobsUploaded > 0 {
				fmt.Fprintf(out, "  %s\n", th.Muted.Render(fmt.Sprintf("uploaded %d %s (%s)",
					res.BlobsUploaded, textfmt.Plural(res.BlobsUploaded, "file", "files"),
					textfmt.HumanBytes(res.BytesUploaded))))
			} else {
				fmt.Fprintf(out, "  %s\n", th.Muted.Render("the server already had every file"))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"overwrite the server even if another machine has pushed since you last synced")
	return cmd
}
