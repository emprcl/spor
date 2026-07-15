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
		Short: "Save one snapshot by hand",
		Long: "Record the current contents of your project as a new snapshot you can jump " +
			"back to later. If nothing has changed since the last one, nothing is " +
			"recorded. This is the manual path: you only need it when 'spor watch' " +
			"isn't running, since the watcher records everything automatically.\n\n" +
			"Files matched by .sporignore, or by spor's built-in defaults (build " +
			"artifacts, editor temp files, .git), are never recorded.",
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
			fmt.Fprintf(cmd.OutOrStdout(), "snapshot %s\n", res.StateID)
			return nil
		},
	}

	cmd.Flags().StringVarP(&label, "label", "l", "", "name for this snapshot")
	return cmd
}
