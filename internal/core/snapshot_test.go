package core

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
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

func TestSnapshotAppendsHeadJournal(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "a.txt", "one")
	first, err := eng.Snapshot(ctx, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// A suppressed no-op moves nothing and must not journal.
	if _, err := eng.Snapshot(ctx, SnapshotOptions{}); err != nil {
		t.Fatal(err)
	}
	write(t, root, "a.txt", "two")
	second, err := eng.Snapshot(ctx, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}

	rows, err := eng.db.QueryContext(ctx, `SELECT state_id FROM head_history ORDER BY seq ASC`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var visited []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		visited = append(visited, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{first.StateID, second.StateID}
	if len(visited) != 2 || visited[0] != want[0] || visited[1] != want[1] {
		t.Fatalf("head_history = %v, want %v", visited, want)
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

// An unreadable file is a hard error naming the file: fix it or .sporignore it
// (docs/SPEC.md §4). Only vanished files are tolerated.
func TestSnapshotUnreadableFileFails(t *testing.T) {
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

	_, err := eng.Snapshot(ctx, SnapshotOptions{})
	if err == nil {
		t.Fatal("Snapshot succeeded despite an unreadable file; want an error")
	}
	if !strings.Contains(err.Error(), "secret.txt") {
		t.Fatalf("error %q does not name the offending file", err)
	}

	// Nothing was recorded: the store still has no states.
	head, err := eng.q.GetHead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if head.Valid {
		t.Fatalf("HEAD = %v after a failed snapshot, want unset", head)
	}
}
