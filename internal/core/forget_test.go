package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/emprcl/spor/internal/lock"
)

// TestForgetRemovesStoreKeepsFiles checks that forget deletes the store but never
// touches working files.
func TestForgetRemovesStoreKeepsFiles(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "keep.txt", "important")
	snap(t, eng)

	stats, err := eng.ForgetStats(ctx)
	if err != nil {
		t.Fatalf("ForgetStats: %v", err)
	}
	if stats.StateCount != 1 {
		t.Errorf("StateCount = %d, want 1", stats.StateCount)
	}
	if stats.Bytes <= 0 {
		t.Errorf("Bytes = %d, want > 0", stats.Bytes)
	}

	if err := eng.Forget(ctx); err != nil {
		t.Fatalf("Forget: %v", err)
	}

	storeDir := filepath.Join(root, storageDir)
	if _, err := os.Stat(storeDir); !os.IsNotExist(err) {
		t.Errorf("store still present after forget: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "keep.txt")); err != nil {
		t.Errorf("working file was removed: %v", err)
	}
}

// TestForgetRefusesWithWatcher checks that forget refuses, and leaves the store
// intact, while a watcher holds the lock.
func TestForgetRefusesWithWatcher(t *testing.T) {
	eng, root := newTestEngine(t)
	write(t, root, "a.txt", "x")
	snap(t, eng)

	wl, err := eng.AcquireWatcher()
	if err != nil {
		t.Fatalf("AcquireWatcher: %v", err)
	}
	defer func() { _ = wl.Release() }()

	if err := eng.Forget(context.Background()); !errors.Is(err, lock.ErrWatcherRunning) {
		t.Fatalf("Forget with watcher = %v, want ErrWatcherRunning", err)
	}
	if _, err := os.Stat(filepath.Join(root, storageDir)); err != nil {
		t.Errorf("store should remain after a refused forget: %v", err)
	}
}

// TestForgetWaitsForWriteLock checks that forget serializes behind the write
// lock, so it cannot delete the store out from under an in-flight mutating
// operation from another front-end.
func TestForgetWaitsForWriteLock(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "a.txt", "x")
	snap(t, eng)

	wl, err := lock.AcquireWrite(ctx, filepath.Join(root, storageDir, "write.lock"))
	if err != nil {
		t.Fatalf("AcquireWrite: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- eng.Forget(ctx) }()

	select {
	case err := <-done:
		t.Fatalf("Forget completed while the write lock was held: %v", err)
	case <-time.After(200 * time.Millisecond):
		// Still blocked, as it should be.
	}

	if err := wl.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Forget after lock release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Forget still blocked after the write lock was released")
	}
	if _, err := os.Stat(filepath.Join(root, storageDir)); !os.IsNotExist(err) {
		t.Errorf("store still present after forget: err=%v", err)
	}
}

// TestForgetStatsHeadBehind checks the prompt data knows whether @ is the last
// saved snapshot: at the tip it is not behind; after rewinding, the snapshots
// saved after @ are counted.
func TestForgetStatsHeadBehind(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "a.txt", "one")
	snap(t, eng)
	write(t, root, "a.txt", "two")
	snap(t, eng)

	stats, err := eng.ForgetStats(ctx)
	if err != nil {
		t.Fatalf("ForgetStats: %v", err)
	}
	if stats.HeadBehind || stats.NewerStates != 0 {
		t.Fatalf("at the tip: HeadBehind=%v NewerStates=%d, want false/0", stats.HeadBehind, stats.NewerStates)
	}

	if _, err := eng.Undo(ctx, 1); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	stats, err = eng.ForgetStats(ctx)
	if err != nil {
		t.Fatalf("ForgetStats after undo: %v", err)
	}
	if !stats.HeadBehind || stats.NewerStates != 1 {
		t.Fatalf("after undo: HeadBehind=%v NewerStates=%d, want true/1", stats.HeadBehind, stats.NewerStates)
	}
}
