package core

import (
	"context"
	"testing"
)

// TestStatusReportsHeadAndWatcher checks that status reflects HEAD and toggles
// with the watcher lock.
func TestStatusReportsHeadAndWatcher(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "a.txt", "hi")
	head := snap(t, eng)

	st, err := eng.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Root != root {
		t.Errorf("Root = %q, want %q", st.Root, root)
	}
	if st.WatcherRunning {
		t.Error("WatcherRunning = true with no watcher")
	}
	if !st.HasHead || st.Head.ID != head {
		t.Errorf("Head = %+v (has=%v), want %s", st.Head, st.HasHead, head)
	}
	if st.StateCount != 1 {
		t.Errorf("StateCount = %d, want 1", st.StateCount)
	}

	// Holding the watcher lock flips WatcherRunning.
	wl, err := eng.AcquireWatcher()
	if err != nil {
		t.Fatalf("AcquireWatcher: %v", err)
	}
	if st, err := eng.Status(ctx); err != nil || !st.WatcherRunning {
		t.Fatalf("Status with watcher held: running=%v err=%v, want running", st.WatcherRunning, err)
	}
	if err := wl.Release(); err != nil {
		t.Fatal(err)
	}
	if st, err := eng.Status(ctx); err != nil || st.WatcherRunning {
		t.Fatalf("Status after release: running=%v err=%v, want not running", st.WatcherRunning, err)
	}
}
