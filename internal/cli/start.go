package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
	"github.com/emprcl/spor/internal/watch"
)

// newStartCmd builds `spor start`: the foreground watcher that snapshots the
// project automatically as it settles, with a live log of states appearing
// (docs/SPEC.md §4, §6). Ctrl+C stops it.
func newStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Watch the project and snapshot it automatically",
		Long: "Run in the foreground, recording a new state every time the project " +
			"settles after a change. A live log shows states as they appear. Press " +
			"Ctrl+C to stop watching.",
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

			// One watcher per project: fail fast if another `spor start` runs.
			wlock, err := eng.AcquireWatcher()
			if err != nil {
				return err
			}
			defer func() { _ = wlock.Release() }()

			// Ctrl+C cancels ctx, which unwinds the watcher's Run.
			ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
			defer stop()

			out := colorprofile.NewWriter(cmd.OutOrStdout(), os.Environ())
			root := eng.Root()
			fmt.Fprintln(out, styleWatchBanner.Render("watching ")+styleWatchPath.Render(root))
			fmt.Fprintln(out, styleWatchHint.Render("recording changes as they happen. press Ctrl+C to stop."))

			// Capture the current tree immediately so there is a baseline, then let
			// the watcher take over.
			if res, err := eng.Snapshot(ctx, core.SnapshotOptions{}); err != nil {
				return err
			} else if res.Created {
				logWatch(out, watch.Event{Kind: watch.Created, ID: res.StateID})
			}

			snap := func(ctx context.Context) (bool, string, error) {
				res, err := eng.Snapshot(ctx, core.SnapshotOptions{})
				return res.Created, res.StateID, err
			}
			w, err := watch.New(root, snap, func(ev watch.Event) { logWatch(out, ev) })
			if err != nil {
				return err
			}
			if err := w.Run(ctx); err != nil {
				return err
			}
			fmt.Fprintln(out, styleWatchHint.Render("stopped watching."))
			return nil
		},
	}
}

// Live-monitor styles, kept close to the log command's palette.
var (
	styleWatchBanner = lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Bold(true)
	styleWatchPath   = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	styleWatchHint   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleWatchDot    = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	styleWatchErr    = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
)

// logWatch renders one watcher event as a line in the live monitor. Only states
// appearing and errors are shown; settling and no-op ticks are kept silent so the
// log stays a clean record of what was recorded (docs/SPEC.md §6, live monitor).
func logWatch(w io.Writer, ev watch.Event) {
	ts := styleWatchHint.Render(time.Now().Format("15:04:05"))
	switch ev.Kind {
	case watch.Created:
		fmt.Fprintln(w, ts+"  "+styleWatchDot.Render("●")+"  "+
			styleWatchPath.Render(ev.ID)+"  "+styleWatchHint.Render("snapshot"))
	case watch.Error:
		fmt.Fprintln(w, ts+"  "+styleWatchErr.Render("✗")+"  "+ev.Err.Error())
	case watch.Settling, watch.NoChange:
		// Intentionally silent.
	}
}
