// Package lock provides spor's advisory file locks (docs/SPEC.md §8). Locks use
// flock(2) via gofrs/flock, so the kernel releases them on process exit -
// including a crash, leaving no stale locks to clean up.
package lock

import (
	"context"
	"errors"
	"time"

	"github.com/gofrs/flock"
)

// ErrBusy is returned when the write lock cannot be acquired within the timeout,
// meaning another spor operation holds it.
var ErrBusy = errors.New("another spor operation is in progress")

// writeTimeout bounds how long a mutating operation waits for the write lock -
// long enough to outlast an in-progress snapshot, short enough to fail loudly if
// something is stuck.
const writeTimeout = 10 * time.Second

// Write is the per-operation exclusive lock serializing all mutating operations
// across front-ends (docs/SPEC.md §8). Reads never take it.
type Write struct {
	fl *flock.Flock
}

// AcquireWrite blocks (up to writeTimeout) for the exclusive write lock at path.
// Call Release when the operation completes.
func AcquireWrite(ctx context.Context, path string) (*Write, error) {
	fl := flock.New(path)
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()

	locked, err := fl.TryLockContext(ctx, 50*time.Millisecond)
	if err != nil {
		return nil, ErrBusy
	}
	if !locked {
		return nil, ErrBusy
	}
	return &Write{fl: fl}, nil
}

// Release unlocks the write lock.
func (w *Write) Release() error {
	return w.fl.Unlock()
}

// ErrWatcherRunning is returned when the watcher lock is already held, meaning a
// `spor watch` is already watching this project.
var ErrWatcherRunning = errors.New("a watcher is already running for this project")

// Watcher is the lifetime lock held by `spor watch`, so a project has at most one
// watcher (docs/SPEC.md §8). It is acquired non-blocking: a second `spor watch`
// fails immediately rather than queuing.
type Watcher struct {
	fl *flock.Flock
}

// AcquireWatcher takes the watcher lock at path without blocking, returning
// ErrWatcherRunning if another watcher already holds it. Hold it for the
// watcher's lifetime and Release on stop; the kernel also drops it on exit.
func AcquireWatcher(path string) (*Watcher, error) {
	fl := flock.New(path)
	locked, err := fl.TryLock()
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, ErrWatcherRunning
	}
	return &Watcher{fl: fl}, nil
}

// Release unlocks the watcher lock.
func (w *Watcher) Release() error {
	return w.fl.Unlock()
}

// WatcherHeld reports whether the watcher lock at path is currently held (by a
// running `spor watch`). It probes with a non-blocking TryLock and releases
// immediately on success, so it never keeps the lock itself. flock associates
// locks with the open file description, so this is exclusive even against a
// watcher in the same process.
func WatcherHeld(path string) (bool, error) {
	fl := flock.New(path)
	locked, err := fl.TryLock()
	if err != nil {
		return false, err
	}
	if locked {
		_ = fl.Unlock()
		return false, nil
	}
	return true, nil
}
