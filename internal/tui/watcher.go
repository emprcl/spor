package tui

import (
	"context"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/emprcl/spor/internal/core"
	"github.com/emprcl/spor/internal/watch"
)

// runWatcher records the initial snapshot, then runs the watcher, forwarding
// each event into the program through send. It stops when ctx is cancelled
// (the w toggle, program quit, or Ctrl+C).
func runWatcher(ctx context.Context, eng *core.Engine, root string, send func(tea.Msg)) {
	// Report the initial snapshot's indexing progress so a slow index over a
	// big or fresh project isn't a silent wait behind the tree. A quick snap
	// finishes before the gate opens and draws nothing.
	fwd := &indexForwarder{send: send, start: time.Now()}
	if _, err := eng.Snap(ctx, core.SnapOptions{OnProgress: fwd.update}); err != nil {
		if ctx.Err() != nil {
			return
		}
		send(fatalMsg{err: err})
		return
	}
	fwd.done()
	send(reloadMsg{})

	snap := func(ctx context.Context) (bool, string, error) {
		res, err := eng.Snap(ctx, core.SnapOptions{})
		return res.Created, res.StateID, err
	}
	w, err := watch.New(root, snap, func(ev watch.Event) { send(watchEventMsg(ev)) })
	if err != nil {
		send(fatalMsg{err: err})
		return
	}
	if err := w.Run(ctx); err != nil && ctx.Err() == nil {
		send(fatalMsg{err: err})
	}
}

// indexForwarder turns a snapshot's OnProgress callbacks, which fire from worker
// goroutines, into throttled program messages. It mirrors the CLI's snap line: the
// gate lets a fast snapshot finish silently, and updates are throttled so the bar
// redraws cheaply; a phase change always goes through, so the sync and commit
// stages appear the moment they start. done() is a no-op when nothing was ever
// shown, so a quick first snap leaves no bar to clear.
type indexForwarder struct {
	send  func(tea.Msg)
	start time.Time

	mu    sync.Mutex
	last  time.Time
	phase core.SnapPhase
	shown bool
}

func (f *indexForwarder) update(phase core.SnapPhase, done, total int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	if now.Sub(f.start) < 150*time.Millisecond {
		return // let a fast snapshot finish before drawing anything
	}
	samePhase := f.shown && phase == f.phase
	if samePhase && (total <= 0 || done != total) && now.Sub(f.last) < 60*time.Millisecond {
		return // throttle intermediate redraws
	}
	f.last = now
	f.phase = phase
	f.shown = true
	f.send(indexProgressMsg{phase: phase, done: done, total: total})
}

func (f *indexForwarder) done() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.shown {
		f.send(indexDoneMsg{})
	}
}
