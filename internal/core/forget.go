package core

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/emprcl/spor/internal/lock"
)

// ForgetResult reports what forget would (or did) destroy: the store location,
// how many states it held, and its on-disk size. Used for the confirmation
// prompt (docs/SPEC.md §5, §6).
type ForgetResult struct {
	StoreDir   string
	StateCount int
	Bytes      int64
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
	return ForgetResult{StoreDir: e.storeDir, StateCount: len(states), Bytes: size}, nil
}

// Forget removes the entire store (docs/SPEC.md §5): every state, all history,
// and all blobs. Working files are never touched; only the .spor directory is
// removed. It refuses while a watcher is running, then closes the database and
// deletes the store. The engine must not be used afterwards (Close is idempotent,
// so a deferred Close is harmless).
func (e *Engine) Forget() error {
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

// dirSize sums the sizes of all files under root.
func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
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
