// Package tui is spor's interactive front-end (`spor ui`): a navigable history
// tree with an optional live watcher, driven by Bubble Tea
// (docs/design-spec.md §6). It is a thin presentation layer over internal/core,
// rendering through the shared internal/view theme and layout so its tree and
// diffs stay identical to `spor log` and `spor diff`.
package tui

import (
	"context"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/emprcl/spor/internal/core"
	"github.com/emprcl/spor/internal/view"
)

// Layout constants: the detail panel's fixed width and the smallest terminal that
// keeps it (narrower drops it so the tree spans the full width), then the cadences
// of the two periodic commands.
const (
	detailWidth   = 34
	minSplitWidth = 62
	// reloadInterval paces the background refresh tick. Each tick probes the
	// store's SQLite data_version (near-free) and reloads only when it moved
	// (an out-of-band mutation from another terminal); otherwise it just keeps
	// the relative-time column fresh (see maybeReload).
	reloadInterval = time.Second
	// pulseInterval paces the watch indicator's animation: the shimmer band
	// advances one cell per tick (see watchIndicator). The pulse tick runs only
	// while watching and stops behind the full-screen diff, where the indicator
	// is not drawn.
	pulseInterval = 120 * time.Millisecond
)

// Config wires the TUI to the engine and the shared theme.
type Config struct {
	Engine *core.Engine
	Root   string
	Theme  *view.Theme
	// ForceWatch and ForceBrowse skip the startup watch offer: ForceWatch starts
	// recording immediately, ForceBrowse opens straight into browse mode. At most
	// one is set (the ui command rejects both). When neither is set the model
	// offers the choice as usual.
	ForceWatch  bool
	ForceBrowse bool
}

// Run starts the interactive program. Watching is a mode inside it: the model
// offers it on startup when the watcher lock is free, and the w key toggles it.
// Quitting the program, or ctx being cancelled, stops any running watcher
// before Run returns, so the caller never closes the engine under it.
func Run(ctx context.Context, cfg Config, f *os.File) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	m := newModel(ctx, cfg)
	p := tea.NewProgram(
		m,
		tea.WithContext(ctx),
		tea.WithColorProfile(colorprofile.Detect(f, os.Environ())),
	)
	// The model starts and stops the watcher itself (the w toggle), forwarding
	// its events into the program through this send hook.
	m.send = p.Send

	rm, err := p.Run()
	// Stop any running watcher and wait for it to unwind before returning.
	cancel()
	m.watchWG.Wait()
	if m.wlock != nil {
		_ = m.wlock.Release()
	}
	if err != nil {
		return err
	}
	if tm, ok := rm.(*model); ok && tm.err != nil {
		return tm.err
	}
	return nil
}
