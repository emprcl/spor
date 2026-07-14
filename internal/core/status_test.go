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
	if st.Tips != 1 || st.Ahead != 0 {
		t.Errorf("Tips=%d Ahead=%d, want 1 and 0 for a single state", st.Tips, st.Ahead)
	}
	if st.StoreBytes <= 0 {
		t.Errorf("StoreBytes = %d, want > 0", st.StoreBytes)
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

// TestStatusTipsAndAhead checks the timeline count and the "newer states ahead"
// position as HEAD is rewound and a second timeline is started.
func TestStatusTipsAndAhead(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "f.txt", "A")
	snap(t, eng)
	write(t, root, "f.txt", "B")
	snap(t, eng)
	write(t, root, "f.txt", "C")
	snap(t, eng) // s1 -> s2 -> s3, HEAD = s3 (a tip)

	if st, _ := eng.Status(ctx); st.Tips != 1 || st.Ahead != 0 {
		t.Fatalf("on tip: Tips=%d Ahead=%d, want 1 and 0", st.Tips, st.Ahead)
	}

	// Rewind two states: two newer states are now ahead of @.
	if _, err := eng.Go(ctx, "@~2"); err != nil {
		t.Fatalf("Go: %v", err)
	}
	if st, _ := eng.Status(ctx); st.Tips != 1 || st.Ahead != 2 {
		t.Fatalf("rewound: Tips=%d Ahead=%d, want 1 and 2", st.Tips, st.Ahead)
	}

	// Editing here forks a second timeline; @ is its leaf, so nothing is ahead.
	write(t, root, "f.txt", "D")
	snap(t, eng)
	if st, _ := eng.Status(ctx); st.Tips != 2 || st.Ahead != 0 {
		t.Fatalf("branched: Tips=%d Ahead=%d, want 2 and 0", st.Tips, st.Ahead)
	}
}
