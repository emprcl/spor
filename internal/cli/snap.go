package cli

import (
	"fmt"
	"os"
	"sync"
	"time"

	"charm.land/bubbles/v2/progress"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
	"github.com/emprcl/spor/internal/textfmt"
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

			// Show a transient progress bar on stderr while a large snapshot
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
			out := styledOut(cmd)
			if !res.Created {
				fmt.Fprintln(out, th.Muted.Render("nothing to snap"))
				return nil
			}
			fmt.Fprintf(out, "snapshot %s\n", th.Accent.Render(res.StateID))
			return nil
		},
	}

	cmd.Flags().StringVarP(&label, "label", "l", "", "name for this snapshot")
	return cmd
}

// snapProgress renders a snapshot's progress as a single, self-overwriting
// line on a terminal: a bar (during the phases with a known total) and the
// phase's caption. Snap's OnProgress fires from worker goroutines, so updates
// are serialized under mu and throttled so the redraws stay cheap.
type snapProgress struct {
	w     *colorprofile.Writer
	bar   progress.Model
	start time.Time

	mu    sync.Mutex
	last  time.Time
	phase core.SnapPhase
	shown bool // a progress line has been drawn (so clear must erase it)
}

func newSnapProgress(f *os.File) *snapProgress {
	loadTheme()
	bar := progress.New(
		progress.WithColors(th.Muted.GetForeground(), th.Accent.GetForeground()),
		progress.WithoutPercentage(),
	)
	bar.SetWidth(30)
	// The colorprofile writer downsamples the bar's gradient to whatever the
	// terminal supports, the same way styledOut treats stdout.
	return &snapProgress{w: colorprofile.NewWriter(f, os.Environ()), bar: bar, start: time.Now()}
}

// update redraws the line, at most ~16 times a second, and never before the
// snapshot has run long enough to be worth reporting, so a quick snap finishes
// silently with no flash. A phase change always draws, so the sync and commit
// stages appear the moment they start instead of the bar sitting at 100%.
func (p *snapProgress) update(phase core.SnapPhase, done, total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if now.Sub(p.start) < 150*time.Millisecond {
		return // let a fast snapshot finish before drawing anything
	}
	samePhase := p.shown && phase == p.phase
	if samePhase && (total <= 0 || done != total) && now.Sub(p.last) < 60*time.Millisecond {
		return // throttle intermediate redraws
	}
	p.last = now
	p.phase = phase
	p.shown = true

	var line string
	switch phase {
	case core.SnapScan:
		line = textfmt.ScanningText(done)
	case core.SnapStore:
		line = p.bar.ViewAs(frac(done, total)) + "  " + textfmt.IndexingText(done, total)
	case core.SnapSync:
		line = p.bar.ViewAs(1) + "  " + textfmt.SyncingText()
	case core.SnapCommit:
		line = p.bar.ViewAs(frac(done, total)) + "  " + textfmt.SavingText(done, total)
	}
	// \r returns to column 0; \x1b[K clears the rest of the line.
	fmt.Fprintf(p.w, "\r\x1b[K%s", line)
}

// frac is done/total guarded against an empty phase.
func frac(done, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(done) / float64(total)
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
