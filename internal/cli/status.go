package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
)

// newStatusCmd builds `spor status`, a quick read of whether a watcher is running
// and where the current state (@) is (docs/SPEC.md §6).
func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether a watcher is running and where @ is",
		Args:  cobra.NoArgs,
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

			st, err := eng.Status(ctx)
			if err != nil {
				return err
			}
			out := colorprofile.NewWriter(cmd.OutOrStdout(), os.Environ())
			renderStatus(out, st)
			return nil
		},
	}
}

var (
	styleStatusKey = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleStatusOn  = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
)

// renderStatus prints an aligned key/value view of the project's status.
func renderStatus(w io.Writer, st core.StatusResult) {
	row := func(keyStyle lipgloss.Style, key, val string) {
		fmt.Fprintln(w, keyStyle.Render(key)+strings.Repeat(" ", 9-len(key))+val)
	}
	row(styleStatusKey, "project", st.Root)
	if st.WatcherRunning {
		row(styleStatusKey, "watcher", styleStatusOn.Render("running"))
	} else {
		row(styleStatusKey, "watcher", styleStatusKey.Render("not running"))
	}
	// The @ marker is colored like the current-state marker in `spor log`.
	if st.HasHead {
		val := styleID.Render(abbrev(st.Head.ID))
		if st.Head.Label != "" {
			val += "  " + styleLabel.Render(st.Head.Label)
		}
		val += "  " + styleTime.Render(humanizeSince(st.Head.CreatedAt))
		row(styleHeadTag, "@", val)
	} else {
		row(styleHeadTag, "@", styleStatusKey.Render("no states yet"))
	}
}
