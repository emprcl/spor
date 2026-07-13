package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
)

// newRestoreCmd builds `spor restore <ref>`, which jumps the working tree back to
// a past state (docs/SPEC.md §5, §6). Trailing args are joined into the ref, so
// `spor restore 2h ago` works without quoting.
func newRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <ref>",
		Short: "Jump the project back to a past state",
		Long: "Materialize a past state into the working directory. Any uncommitted " +
			"changes are recorded as their own state first, so the jump is always " +
			"undoable. A <ref> is @, @~n, a label, a time like \"2h ago\", or a state id.",
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

			res, err := eng.Restore(ctx, ref)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if res.Settled {
				fmt.Fprintf(out, "recorded current changes as %s\n", res.SettledID)
			}
			fmt.Fprintf(out, "restored %s (%d written, %d removed)\n",
				res.StateID, res.Written, res.Deleted)
			return nil
		},
	}
}
