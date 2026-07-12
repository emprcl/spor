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
