package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
)

// newGoCmd builds `spor go <ref>`, which jumps the working tree back to
// a past state (docs/design-spec.md §5, §6).
func newGoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "go <ref>",
		Short: "Jump the project back to a past snapshot",
		Long: "Restore your project files to exactly how they were at an earlier snapshot. " +
			"Any changes you had not snapshotted yet are recorded first, so a jump is " +
			"always reversible, nothing is lost.\n\n" +
			"A <ref> picks the snapshot to go to:\n" +
			"  @~n          n snapshots back from the current one\n" +
			"  <label>      a snapshot you named with 'snap -l' or 'label'\n" +
			"  <time>       a duration back from now, e.g. 2h or 3d\n" +
			"               (units: s, m, h, d)\n" +
			"  <id>         a snapshot id, or just its first few characters",
		Example: `  # Jump back to how things were 2 hours ago
  spor go 2h

  # Go back 2 states from where you are
  spor go @~2

  # Jump to a specific state by its id (or a short prefix of it)
  spor go 01ARZ7

  # Jump to a state you named
  spor go before-refactor`,
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

			res, err := eng.Go(ctx, ref)
			if err != nil {
				return err
			}
			out := styledOut(cmd)
			if res.Settled {
				fmt.Fprintf(out, "recorded current changes as %s\n", styleAccent.Render(res.SettledID))
			}
			fmt.Fprintf(out, "went to %s (%s written, %s removed)\n",
				styleAccent.Render(res.StateID),
				styleGood.Render(fmt.Sprintf("%d", res.Written)),
				styleBad.Render(fmt.Sprintf("%d", res.Deleted)))
			return nil
		},
	}
}
