package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
	"github.com/emprcl/spor/internal/textfmt"
	"github.com/emprcl/spor/internal/watch"
)

// newWatchCmd builds `spor watch`: the foreground recorder that snapshots the
// project automatically as it settles (docs/design-spec.md §4, §6). It streams one
// line per recorded snapshot, safe to leave running in a spare terminal or to
// redirect into a file; the interactive view lives in `spor ui`, which can also
// watch. Ctrl+C stops it.
func newWatchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "watch",
		Short: "Watch the project and snapshot it automatically",
		Long: "Run in the foreground and record a new snapshot every time the project " +
			"settles after a change, so you never have to snapshot by hand. It prints " +
			"one line per recorded snapshot, so it can run in a spare terminal or be " +
			"redirected into a file. Press Ctrl+C to stop (recording stops with it).\n\n" +
			"For the interactive view, use 'spor ui', which can also watch.\n\n" +
			"Files matched by .sporignore, or by spor's built-in defaults (build " +
			"artifacts, editor temp files, .git), are never recorded.",
		Example: `  # Watch and snapshot automatically until you press Ctrl+C
  spor watch

  # Keep a log of what was recorded
  spor watch >> snapshots.log`,
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

			// One watcher per project; a second `spor watch` (or a watching
			// `spor ui`) fails immediately.
			wlock, err := eng.AcquireWatcher()
			if err != nil {
				return err
			}
			defer func() { _ = wlock.Release() }()

			// On a terminal, the first snapshot shows the same progress bar as
			// `spor snap`; redirected output gets a one-line announcement instead.
			var prog *snapProgress
			if f, ok := cmd.OutOrStdout().(*os.File); ok && term.IsTerminal(f.Fd()) {
				prog = newSnapProgress(f)
			}

			// Ctrl+C cancels ctx, which unwinds the streaming watcher. The terminal
			// stays in cooked mode so the interrupt is delivered as SIGINT.
			ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
			defer stop()
			return runWatchStream(ctx, eng, eng.Root(), cmd.OutOrStdout(), prog)
		},
	}
}

// runWatchStream drives the recording loop: a banner plus one line per recorded
// state, colored on a terminal and plain when redirected. On a terminal (prog
// non-nil) the initial snapshot draws the shared progress bar; otherwise it
// announces itself with a single line so a redirected log stays clean.
func runWatchStream(ctx context.Context, eng *core.Engine, root string, w io.Writer, prog *snapProgress) error {
	out := colorprofile.NewWriter(w, os.Environ())
	fmt.Fprintln(out, th.WatchBanner.Render("watching ")+th.WatchPath.Render(root))
	hint := func() {
		fmt.Fprintln(out, th.WatchHint.Render("recording changes as they happen. press Ctrl+C to stop."))
	}

	// Capture the current tree immediately so there is a baseline, reporting
	// progress so a slow first index over a big project isn't a silent wait.
	opts := core.SnapOptions{}
	if prog != nil {
		opts.OnProgress = prog.update
	} else {
		// No terminal: the hint leads the log, and the first snapshot announces
		// itself with a single line.
		hint()
		var announce sync.Once
		opts.OnProgress = func(phase core.SnapPhase, _, total int) {
			if phase != core.SnapStore {
				return
			}
			announce.Do(func() {
				fmt.Fprintln(out, th.WatchHint.Render(fmt.Sprintf("indexing %s files...", textfmt.HumanCount(total))))
			})
		}
	}
	res, err := eng.Snap(ctx, opts)
	if prog != nil {
		prog.clear()
	}
	if err != nil {
		return err
	}
	if prog != nil {
		// On a terminal the hint waits for the first snapshot: the progress bar
		// owns the line until then, and the hint prints once it has cleared.
		hint()
	}
	if res.Created {
		logWatch(out, watch.Event{Kind: watch.Created, ID: res.StateID})
	}

	snap := func(ctx context.Context) (bool, string, error) {
		res, err := eng.Snap(ctx, core.SnapOptions{})
		return res.Created, res.StateID, err
	}
	w2, err := watch.New(root, snap, func(ev watch.Event) { logWatch(out, ev) })
	if err != nil {
		return err
	}
	if err := w2.Run(ctx); err != nil {
		return err
	}
	fmt.Fprintln(out, th.WatchHint.Render("stopped watching."))
	return nil
}

// logWatch renders one watcher event as a line in the stream. Only states
// appearing and errors are shown; settling and no-op ticks are kept silent so
// the log stays a clean record of what was recorded (docs/design-spec.md §6).
func logWatch(w io.Writer, ev watch.Event) {
	ts := th.WatchHint.Render(time.Now().Format("15:04:05"))
	switch ev.Kind {
	case watch.Created:
		// The recorded id is the event's result, so it gets accent emphasis, the
		// same as a one-shot command's "snapshot <id>" line.
		fmt.Fprintln(w, ts+"  "+th.WatchDot.Render("●")+"  "+
			th.Accent.Render(ev.ID)+"  "+th.WatchHint.Render("snapshot"))
	case watch.Error:
		fmt.Fprintln(w, ts+"  "+th.WatchErr.Render("✗")+"  "+ev.Err.Error())
	case watch.Settling, watch.NoChange:
		// Intentionally silent.
	}
}
