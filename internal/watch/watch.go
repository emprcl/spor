// Package watch turns filesystem activity into snapshots for `spor watch`. It is
// the serial pipeline from docs/design-spec.md §4:
//
//	fs events -> "dirty" signal -> debounce timer -> [ snapshot job ] -> single worker
//
// Debounce decides *when* the tree is consistent to snapshot; the single worker
// runs snapshots one at a time. A dirty flag closes the walk-to-idle race, and a
// max-debounce cap guarantees progress under a continuous writer. The filesystem
// walk (inside the snapshot) remains the source of truth, so lost or coalesced
// events only affect *when* a snapshot runs, never what it records.
package watch

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/emprcl/spor/internal/walk"
)

// Default timing for the debounce pipeline (docs/design-spec.md §4). Settle is the quiet
// window: instant-feeling but long enough to outlast an atomic-save burst or a
// project-wide save-all. MaxWait caps it so a continuous writer still snapshots.
const (
	DefaultSettle  = 300 * time.Millisecond
	DefaultMaxWait = 5 * time.Second
)

// EventKind classifies a live-monitor event emitted to the log callback.
type EventKind int

const (
	// Settling means a snapshot is about to run (the tree settled).
	Settling EventKind = iota
	// Created means a snapshot recorded a new state.
	Created
	// NoChange means a snapshot ran but nothing changed (no-op suppression).
	NoChange
	// Error means a snapshot or the watcher itself failed.
	Error
)

// Event is one thing worth showing in the live monitor.
type Event struct {
	Kind EventKind
	ID   string // set for Created
	Err  error  // set for Error
}

// SnapFunc performs one snapshot and reports whether a state was created and
// its id, for the live log. The watcher supplies the context so a stop cancels it.
type SnapFunc func(ctx context.Context) (created bool, id string, err error)

// Watcher watches a project tree and snapshots it when it settles.
type Watcher struct {
	root     string
	matcher  *walk.Matcher
	fsw      *fsnotify.Watcher
	settle   time.Duration
	maxWait  time.Duration
	snapshot SnapFunc
	log      func(Event)

	dirty chan struct{}   // coalesced change signal (capacity 1)
	done  chan snapResult // a snapshot finished
}

type snapResult struct {
	created bool
	id      string
	err     error
}

// Option customizes a Watcher.
type Option func(*Watcher)

// WithTiming overrides the debounce settle window and max-debounce cap. Mainly
// for tests, which want a fast settle.
func WithTiming(settle, maxWait time.Duration) Option {
	return func(w *Watcher) {
		w.settle = settle
		w.maxWait = maxWait
	}
}

// New creates a Watcher over root. snapshot is invoked on each settle; log
// receives live-monitor events (both may run on the watcher's goroutines).
func New(root string, snapshot SnapFunc, log func(Event), opts ...Option) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		root:     root,
		matcher:  walk.NewMatcher(root),
		fsw:      fsw,
		settle:   DefaultSettle,
		maxWait:  DefaultMaxWait,
		snapshot: snapshot,
		log:      log,
		dirty:    make(chan struct{}, 1),
		done:     make(chan snapResult, 1),
	}
	for _, opt := range opts {
		opt(w)
	}
	return w, nil
}

// Run places watches over the whole tree and drives the debounce pipeline until
// ctx is cancelled (Ctrl+C). It blocks. An in-flight snapshot is allowed to
// finish on shutdown so the write lock is released cleanly.
func (w *Watcher) Run(ctx context.Context) error {
	defer w.fsw.Close()
	if err := w.addTree(w.root); err != nil {
		return err
	}
	go w.readEvents(ctx)
	w.loop(ctx)
	return nil
}

// readEvents turns raw fsnotify events into dirty signals and grows the watch set
// as new directories appear. It exits when ctx is cancelled or the fsnotify
// channels close.
func (w *Watcher) readEvents(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			w.handleEvent(ev)
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			w.log(Event{Kind: Error, Err: err})
			// A watcher error (typically a kernel event-queue overflow) means
			// events were dropped, possibly including the last one before the
			// tree went idle. Mark dirty so a reconciling walk runs; if nothing
			// actually changed, no-op suppression makes it free.
			w.markDirty()
		}
	}
}

