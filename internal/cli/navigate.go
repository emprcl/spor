package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
)

// newUndoCmd builds `spor undo [n]`, stepping HEAD back n states (default 1).
func newUndoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "undo [n]",
		Short: "Step back to an earlier state",
		Long: "Move back n states (default 1) along the current line of history and " +
			"restore that state. Undo is reversible with redo. If you ask for more " +
			"steps than exist, it stops at the oldest state.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := parseSteps(args)
			if err != nil {
				return err
			}
			return runMove(cmd, "undo", func(ctx context.Context, eng *core.Engine) (core.MoveResult, error) {
				return eng.Undo(ctx, n)
			})
		},
	}
}

// newRedoCmd builds `spor redo [n]`, stepping HEAD forward n states (default 1).
func newRedoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "redo [n]",
		Short: "Step forward to a state you undid",
		Long: "Move forward n states (default 1), following the branch you most " +
			"recently left. If you ask for more steps than exist, it stops at the " +
			"newest state. Other branches are reached with 'log' and 'restore'.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := parseSteps(args)
			if err != nil {
				return err
			}
			return runMove(cmd, "redo", func(ctx context.Context, eng *core.Engine) (core.MoveResult, error) {
				return eng.Redo(ctx, n)
			})
		},
	}
}

// parseSteps reads the optional count argument, defaulting to 1 and rejecting
// anything that is not a positive integer.
func parseSteps(args []string) (int, error) {
	if len(args) == 0 {
		return 1, nil
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n < 1 {
		return 0, fmt.Errorf("count must be a positive integer, got %q", args[0])
	}
	return n, nil
}

// runMove opens the store, runs an undo/redo move, and reports the outcome. verb
// is "undo" or "redo", used in the messages; the two commands differ only in the
// core call and their boundary wording.
func runMove(cmd *cobra.Command, verb string, do func(context.Context, *core.Engine) (core.MoveResult, error)) error {
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

	res, err := do(ctx, eng)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if res.Steps == 0 {
		edge := "oldest"
		if verb == "redo" {
			edge = "newest"
		}
		fmt.Fprintf(out, "already at the %s state, nothing to %s\n", edge, verb)
		return nil
	}
	if res.Settled {
		fmt.Fprintf(out, "recorded current changes as %s\n", res.SettledID)
	}
	fmt.Fprintf(out, "%s %d state(s) to %s (%d written, %d removed)\n",
		past(verb), res.Steps, res.StateID, res.Written, res.Deleted)
	return nil
}

// past renders the past tense of the move verb for the result line.
func past(verb string) string {
	if verb == "redo" {
		return "redid"
	}
	return "undid"
}
