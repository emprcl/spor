// Package cli wires the spor command surface. Commands are thin front-ends over
// internal/core; see docs/SPEC.md §6 (CLI & UX) and §8 (process model).
package cli

import (
	"github.com/spf13/cobra"
)

// Root builds the top-level spor command tree.
func Root() *cobra.Command {
	root := &cobra.Command{
		Use:   "spor",
		Short: "Infinite undo for your whole project",
		Long: "spor saves a snapshot of your project every time it changes, so you " +
			"can jump back to any past state, or branch off to explore a different " +
			"path, all with one command. Built for creative workflows: no commits, " +
			"no staging, no version-control ceremony.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(newWatchCmd())
	root.AddCommand(newSnapCmd())
	root.AddCommand(newLogCmd())
	root.AddCommand(newGoCmd())
	root.AddCommand(newDiffCmd())
	root.AddCommand(newLabelCmd())
	root.AddCommand(newUndoCmd())
	root.AddCommand(newRedoCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newForgetCmd())
	root.AddCommand(newDropfromCmd())
	root.AddCommand(newKeepfromCmd())
	root.AddCommand(newGCCmd())
	root.AddCommand(newVerifyCmd())

	return root
}
