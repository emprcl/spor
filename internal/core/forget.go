package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/emprcl/spor/internal/lock"
)

// ForgetResult reports what forget would (or did) destroy: the store location,
// how many states it held, and its on-disk size. Used for the confirmation
// prompt (docs/design-spec.md §5, §6).
type ForgetResult struct {
	StoreDir   string
	StateCount int
	Bytes      int64
	// HeadBehind reports that @ is not the most recently saved snapshot: the
	// working files match an older point in history, and the NewerStates
	// snapshots saved after it go down with the store too. Best-effort (false
	// on a store too damaged to read HEAD), so the prompt still works under
	// OpenForRepair.
	HeadBehind  bool
	NewerStates int
}

// ForgetStats measures the store without changing anything, so a caller can
// confirm before calling Forget.
func (e *Engine) ForgetStats(ctx context.Context) (ForgetResult, error) {
	states, err := e.q.ListStates(ctx)
	if err != nil {
		return ForgetResult{}, fmt.Errorf("listing states: %w", err)
	}
	size, err := dirSize(e.storeDir)
	if err != nil {
		return ForgetResult{}, err
	}
	res := ForgetResult{StoreDir: e.storeDir, StateCount: len(states), Bytes: size}

	// Is @ the last saved snapshot? Creation order breaks ties by id, the same
	// (time, id) ordering ULIDs sort by.
	if head, err := e.q.GetHead(ctx); err == nil && head.Valid {
		var headAt int64
		found := false
		for _, s := range states {
			if s.ID == head.String {
				headAt, found = s.CreatedAt, true
				break
			}
		}
		if found {
			for _, s := range states {
				if s.CreatedAt > headAt || (s.CreatedAt == headAt && s.ID > head.String) {
					res.NewerStates++
				}
			}
			res.HeadBehind = res.NewerStates > 0
		}
	}
	return res, nil
}

// Forget removes the entire store (docs/design-spec.md §5): every state, all history,
// and all blobs. Working files are never touched; only the .spor directory is
// removed. It refuses while a watcher is running, then closes the database and
// deletes the store, all under the write lock. The engine must not be used
// afterwards (Close is idempotent, so a deferred Close is harmless).
func (e *Engine) Forget(ctx context.Context) error {
	// The write lock serializes forget against in-flight mutating operations from
	// other front-ends (a snap or go mid-commit holds only this lock, not the
	// watcher lock), so the store is never deleted under a running write. Held
	// through the RemoveAll: flock follows the open descriptor, so releasing
	// after the file is gone is safe.
	wl, err := lock.AcquireWrite(ctx, e.writeLockPath())
	if err != nil {
		return err
	}
	defer func() { _ = wl.Release() }()

	running, err := e.WatcherRunning()
	if err != nil {
		return err
	}
	if running {
		return lock.ErrWatcherRunning
	}
	if err := e.Close(); err != nil {
		return fmt.Errorf("closing store: %w", err)
	}
	if err := os.RemoveAll(e.storeDir); err != nil {
		return fmt.Errorf("removing %s: %w", e.storeDir, err)
	}
	return nil
}

// dirSize sums the sizes of all files under root. A file that vanishes mid-walk
// is skipped, not an error: readers measure the live store while a concurrent
// snap creates and renames temp blobs.
func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("measuring %s: %w", root, err)
	}
	return total, nil
}