// handleEvent filters one event against the ignore rules and, for a new
// directory, extends the watch set (inotify is per-directory, not recursive).
// Anything surviving the filter marks the tree dirty.
func (w *Watcher) handleEvent(ev fsnotify.Event) {
	rel, ok := w.rel(ev.Name)
	if !ok || w.isStorage(rel) {
		return // outside the tree, or spor's own store: never a trigger
	}
	if ev.Has(fsnotify.Create) {
		if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() && !w.matcher.Ignored(rel, true) {
			_ = w.addTree(ev.Name) // best-effort; the walk still captures its contents
		}
	}
	if w.matcher.Ignored(rel, false) {
		return // an ignored file (e.g. *.tmp, editor swap) churned; don't snapshot
	}
	w.markDirty()
}

// markDirty signals a change, coalescing: if a signal is already pending the send
// is dropped, so a burst of events collapses to one (the capacity-1 slot from
// docs/design-spec.md §4).
func (w *Watcher) markDirty() {
	select {
	case w.dirty <- struct{}{}:
	default:
	}
}

// loop is the debouncer and worker driver. It owns all timing state on one
// goroutine, so no locks are needed. quiet is nil unless a settle timer is armed
// (a nil channel blocks forever in select, disabling that case).
func (w *Watcher) loop(ctx context.Context) {
	var (
		quiet   <-chan time.Time // fires w.settle after the last event
		burstAt time.Time        // when the current debounce burst began
		running bool             // a snapshot worker is active
		pending bool             // an event landed while the worker ran
	)
	for {
		select {
		case <-ctx.Done():
			if running {
				<-w.done // let the in-flight snapshot finish and release the lock
			}
			return

		case <-w.dirty:
			if running {
				pending = true // walk-to-idle race: re-run after this snapshot
				continue
			}
			if quiet == nil {
				burstAt = time.Now()
			}
			// Max-debounce cap: a continuous writer keeps resetting the quiet
			// timer, so once the burst has run long enough, snapshot now instead
			// of extending again.
			if time.Since(burstAt) >= w.maxWait {
				quiet = nil
				running = true
				w.begin(ctx)
			} else {
				quiet = time.After(w.settle)
			}

		case <-quiet:
			quiet = nil
			running = true
			w.begin(ctx)

		case res := <-w.done:
			running = false
			w.report(res)
			if pending {
				pending = false
				burstAt = time.Now()
				quiet = time.After(w.settle)
			}
		}
	}
}

// begin launches one snapshot on its own goroutine so the loop keeps servicing
// dirty signals (setting the pending flag) while it runs.
func (w *Watcher) begin(ctx context.Context) {
	w.log(Event{Kind: Settling})
	go func() {
		created, id, err := w.snapshot(ctx)
		w.done <- snapResult{created: created, id: id, err: err}
	}()
}

// report turns a finished snapshot into a live-monitor event.
func (w *Watcher) report(r snapResult) {
	switch {
	case r.err != nil:
		w.log(Event{Kind: Error, Err: r.err})
	case r.created:
		w.log(Event{Kind: Created, ID: r.id})
	default:
		w.log(Event{Kind: NoChange})
	}
}

// addTree walks dir and adds an inotify watch on every non-ignored directory
// within it, skipping .spor and ignored subtrees. It is used both for the initial
// tree and for directories that appear at runtime. A directory that vanishes
// mid-walk (atomic operations) is skipped rather than failing.
func (w *Watcher) addTree(dir string) error {
	return filepath.WalkDir(dir, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.IsDir() {
			return nil
		}
		rel, ok := w.rel(abs)
		if !ok {
			return nil
		}
		if rel != "." {
			if d.Name() == walk.StorageDir {
				return filepath.SkipDir
			}
			if w.matcher.Ignored(rel, true) {
				return filepath.SkipDir
			}
		}
		if err := w.fsw.Add(abs); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		return nil
	})
}

// rel converts an absolute event path to a slash-separated, root-relative path,
// reporting false if it escapes the root.
func (w *Watcher) rel(abs string) (string, bool) {
	r, err := filepath.Rel(w.root, abs)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(r), true
}

// isStorage reports whether rel is spor's own store, which is never watched or
// treated as a change (walk.StorageDir, mirroring the walk's own exclusion).
func (w *Watcher) isStorage(rel string) bool {
	return rel == walk.StorageDir || strings.HasPrefix(rel, walk.StorageDir+"/")
}
