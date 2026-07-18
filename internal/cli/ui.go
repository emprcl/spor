package cli

import (
	"errors"
	"os"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
	"github.com/emprcl/spor/internal/tui"
)

// newUICmd builds `spor ui`: the interactive front-end (docs/design-spec.md §6).
// It hosts the full TUI; whether the project is also being watched is a mode
// inside it (offered on startup, toggled with w), not a separate command.
func newUICmd() *cobra.Command {
	var watch, browse bool
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Open the interactive view",
		Long: "Open the interactive view of the history: navigate the tree, jump back, " +
			"diff, name, pick files out of, prune, and squash snapshots.\n\n" +
			"On startup it offers to watch the project and record changes automatically; " +
			"toggle watching any time with w (only one watcher can run per project). " +
			"When not watching, record a snapshot by hand with s. Use --watch or " +
			"--browse to pick the startup mode and skip the offer.\n\n" +
			"Needs a terminal; use 'spor log' to print the history instead.",
		Example: `  # Open the interactive view
  spor ui

  # Open watching, recording changes right away
  spor ui --watch

  # Open in browse mode, skipping the watch offer
  spor ui --browse`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			eng, err := core.OpenOrInit(ctx, cwd)
			if err != nil {
				return err
			}
			defer eng.Close()

			f, isTTY := ttyFile(cmd)
			if !isTTY {
				return errors.New("spor ui needs a terminal; use 'spor log' to print the history")
			}
			return tui.Run(ctx, tui.Config{
				Engine:      eng,
				Root:        eng.Root(),
				Theme:       th,
				ForceWatch:  watch,
				ForceBrowse: browse,
			}, f)
		},
	}
	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "start watching immediately, skipping the offer")
	cmd.Flags().BoolVarP(&browse, "browse", "b", false, "open in browse mode, skipping the offer")
	cmd.MarkFlagsMutuallyExclusive("watch", "browse")
	return cmd
}

// ttyFile reports whether the command's output is a terminal, returning the
// underlying file when it is.
func ttyFile(cmd *cobra.Command) (*os.File, bool) {
	if f, ok := cmd.OutOrStdout().(*os.File); ok && term.IsTerminal(f.Fd()) {
		return f, true
	}
	return nil, false
}
