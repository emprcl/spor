package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
)

// newSnapshotCmd builds `spor snapshot`, the manual, watcher-free way to record
// a state (docs/SPEC.md §4, §6).
func newSnapshotCmd() *cobra.Command {
	var label string

	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Record the current project state now",
		Long: "Walk the project in the current directory and record it as a new state. " +
			"If nothing changed since the last state, nothing is recorded.",
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

			res, err := eng.Snapshot(ctx, core.SnapshotOptions{Label: label})
			if err != nil {
				return err
			}
			if !res.Created {
				fmt.Fprintln(cmd.OutOrStdout(), "nothing to snapshot")
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "snapshot %s\n", res.StateID)
			return nil
		},
	}

	cmd.Flags().StringVarP(&label, "label", "l", "", "name for this state")
	return cmd
}
