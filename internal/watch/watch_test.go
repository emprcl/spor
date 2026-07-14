package watch_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emprcl/spor/internal/core"
	"github.com/emprcl/spor/internal/watch"
)

// newWatched starts a watcher over a fresh project with a fast settle and returns
// the project root, a counter of states created, and a stop func. The engine's
// initial tree is not snapshotted here, so counts reflect only post-start changes.
func newWatched(t *testing.T) (root string, created *int32, stop func()) {
	t.Helper()
	root = t.TempDir()
	eng, err := core.OpenOrInit(context.Background(), root)
	if err != nil {
		t.Fatalf("OpenOrInit: %v", err)
	}

	created = new(int32)
	snap := func(ctx context.Context) (bool, string, error) {
		res, err := eng.Snap(ctx, core.SnapOptions{})
		if err == nil && res.Created {
			atomic.AddInt32(created, 1)
		}
		return res.Created, res.StateID, err
	}
	w, err := watch.New(root, snap, func(watch.Event) {}, watch.WithTiming(30*time.Millisecond, 500*time.Millisecond))
	if err != nil {
		t.Fatalf("watch.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() { _ = w.Run(ctx); close(runDone) }()

	// Give Run a moment to place its watches before the caller edits files.
	time.Sleep(150 * time.Millisecond)

	stop = func() {
		cancel()
		<-runDone
		eng.Close()
	}
	return root, created, stop
}

// waitFor polls until cond holds or a few seconds pass, generous enough to
// absorb filesystem-event and settle latency without flaking.
func waitFor(cond func() bool) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestWatcherSnapsOnChange checks that editing a tracked file, and creating a
// file in a brand-new subdirectory, each drive a snapshot.
func TestWatcherSnapsOnChange(t *testing.T) {
	root, created, stop := newWatched(t)
	defer stop()

	writeFile(t, root, "main.go", "v1")
	if !waitFor(func() bool { return atomic.LoadInt32(created) >= 1 }) {
		t.Fatalf("expected a snapshot after editing a file, got %d", atomic.LoadInt32(created))
	}

	// A new subdirectory must be watched too (inotify is per-directory).
	writeFile(t, root, "sub/nested/note.txt", "hi")
	if !waitFor(func() bool { return atomic.LoadInt32(created) >= 2 }) {
		t.Fatalf("expected a snapshot for a file in a new subdir, got %d", atomic.LoadInt32(created))
	}
}

// TestWatcherIgnoresArtifacts checks that churn on an ignored file does not drive
// a snapshot, while a real change still does (proving the watcher stayed alive).
func TestWatcherIgnoresArtifacts(t *testing.T) {
	root, created, stop := newWatched(t)
	defer stop()

	// An ignored temp file (matches the built-in *.tmp default) must not trigger.
	writeFile(t, root, "build.tmp", "junk")
	time.Sleep(400 * time.Millisecond) // longer than settle
	if n := atomic.LoadInt32(created); n != 0 {
		t.Fatalf("ignored .tmp file drove %d snapshot(s), want 0", n)
	}

	// A real edit still snapshots.
	writeFile(t, root, "main.go", "real")
	if !waitFor(func() bool { return atomic.LoadInt32(created) >= 1 }) {
		t.Fatal("watcher did not snapshot a real change after ignoring an artifact")
	}
}

// TestWatcherCoalesces checks that a rapid burst of writes collapses into far
// fewer snapshots than writes (debounce + coalescing).
func TestWatcherCoalesces(t *testing.T) {
	root, created, stop := newWatched(t)
	defer stop()

	for i := 0; i < 20; i++ {
		writeFile(t, root, "main.go", string(rune('a'+i)))
		time.Sleep(2 * time.Millisecond) // well under the 30ms settle
	}
	// Let it settle.
	if !waitFor(func() bool { return atomic.LoadInt32(created) >= 1 }) {
		t.Fatal("expected at least one snapshot from the burst")
	}
	time.Sleep(300 * time.Millisecond)
	if n := atomic.LoadInt32(created); n > 5 {
		t.Fatalf("burst of 20 writes drove %d snapshots, expected coalescing to a few", n)
	}
}
