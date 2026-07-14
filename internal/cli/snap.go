package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
)

// newSnapCmd builds `spor snap`, the manual, watcher-free way to record
// a state (docs/design-spec.md §4, §6).
func newSnapCmd() *cobra.Command {
	var label string

	cmd := &cobra.Command{
		Use:   "snap",
		Short: "Record the current project state now",
		Long: "Record the current contents of your project as a new state you can jump " +
			"back to later. If nothing has changed since the last one, nothing is " +
			"recorded. This is the manual alternative to leaving 'spor watch' running.",
		Example: `  # Record the current state
  spor snap

  # Record it with a name you can jump back to
  spor snap -l "before refactor"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}

			ctx := cmd.Context()
			eng, err := core.OpenOrInit(ctx, root)
			if err != nil {
				return err
			}
			defer eng.Close()

			res, err := eng.Snap(ctx, core.SnapOptions{Label: label})
			if err != nil {
				return err
			}
			if !res.Created {
				fmt.Fprintln(cmd.OutOrStdout(), "nothing to snap")
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "snap %s\n", res.StateID)
			return nil
		},
	}

	cmd.Flags().StringVarP(&label, "label", "l", "", "name for this state")
	return cmd
}
