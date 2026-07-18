package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
	"github.com/emprcl/spor/internal/textfmt"
)

// newStatusCmd builds `spor status`, a quick read of whether a watcher is running
// and where the current state (@) is (docs/design-spec.md §6).
func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show a summary of the project and where you are",
		Long: "A quick overview: the project path, whether a watcher is running, how " +
			"large the history is (snapshots and branches), how much disk the store " +
			"uses, and where the current snapshot (@) sits, whether you are on the latest " +
			"or have rewound with newer snapshots still ahead.",
		Example: `  # Show where you are and how large the history is
  spor status`,
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

			st, err := eng.Status(ctx)
			if err != nil {
				return err
			}
			renderStatus(styledOut(cmd), st)
			return nil
		},
	}
}

// renderStatus prints an aligned key/value view of the project's status.
func renderStatus(w io.Writer, st core.StatusResult) {
	row := func(keyStyle lipgloss.Style, key, val string) {
		fmt.Fprintln(w, keyStyle.Render(key)+strings.Repeat(" ", 9-len(key))+val)
	}
	// count renders a number and its unit, the number picked out like a state id.
	count := func(n int, one, many string) string {
		return th.ID.Render(fmt.Sprintf("%d", n)) + th.StatusKey.Render(" "+textfmt.Plural(n, one, many))
	}

	row(th.StatusKey, "project", st.Root)
	if st.WatcherRunning {
		row(th.StatusKey, "watcher", th.StatusOn.Render("running"))
	} else {
		row(th.StatusKey, "watcher", th.StatusKey.Render("not running"))
	}

	hist := count(st.StateCount, "snapshot", "snapshots")
	if st.Tips > 0 {
		hist += th.StatusKey.Render("  ·  ") + count(st.Tips, "timeline", "timelines")
	}
	row(th.StatusKey, "history", hist)
	row(th.StatusKey, "store", th.ID.Render(textfmt.HumanBytes(st.StoreBytes)))

	// The @ marker is colored like the current-state marker in `spor log`.
	if !st.HasHead {
		row(th.HeadTag, "@", th.StatusKey.Render("no snapshots yet"))
		return
	}
	val := th.ID.Render(textfmt.Abbrev(st.Head.ID))
	if st.Head.Label != "" {
		val += "  " + th.Label.Render(st.Head.Label)
	}
	val += "  " + th.Time.Render(textfmt.HumanizeSince(st.Head.CreatedAt))
	row(th.HeadTag, "@", val)

	// A second line places @ within the history: on the tip, or rewound with newer
	// states ahead that redo/go can move to.
	if st.Ahead > 0 {
		row(th.StatusKey, "", count(st.Ahead, "newer snapshot", "newer snapshots")+
			th.StatusKey.Render(" ahead, redo or go to move forward"))
	} else {
		row(th.StatusKey, "", th.StatusKey.Render("at the tip of its timeline"))
	}
}
