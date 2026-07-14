package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
)

// newGoCmd builds `spor go <ref>`, which jumps the working tree back to
// a past state (docs/design-spec.md §5, §6). Trailing args are joined into the ref, so
// `spor go 2h ago` works without quoting.
func newGoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "go <ref>",
		Short: "Jump the project back to a past state",
		Long: "Materialize a past state into the working directory. Any uncommitted " +
			"changes are recorded as their own state first, so the jump is always " +
			"undoable.\n\n" +
			"A <ref> selects the state to go to:\n" +
			"  @~n          n states back from the current one\n" +
			"  <label>      a state named with 'snap -l' or 'label'\n" +
			"  <time>       how long ago, e.g. \"2h ago\" or \"3d\"\n" +
			"               (units: s, m, h, d; the word \"ago\" is optional)\n" +
			"  <id>         a state id, or just its first few characters",
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

			res, err := eng.Go(ctx, ref)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if res.Settled {
				fmt.Fprintf(out, "recorded current changes as %s\n", res.SettledID)
			}
			fmt.Fprintf(out, "went to %s (%d written, %d removed)\n",
				res.StateID, res.Written, res.Deleted)
			return nil
		},
	}
}
