package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
	"github.com/emprcl/spor/internal/watch"
)

// newWatchCmd builds `spor watch`: the foreground watcher that snapshots the
// project automatically as it settles (docs/SPEC.md §4, §6). On a terminal it
// shows the history tree repainting live as states appear, the same view as
// `spor log`; piped, it falls back to a plain line log. Ctrl+C stops it.
func newWatchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "watch",
		Short: "Watch the project and snapshot it automatically",
		Long: "Run in the foreground, recording a new state every time the project " +
			"settles after a change. The history tree is shown live, repainting as " +
			"states appear. Press Ctrl+C to stop watching.",
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

			// One watcher per project: fail fast if another `spor watch` runs.
			wlock, err := eng.AcquireWatcher()
			if err != nil {
				return err
			}
			defer func() { _ = wlock.Release() }()

			// Ctrl+C cancels ctx, which unwinds the watcher's Run. The terminal stays
			// in cooked mode so the interrupt is delivered as SIGINT.
			ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
			defer stop()

			root := eng.Root()

			// Live tree view on a real terminal; plain line streaming when piped, so
			// no cursor-control escapes ever leak into a redirected stream.
			if f, ok := cmd.OutOrStdout().(*os.File); ok && term.IsTerminal(f.Fd()) {
				return runWatchLive(ctx, eng, root, f)
			}
			return runWatchStream(ctx, eng, root, cmd.OutOrStdout())
		},
	}
}

// runWatchLive drives the watcher with a full-screen live view of the history
// tree. It takes over the alternate screen, repaints the tree on every new
// state, restores the terminal on exit, and then leaves a final static tree in
// the scrollback so the session's result persists (docs/SPEC.md §6).
func runWatchLive(ctx context.Context, eng *core.Engine, root string, f *os.File) error {
	v := &liveView{eng: eng, root: root, f: f, profile: colorprofile.Detect(f, os.Environ())}
	v.enter()
	defer v.leave() // idempotent: always restores the terminal, even on error

	// Capture the current tree immediately so there is a baseline, then paint.
	if _, err := eng.Snap(ctx, core.SnapOptions{}); err != nil {
		return err
	}
	v.repaint(ctx)

	snap := func(ctx context.Context) (bool, string, error) {
		res, err := eng.Snap(ctx, core.SnapOptions{})
		return res.Created, res.StateID, err
	}
	w, err := watch.New(root, snap, func(ev watch.Event) { v.onEvent(ctx, ev) })
	if err != nil {
		return err
	}

	// Out-of-band changes (dropfrom, keepfrom, go, undo/redo run from another
	// terminal) mutate the state graph without producing filesystem events, so the
	// watcher pipeline never fires for them. A low-frequency poll repaints when the
	// tree has changed; the frame dedup in repaintLocked makes an unchanged poll a
	// no-op, and it keeps relative timestamps fresh too.
	pollCtx, stopPoll := context.WithCancel(ctx)
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-t.C:
				v.repaint(pollCtx)
			}
		}
	}()

	runErr := w.Run(ctx)
	stopPoll()
	<-pollDone
	if runErr != nil {
		return runErr
	}

	// Leave the alternate screen, then reprint the final tree to the main buffer
	// so it stays visible after the watcher stops. ctx is already cancelled here
	// (Ctrl+C is what unwound Run), so read with a fresh context.
	v.leave()
	out := colorprofile.NewWriter(f, os.Environ())
	res, err := eng.Log(context.Background())
	if err != nil {
		return err
	}
	renderLog(out, res)
	fmt.Fprintln(out, styleWatchHint.Render("stopped watching."))
	return nil
}

// liveView owns the alternate-screen live monitor. Its repaint is guarded by a
// mutex because the watcher delivers events from more than one goroutine.
type liveView struct {
	eng     *core.Engine
	root    string
	f       *os.File
	profile colorprofile.Profile

	mu        sync.Mutex
	left      bool   // the alternate screen has been restored
	status    string // transient activity note (e.g. "settling...")
	errMsg    string // last error, shown until the next successful snapshot
	lastFrame string // last frame written, to skip redundant repaints
}

// enter switches to the alternate screen and hides the cursor.
func (v *liveView) enter() {
	fmt.Fprint(v.f, "\x1b[?1049h\x1b[?25l")
}

// leave restores the cursor and the main screen. It is safe to call twice.
func (v *liveView) leave() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.left {
		return
	}
	v.left = true
	fmt.Fprint(v.f, "\x1b[?25h\x1b[?1049l")
}

