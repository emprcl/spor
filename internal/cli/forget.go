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
)

// newForgetCmd builds `spor forget`, the escape hatch out of "infinite undo": it
// deletes the entire .spor store, keeping the working files (docs/design-spec.md §5,
// §6). It refuses while a watcher runs and confirms before deleting.
func newForgetCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "forget",
		Short: "Delete the entire spor store for this project",
		Long: "Permanently remove the .spor store: every state, all history, and all " +
			"blobs. Your working files are left untouched. This cannot be undone. " +
			"Afterwards the project is no longer tracked, and the next snap or " +
			"'spor watch' starts a fresh store.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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

			out := cmd.OutOrStdout()
			if !yes {
				fmt.Fprintf(out, "This permanently deletes the spor store at %s\n", stats.StoreDir)
				fmt.Fprintf(out, "  %d %s, %s of history and blobs.\n",
					stats.StateCount, plural(stats.StateCount, "state", "states"), humanBytes(stats.Bytes))
				fmt.Fprintln(out, "  Your working files are left untouched. This cannot be undone.")
				if !promptYesNo(cmd.InOrStdin(), out, "Delete the store?") {
					fmt.Fprintln(out, "Aborted; nothing was deleted.")
					return nil
				}
			}

			if err := eng.Forget(); err != nil {
				return err
			}
			fmt.Fprintf(out, "Deleted the spor store; %s is no longer tracked.\n", root)
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

// humanBytes formats a byte count with a binary (1024) unit suffix.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// plural picks the singular or plural word for a count.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
