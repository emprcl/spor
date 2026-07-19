package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
	"github.com/emprcl/spor/internal/textfmt"
)

// newPullCmd builds `spor pull`, which brings the server's history into this
// machine (docs/design-spec.md §7).
func newPullCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Bring the server's history to this machine",
		Long: "Bring in snapshots from the sync server.\n\n" +
			"Snapshots another machine removed are removed here too, so a thin done there " +
			"reclaims space here as well. Work only this machine has is always kept: if " +
			"you snapped on both machines, you simply get a branch.\n\n" +
			"Nothing you are standing on is ever removed, and your working files are not " +
			"touched: pull only changes the history, never the tree. Use 'spor go' to " +
			"move to a snapshot it brought in.",
		Example: `  # Bring in everything new
  spor pull

  # Settle disagreements in the server's favor
  spor pull --force`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			eng, err := openOrInitHere(ctx)
			if err != nil {
				return err
			}
			defer eng.Close()

			res, err := eng.Pull(ctx, core.SyncOptions{Force: force})
			if err != nil {
				return err
			}

			out := styledOut(cmd)
			if res.NoOp {
				fmt.Fprintln(out, th.Muted.Render("Already up to date; nothing to bring in."))
				return nil
			}

			switch {
			case res.Added > 0 && res.Removed > 0:
				fmt.Fprintf(out, "Pulled; added %s, removed %s.\n",
					th.Good.Render(fmt.Sprintf("%d %s", res.Added, textfmt.Plural(res.Added, "snapshot", "snapshots"))),
					th.Bad.Render(fmt.Sprintf("%d", res.Removed)))
			case res.Added > 0:
				fmt.Fprintf(out, "Pulled %s.\n", th.Good.Render(fmt.Sprintf("%d %s",
					res.Added, textfmt.Plural(res.Added, "snapshot", "snapshots"))))
			case res.Removed > 0:
				fmt.Fprintf(out, "Pulled; removed %s.\n", th.Bad.Render(fmt.Sprintf("%d %s",
					res.Removed, textfmt.Plural(res.Removed, "snapshot", "snapshots"))))
			default:
				fmt.Fprintln(out, "Pulled; history rearranged.")
			}

			if res.BlobsDownloaded > 0 {
				fmt.Fprintf(out, "  %s\n", th.Muted.Render(fmt.Sprintf("downloaded %d %s",
					res.BlobsDownloaded, textfmt.Plural(res.BlobsDownloaded, "file", "files"))))
			}
			// Anything below changed the history in a way the user did not directly
			// ask for, so it is reported rather than left to be discovered.
			if n := len(res.Resurrected); n > 0 {
				fmt.Fprintf(out, "  %s\n", th.Muted.Render(fmt.Sprintf(
					"kept %d %s another machine removed, because your work builds on them",
					n, textfmt.Plural(n, "snapshot", "snapshots"))))
			}
			for _, c := range res.LabelsCleared {
				fmt.Fprintf(out, "  %s\n", th.Bad.Render(fmt.Sprintf(
					"the name %q now belongs to %s; it was cleared from %s",
					c.Label, textfmt.Abbrev(c.Kept), textfmt.Abbrev(c.Cleared))))
			}
			if n := len(res.ForcedRemote); n > 0 {
				fmt.Fprintf(out, "  %s\n", th.Bad.Render(fmt.Sprintf(
					"took the server's version of %d disagreeing %s",
					n, textfmt.Plural(n, "snapshot", "snapshots"))))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"settle disagreements in the server's favor instead of stopping")
	return cmd
}
