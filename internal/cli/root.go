// Package cli wires the spor command surface. Commands are thin front-ends over
// internal/core; see docs/SPEC.md §6 (CLI & UX) and §8 (process model).
package cli

import (
	"github.com/spf13/cobra"
)

// Command groups for `spor --help`, ordered most-used first and grouped by
// feature (mirroring docs/SPEC.md §6) instead of the default alphabetical list.
const (
	groupEveryday    = "everyday"
	groupInspect     = "inspect"
	groupHistory     = "history"
	groupStartOver   = "startover"
	groupMaintenance = "maintenance"
	groupOther       = "other"
)

// Root builds the top-level spor command tree.
func Root() *cobra.Command {
	// Show commands in declaration order within each group, not alphabetically, so
	// the everyday commands lead and related ones stay together.
	cobra.EnableCommandSorting = false

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

	root.AddGroup(
		&cobra.Group{ID: groupEveryday, Title: "Common"},
		&cobra.Group{ID: groupInspect, Title: "Naming & inspecting"},
		&cobra.Group{ID: groupHistory, Title: "History editing"},
		&cobra.Group{ID: groupStartOver, Title: "Starting over"},
		&cobra.Group{ID: groupMaintenance, Title: "Maintenance"},
		&cobra.Group{ID: groupOther, Title: "Other"},
	)

	// addGroup assigns a group to each command as it is registered; the order here
	// is the order shown under each heading.
	addGroup := func(id string, cmds ...*cobra.Command) {
		for _, c := range cmds {
			c.GroupID = id
			root.AddCommand(c)
		}
	}

	addGroup(groupEveryday,
		newWatchCmd(), newSnapCmd(), newLogCmd(), newUndoCmd(), newRedoCmd(), newGoCmd())
	addGroup(groupInspect,
		newLabelCmd(), newDiffCmd(), newStatusCmd())
	addGroup(groupHistory,
		newDropfromCmd(), newKeepfromCmd(), newFoldCmd())
	addGroup(groupStartOver,
		newForgetCmd())
	addGroup(groupMaintenance,
		newVerifyCmd(), newGCCmd())

	// The built-in help and completion commands are utilities, not everyday verbs;
	// keep them out of the default (top) group and in "Other" at the bottom.
	root.SetHelpCommandGroupID(groupOther)
	root.SetCompletionCommandGroupID(groupOther)

	return root
}
