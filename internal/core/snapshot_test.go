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
	eng, err := Open(context.Background(), root)
	if err != nil {
		t.Fatalf("Open: %v", err)
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
	ents, err := os.ReadDir(filepath.Join(root, storageDir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	return len(ents)
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