// onEvent updates the status line for one watcher event and repaints. Only a new
// state changes the tree; settling and errors update the status/footer.
func (v *liveView) onEvent(ctx context.Context, ev watch.Event) {
	v.mu.Lock()
	defer v.mu.Unlock()
	switch ev.Kind {
	case watch.Settling:
		v.status = "settling..."
	case watch.Created:
		v.status, v.errMsg = "", ""
	case watch.NoChange:
		v.status = ""
	case watch.Error:
		if ev.Err != nil {
			v.errMsg = ev.Err.Error()
		}
	}
	v.repaintLocked(ctx)
}

// repaint redraws the view under the lock.
func (v *liveView) repaint(ctx context.Context) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.repaintLocked(ctx)
}

// repaintLocked composes and writes one frame: a header, the history tree
// (tail-anchored to what fits the window so the newest states stay visible), and
// a status/error footer. The caller must hold v.mu.
func (v *liveView) repaintLocked(ctx context.Context) {
	if v.left {
		return
	}
	res, err := v.eng.Log(ctx)
	if err != nil {
		// A transient read failure, or a cancelled context during shutdown, must
		// not blank the view; keep the last frame.
		return
	}

	// Render the tree with color reconciled to the terminal (the buffer is not a
	// terminal, so target the profile detected from f instead).
	var treeBuf bytes.Buffer
	renderLog(&colorprofile.Writer{Forward: &treeBuf, Profile: v.profile}, res)
	tree := strings.Split(strings.TrimRight(treeBuf.String(), "\n"), "\n")

	_, height, err := term.GetSize(v.f.Fd())
	if err != nil || height <= 0 {
		height = len(tree) + 5
	}

	header := v.style(styleWatchBanner.Render("watching ") + styleWatchPath.Render(v.root))
	hint := v.style(styleWatchHint.Render("recording changes as they happen. press Ctrl+C to stop."))
	footer := v.footer()

	// Reserve rows for the header, the hint, one blank line, and the footer (if
	// any); the rest is the tree body.
	avail := height - 3
	if footer != "" {
		avail--
	}
	if avail < 1 {
		avail = 1
	}
	if len(tree) > avail {
		tree = tree[len(tree)-avail:]
	}

	var frame bytes.Buffer
	frame.WriteString("\x1b[H\x1b[J") // cursor home, clear to end of screen
	frame.WriteString(header + "\n")
	frame.WriteString(hint + "\n\n")
	for _, ln := range tree {
		frame.WriteString(ln + "\n")
	}
	if footer != "" {
		frame.WriteString(footer)
	}
	s := frame.String()
	if s == v.lastFrame {
		return // nothing changed; skip the redundant repaint (and its flicker)
	}
	v.lastFrame = s
	_, _ = io.WriteString(v.f, s)
}

// footer returns the status/error line, downsampled to the terminal profile, or
// empty when there is nothing to show.
func (v *liveView) footer() string {
	switch {
	case v.errMsg != "":
		return v.style(styleWatchErr.Render("✗ " + v.errMsg))
	case v.status != "":
		return v.style(styleWatchHint.Render(v.status))
	default:
		return ""
	}
}

// style downsamples an already-styled string to the terminal's color profile.
func (v *liveView) style(s string) string {
	var b bytes.Buffer
	_, _ = io.WriteString(&colorprofile.Writer{Forward: &b, Profile: v.profile}, s)
	return b.String()
}

// runWatchStream is the non-terminal fallback: a plain banner plus one line per
// recorded state, safe to redirect into a file or pipe.
func runWatchStream(ctx context.Context, eng *core.Engine, root string, w io.Writer) error {
	out := colorprofile.NewWriter(w, os.Environ())
	fmt.Fprintln(out, styleWatchBanner.Render("watching ")+styleWatchPath.Render(root))
	fmt.Fprintln(out, styleWatchHint.Render("recording changes as they happen. press Ctrl+C to stop."))

	// Capture the current tree immediately so there is a baseline.
	if res, err := eng.Snap(ctx, core.SnapOptions{}); err != nil {
		return err
	} else if res.Created {
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
	fmt.Fprintln(out, styleWatchHint.Render("stopped watching."))
	return nil
}

// logWatch renders one watcher event as a line in the streaming fallback monitor.
// Only states appearing and errors are shown; settling and no-op ticks are kept
// silent so the log stays a clean record of what was recorded (docs/SPEC.md §6).
func logWatch(w io.Writer, ev watch.Event) {
	ts := styleWatchHint.Render(time.Now().Format("15:04:05"))
	switch ev.Kind {
	case watch.Created:
		fmt.Fprintln(w, ts+"  "+styleWatchDot.Render("●")+"  "+
			styleWatchPath.Render(ev.ID)+"  "+styleWatchHint.Render("snap"))
	case watch.Error:
		fmt.Fprintln(w, ts+"  "+styleWatchErr.Render("✗")+"  "+ev.Err.Error())
	case watch.Settling, watch.NoChange:
		// Intentionally silent.
	}
}
