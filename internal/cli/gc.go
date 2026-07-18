package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
	"github.com/emprcl/spor/internal/textfmt"
)

// newGCCmd builds `spor gc`, which reclaims storage from blobs no state
// references (docs/design-spec.md §6, §8). GC is mostly automatic after drop/fold;
// this runs it on demand.
func newGCCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "gc",
		Short: "Reclaim disk space from unreferenced data",
		Long: "Reclaim disk space held by file contents that no snapshot references " +
			"anymore, left behind after drop or fold. This runs automatically after " +
			"those commands; run it by hand to reclaim space at any time. It never " +
			"removes anything a surviving snapshot still needs.",
		Example: `  # Reclaim disk space from data no longer referenced
  spor gc`,
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

			res, err := eng.GC(ctx)
			if err != nil {
				return err
			}
			out := styledOut(cmd)
			if res.Removed == 0 {
				fmt.Fprintln(out, th.Muted.Render("Nothing to reclaim; every blob is still referenced."))
				return nil
			}
			fmt.Fprintf(out, "Reclaimed %s from %s.\n",
				th.Accent.Render(textfmt.HumanBytes(res.Bytes)),
				th.Muted.Render(fmt.Sprintf("%d unreferenced %s", res.Removed, textfmt.Plural(res.Removed, "blob", "blobs"))))
			return nil
		},
	}
}
