package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
	"github.com/emprcl/spor/internal/textfmt"
)

// newForgetCmd builds `spor forget`, the escape hatch out of "infinite undo": it
// deletes the entire .spor store, keeping the working files (docs/design-spec.md §5,
// §6). It refuses while a watcher runs and confirms before deleting.
func newForgetCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "forget",
		Short: "Delete all of spor's history for this project",
		Long: "Permanently remove the .spor store: every snapshot and all history. Your " +
			"actual project files are left untouched. This cannot be undone. Afterwards " +
			"the project is no longer tracked, and the next 'spor snap' or 'spor watch' " +
			"starts fresh.",
		Example: `  # Delete spor's history for this project (your files are kept)
  spor forget`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			eng, err := core.OpenForRepair(ctx, root)
			if err != nil {
				return err
			}
			defer eng.Close()

			// Refuse before prompting if a watcher is running, so the user is not
			// asked to confirm an operation that will fail.
			running, err := eng.WatcherRunning()
			if err != nil {
				return err
			}
			if running {
				return errors.New("a watcher is running; stop 'spor watch' before forgetting the store")
			}

			stats, err := eng.ForgetStats(ctx)
			if err != nil {
				return err
			}

			out := styledOut(cmd)
			if !yes {
				fmt.Fprintf(out, "This permanently deletes the spor store at %s\n", th.Accent.Render(stats.StoreDir))
				fmt.Fprintf(out, "  %s, %s of history and blobs.\n",
					th.Bad.Render(fmt.Sprintf("%d %s", stats.StateCount, textfmt.Plural(stats.StateCount, "snapshot", "snapshots"))),
					th.Accent.Render(textfmt.HumanBytes(stats.Bytes)))
				if stats.HeadBehind {
					fmt.Fprintln(out, th.Bad.Render(fmt.Sprintf(
						"  Your files match an older snapshot, not the last one saved: the %d %s saved after it %s deleted too.",
						stats.NewerStates,
						textfmt.Plural(stats.NewerStates, "snapshot", "snapshots"),
						textfmt.Plural(stats.NewerStates, "is", "are"))))
				}
				fmt.Fprintln(out, th.Muted.Render("  Your working files are left untouched. ")+th.Bad.Render("This cannot be undone."))
				if !promptYesNo(cmd.InOrStdin(), out, "Delete the store?") {
					fmt.Fprintln(out, th.Bad.Render("Aborted; nothing was deleted."))
					return nil
				}
			}

			if err := eng.Forget(ctx); err != nil {
				return err
			}
			fmt.Fprintf(out, "Deleted the spor store; %s is no longer tracked.\n", th.Accent.Render(root))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	return cmd
}

// promptYesNo asks a yes/no question, defaulting to no. A closed or empty input
// (a pipe with no data, EOF) counts as no, so an unattended run never deletes.
func promptYesNo(in io.Reader, out io.Writer, question string) bool {
	fmt.Fprintf(out, "%s [y/N]: ", question)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
