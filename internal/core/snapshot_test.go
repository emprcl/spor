package core

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func newTestEngine(t *testing.T) (*Engine, string) {
	t.Helper()
	root := t.TempDir()
	eng, err := OpenOrInit(context.Background(), root)
	if err != nil {
		t.Fatalf("OpenOrInit: %v", err)
	}
	t.Cleanup(func() { eng.Close() })
	return eng, root
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func countBlobs(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(filepath.Join(root, storageDir, "blobs"),
		func(_ string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				n++
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSnapshotCreatesThenSuppresses(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "main.go", "package main")

	res, err := eng.Snapshot(ctx, SnapshotOptions{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !res.Created || res.StateID == "" {
		t.Fatalf("first snapshot: got %+v, want Created with an id", res)
	}

	// Nothing changed → no-op suppression.
	res2, err := eng.Snapshot(ctx, SnapshotOptions{})
	if err != nil {
		t.Fatalf("Snapshot #2: %v", err)
	}
	if res2.Created {
		t.Fatalf("second snapshot recorded a state despite no changes: %+v", res2)
	}
}

func TestSnapshotDedupParentAndLabel(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	// Two identical files must share one blob.
	write(t, root, "a.txt", "same")
	write(t, root, "b.txt", "same")

	first, err := eng.Snapshot(ctx, SnapshotOptions{Label: "first"})
	if err != nil {
		t.Fatalf("Snapshot first: %v", err)
	}
	if !first.Created {
		t.Fatal("first snapshot did not create a state")
	}
	if n := countBlobs(t, root); n != 1 {
		t.Fatalf("expected 1 deduplicated blob, got %d", n)
	}

	// Change one file → one new blob, a new state parented on the first.
	write(t, root, "a.txt", "changed")
	second, err := eng.Snapshot(ctx, SnapshotOptions{Label: "second"})
	if err != nil {
		t.Fatalf("Snapshot second: %v", err)
	}
	if !second.Created {
		t.Fatal("second snapshot did not create a state")
	}
	if n := countBlobs(t, root); n != 2 {
		t.Fatalf("expected 2 blobs after edit, got %d", n)
	}

	// Parent linkage: second descends from first.
	var parent sql.NullString
	if err := eng.db.QueryRowContext(ctx,
		`SELECT parent_id FROM states WHERE id = ?`, second.StateID).Scan(&parent); err != nil {
		t.Fatalf("querying parent: %v", err)
	}
	if !parent.Valid || parent.String != first.StateID {
		t.Fatalf("second.parent = %v, want %s", parent, first.StateID)
	}

	// First state's root has no parent.
	var rootParent sql.NullString
	if err := eng.db.QueryRowContext(ctx,
		`SELECT parent_id FROM states WHERE id = ?`, first.StateID).Scan(&rootParent); err != nil {
		t.Fatalf("querying root parent: %v", err)
	}
	if rootParent.Valid {
		t.Fatalf("first state should be a root, got parent %s", rootParent.String)
	}

	// HEAD points at the latest state.
	head, err := eng.q.GetHead(ctx)
	if err != nil {
		t.Fatalf("GetHead: %v", err)
	}
	if !head.Valid || head.String != second.StateID {
		t.Fatalf("HEAD = %v, want %s", head, second.StateID)
	}

	// Label was stored.
	var label sql.NullString
	if err := eng.db.QueryRowContext(ctx,
		`SELECT label FROM states WHERE id = ?`, first.StateID).Scan(&label); err != nil {
		t.Fatalf("querying label: %v", err)
	}
	if !label.Valid || label.String != "first" {
		t.Fatalf("label = %v, want %q", label, "first")
	}
}

// requirePermissionChecks skips tests that rely on chmod 000 being enforced,
// which it is not for root.
func requirePermissionChecks(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission bits are not enforced for root")
	}
}

func TestSnapshotUnreadableFileInheritsHeadEntry(t *testing.T) {
	requirePermissionChecks(t)
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "a.txt", "original")
	write(t, root, "b.txt", "one")
	first, err := eng.Snapshot(ctx, SnapshotOptions{})
	if err != nil {
		t.Fatalf("Snapshot #1: %v", err)
	}

	// Make a.txt unreadable and change b.txt so the next snapshot records state.
	// chmod alone leaves size/mtime/inode intact, so the stat cache would serve
	// a.txt's hash without reading it; drop its row to force the read path this
	// test is about.
	aPath := filepath.Join(root, "a.txt")
	if err := os.Chmod(aPath, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(aPath, 0o644) })
	if _, err := eng.db.ExecContext(ctx, `DELETE FROM stat_cache WHERE path = 'a.txt'`); err != nil {
		t.Fatal(err)
	}
	write(t, root, "b.txt", "two")

	second, err := eng.Snapshot(ctx, SnapshotOptions{})
	if err != nil {
		t.Fatalf("Snapshot #2: %v", err)
	}
	if !second.Created {
		t.Fatal("second snapshot did not create a state")
	}
	if len(second.Warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", second.Warnings)
	}

	// a.txt must still be in the new manifest, with its original blob hash.
	rows, err := eng.q.ListManifestEntries(ctx, second.StateID)
	if err != nil {
		t.Fatal(err)
	}
	prev, err := eng.q.ListManifestEntries(ctx, first.StateID)
	if err != nil {
		t.Fatal(err)
	}
	prevHash := map[string]string{}
	for _, r := range prev {
		prevHash[r.Path] = r.BlobHash
	}
	found := false
	for _, r := range rows {
		if r.Path == "a.txt" {
			found = true
			if r.BlobHash != prevHash["a.txt"] {
				t.Fatalf("a.txt hash = %s, want inherited %s", r.BlobHash, prevHash["a.txt"])
			}
		}
	}
	if !found {
		t.Fatal("unreadable a.txt was recorded as deleted; want inherited entry")
	}
}

func TestSnapshotUnreadableNewFileIsSkipped(t *testing.T) {
	requirePermissionChecks(t)
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "ok.txt", "fine")
	write(t, root, "secret.txt", "nope")
	secret := filepath.Join(root, "secret.txt")
	if err := os.Chmod(secret, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(secret, 0o644) })

	res, err := eng.Snapshot(ctx, SnapshotOptions{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !res.Created {
		t.Fatal("snapshot did not create a state")
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", res.Warnings)
	}
	rows, err := eng.q.ListManifestEntries(ctx, res.StateID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Path == "secret.txt" {
			t.Fatal("unreadable new file should be skipped, not recorded")
		}
	}
}
