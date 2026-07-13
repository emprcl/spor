package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/emprcl/spor/internal/db/gen"
)

func listCache(t *testing.T, eng *Engine) map[string]gen.StatCache {
	t.Helper()
	rows, err := eng.q.ListStatCache(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := make(map[string]gen.StatCache, len(rows))
	for _, r := range rows {
		m[r.Path] = r
	}
	return m
}

// A trusted cache row must let a snapshot reuse the blob hash without opening
// the file. The observable: an unreadable file aborts a snapshot, so an
// unreadable-but-unchanged file succeeding proves it was never opened.
func TestStatCacheHitAvoidsReading(t *testing.T) {
	requirePermissionChecks(t)
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "a.txt", "original")
	write(t, root, "b.txt", "one")
	first, err := eng.Snapshot(ctx, SnapshotOptions{})
	if err != nil {
		t.Fatalf("Snapshot #1: %v", err)
	}

	aPath := filepath.Join(root, "a.txt")
	if err := os.Chmod(aPath, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(aPath, 0o644) })
	write(t, root, "b.txt", "two")

	second, err := eng.Snapshot(ctx, SnapshotOptions{})
	if err != nil {
		t.Fatalf("Snapshot #2 read the unreadable a.txt instead of hitting the cache: %v", err)
	}
	if !second.Created {
		t.Fatal("second snapshot did not create a state")
	}

	// a.txt kept its blob hash, served from the cache.
	firstRows, err := eng.q.ListManifestEntries(ctx, first.StateID)
	if err != nil {
		t.Fatal(err)
	}
	secondRows, err := eng.q.ListManifestEntries(ctx, second.StateID)
	if err != nil {
		t.Fatal(err)
	}
	hash := func(rows []gen.ListManifestEntriesRow, path string) string {
		for _, r := range rows {
			if r.Path == path {
				return r.BlobHash
			}
		}
		t.Fatalf("%s missing from manifest", path)
		return ""
	}
	if hash(secondRows, "a.txt") != hash(firstRows, "a.txt") {
		t.Fatal("a.txt hash changed despite unchanged stats")
	}
}

// A row whose file mtime is not older than the row's recording time is racily
// clean and must not be trusted: the file gets re-read.
func TestStatCacheRacilyCleanRowIsNotTrusted(t *testing.T) {
	requirePermissionChecks(t)
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "a.txt", "original")
	write(t, root, "b.txt", "one")
	if _, err := eng.Snapshot(ctx, SnapshotOptions{}); err != nil {
		t.Fatalf("Snapshot #1: %v", err)
	}

	// Age the row into raciness (recorded_at <= mtime_ns), then make the file
	// unreadable: if the snapshot correctly distrusts the row it must try to
	// read a.txt and fail on it.
	if _, err := eng.db.ExecContext(ctx,
		`UPDATE stat_cache SET recorded_at = mtime_ns WHERE path = 'a.txt'`); err != nil {
		t.Fatal(err)
	}
	aPath := filepath.Join(root, "a.txt")
	if err := os.Chmod(aPath, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(aPath, 0o644) })
	write(t, root, "b.txt", "two")

	if _, err := eng.Snapshot(ctx, SnapshotOptions{}); err == nil {
		t.Fatal("Snapshot trusted a racily-clean row; want a read attempt (which fails here)")
	}
}

// Cache rows track the file set: created on snapshot, removed when the file
// goes away, and repopulated by a suppressed no-op snapshot after a cold start.
func TestStatCacheRowsFollowFiles(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "a.txt", "a")
	write(t, root, "b.txt", "b")
	if _, err := eng.Snapshot(ctx, SnapshotOptions{}); err != nil {
		t.Fatal(err)
	}
	cache := listCache(t, eng)
	if len(cache) != 2 {
		t.Fatalf("cache has %d rows, want 2", len(cache))
	}
	if _, ok := cache["a.txt"]; !ok {
		t.Fatal("cache missing a.txt")
	}

	// Deleting a file removes its row with the state that records the deletion.
	if err := os.Remove(filepath.Join(root, "b.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Snapshot(ctx, SnapshotOptions{}); err != nil {
		t.Fatal(err)
	}
	cache = listCache(t, eng)
	if _, ok := cache["b.txt"]; ok {
		t.Fatal("cache still has a row for deleted b.txt")
	}

	// Cold cache + nothing changed: the suppressed snapshot must still warm the
	// cache, or every future no-op would re-read the whole project.
	if _, err := eng.db.ExecContext(ctx, `DELETE FROM stat_cache`); err != nil {
		t.Fatal(err)
	}
	res, err := eng.Snapshot(ctx, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Created {
		t.Fatal("expected a suppressed no-op snapshot")
	}
	if cache = listCache(t, eng); len(cache) != 1 {
		t.Fatalf("no-op snapshot did not repopulate the cache: %d rows, want 1", len(cache))
	}
}
