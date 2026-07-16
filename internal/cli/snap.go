package cli

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
)

// newSnapCmd builds `spor snap`, the manual, watcher-free way to record
// a state (docs/design-spec.md §4, §6).
func newSnapCmd() *cobra.Command {
	var label string

	cmd := &cobra.Command{
		Use:   "snap",
		Short: "Save one snapshot by hand",
		Long: "Record the current contents of your project as a new snapshot you can jump " +
			"back to later. If nothing has changed since the last one, nothing is " +
			"recorded. This is the manual path: you only need it when 'spor watch' " +
			"isn't running, since the watcher records everything automatically.\n\n" +
			"Files matched by .sporignore, or by spor's built-in defaults (build " +
			"artifacts, editor temp files, .git), are never recorded.",
		Example: `  # Record the current state
  spor snap

  # Record it with a name you can jump back to
  spor snap -l "before refactor"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}

			ctx := cmd.Context()
			eng, err := core.OpenOrInit(ctx, root)
			if err != nil {
				return err
			}
			defer eng.Close()

			// Show a transient indexing counter on stderr while a large snapshot
			// runs, so a slow first snap isn't a silent wait. It stays on stderr
			// and only when stderr is a terminal, so `id=$(spor snap)` and pipes
			// see just the snapshot id on stdout.
			opts := core.SnapOptions{Label: label}
			var prog *snapProgress
			if f, ok := cmd.ErrOrStderr().(*os.File); ok && term.IsTerminal(f.Fd()) {
				prog = newSnapProgress(f)
				opts.OnProgress = prog.update
			}

			res, err := eng.Snap(ctx, opts)
			if prog != nil {
				prog.clear()
			}
			if err != nil {
				return err
			}
			if !res.Created {
				fmt.Fprintln(cmd.OutOrStdout(), "nothing to snap")
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "snapshot %s\n", res.StateID)
			return nil
		},
	}

	cmd.Flags().StringVarP(&label, "label", "l", "", "name for this snapshot")
	return cmd
}

// snapProgress renders a snapshot's file-indexing progress as a single,
// self-overwriting line on a terminal. Snap's OnProgress fires from worker
// goroutines, so updates are serialized under mu and throttled so the redraws
// stay cheap.
type snapProgress struct {
	w     io.Writer
	start time.Time

	mu    sync.Mutex
	last  time.Time
	shown bool // a progress line has been drawn (so clear must erase it)
}

func newSnapProgress(w io.Writer) *snapProgress {
	return &snapProgress{w: w, start: time.Now()}
}

// update redraws the counter, at most ~16 times a second, and never before the
// snapshot has run long enough to be worth reporting, so a quick snap finishes
// silently with no flash.
func (p *snapProgress) update(done, total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if now.Sub(p.start) < 150*time.Millisecond {
		return // let a fast snapshot finish before drawing anything
	}
	if p.shown && done < total && now.Sub(p.last) < 60*time.Millisecond {
		return // throttle intermediate redraws
	}
	p.last = now
	p.shown = true
	// \r returns to column 0; \x1b[K clears the rest of the line.
	fmt.Fprintf(p.w, "\r\x1b[K%s", indexingText(done, total))
}

// clear erases the progress line if one was drawn, so the snapshot id prints on
// a clean line.
func (p *snapProgress) clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.shown {
		fmt.Fprint(p.w, "\r\x1b[K")
	}
}
