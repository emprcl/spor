package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
)

// newGCCmd builds `spor gc`, which reclaims storage from blobs no state
// references (docs/SPEC.md §6, §8). GC is mostly automatic after dropfrom/fold;
// this runs it on demand.
func newGCCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "gc",
		Short: "Reclaim storage from unreferenced blobs",
		Args:  cobra.NoArgs,
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
			out := cmd.OutOrStdout()
			if res.Removed == 0 {
				fmt.Fprintln(out, "Nothing to reclaim; every blob is still referenced.")
				return nil
			}
			fmt.Fprintf(out, "Reclaimed %s from %d unreferenced %s.\n",
				humanBytes(res.Bytes), res.Removed, plural(res.Removed, "blob", "blobs"))
			return nil
		},
	}
}
