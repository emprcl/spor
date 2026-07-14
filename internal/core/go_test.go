package core

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// snapID takes a snapshot and returns the new state id, failing if none was
// created.
func snapID(t *testing.T, eng *Engine) string {
	t.Helper()
	res, err := eng.Snap(context.Background(), SnapOptions{})
	if err != nil {
		t.Fatalf("Snap: %v", err)
	}
	if !res.Created {
		t.Fatal("expected a state to be created")
	}
	return res.StateID
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(b)
}

func mustHead(t *testing.T, eng *Engine) string {
	t.Helper()
	res, err := eng.Log(context.Background())
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	return res.Head
}

// TestGoMaterializesTarget covers the core case: restoring an older state
// rewrites changed files and removes files added since.
func TestGoMaterializesTarget(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "main.go", "v1")
	a := snapID(t, eng)

	write(t, root, "main.go", "v2")
	write(t, root, "extra.txt", "new file")
	snapID(t, eng)

	res, err := eng.Go(ctx, a)
	if err != nil {
		t.Fatalf("Go: %v", err)
	}
	if res.StateID != a {
		t.Fatalf("restored %s, want %s", res.StateID, a)
	}
	if got := readFile(t, root, "main.go"); got != "v1" {
		t.Fatalf("main.go = %q, want v1", got)
	}
	if _, err := os.Stat(filepath.Join(root, "extra.txt")); !os.IsNotExist(err) {
		t.Fatalf("extra.txt should have been removed, stat err = %v", err)
	}
	if h := mustHead(t, eng); h != a {
		t.Fatalf("HEAD = %s, want %s", h, a)
	}
}

// TestGoForceSettlesUncommittedEdit checks that an edit made after the last
// snapshot is recorded as its own state before the jump, so restore is undoable.
func TestGoForceSettlesUncommittedEdit(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "main.go", "v1")
	a := snapID(t, eng)

	// Edit without snapshotting, then restore.
	write(t, root, "main.go", "uncommitted")
	res, err := eng.Go(ctx, a)
	if err != nil {
		t.Fatalf("Go: %v", err)
	}
	if !res.Settled || res.SettledID == "" {
		t.Fatalf("expected the uncommitted edit to be force-settled, got %+v", res)
	}
	if got := readFile(t, root, "main.go"); got != "v1" {
		t.Fatalf("main.go = %q, want v1", got)
	}
	// The settled state must survive, holding the uncommitted content.
	settled := execManifestContent(t, eng, res.SettledID, "main.go")
	if settled != "uncommitted" {
		t.Fatalf("settled state main.go = %q, want the uncommitted edit", settled)
	}
}

// TestGoNoUncommittedChangesDoesNotSettle verifies force-settle is a no-op
// when the tree already matches HEAD.
func TestGoNoUncommittedChangesDoesNotSettle(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "main.go", "v1")
	a := snapID(t, eng)
	write(t, root, "main.go", "v2")
	b := snapID(t, eng)

	res, err := eng.Go(ctx, a)
	if err != nil {
		t.Fatalf("Go: %v", err)
	}
	if res.Settled {
		t.Fatalf("nothing was uncommitted, but restore settled a state: %+v", res)
	}
	// Restoring forward again should also not settle.
	if res2, err := eng.Go(ctx, b); err != nil || res2.Settled {
		t.Fatalf("forward restore: err=%v res=%+v", err, res2)
	}
	if got := readFile(t, root, "main.go"); got != "v2" {
		t.Fatalf("main.go = %q, want v2", got)
	}
}

// TestGoRemovesEmptyDirectories checks that a directory that becomes empty
// after its only tracked file is removed is cleaned up.
func TestGoRemovesEmptyDirectories(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "keep.txt", "x")
	a := snapID(t, eng)
	write(t, root, "sub/nested/f.txt", "y")
	snapID(t, eng)

	if _, err := eng.Go(ctx, a); err != nil {
		t.Fatalf("Go: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "sub")); !os.IsNotExist(err) {
		t.Fatalf("empty directory sub/ should have been removed, stat err = %v", err)
	}
}

// TestGoExecBit checks that the stored execute bit is reapplied on restore.
func TestGoExecBit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("execute bit is not observable on Windows")
	}
	eng, root := newTestEngine(t)
	ctx := context.Background()

	script := filepath.Join(root, "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := snapID(t, eng)

	// Drop the execute bit and snapshot, then restore the executable version.
	if err := os.Chmod(script, 0o644); err != nil {
		t.Fatal(err)
	}
	snapID(t, eng)

	if _, err := eng.Go(ctx, a); err != nil {
		t.Fatalf("Go: %v", err)
	}
	info, err := os.Stat(script)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o100 == 0 {
		t.Fatalf("run.sh should be executable after restore, mode = %v", info.Mode())
	}
}

// execManifestContent reads the on-disk blob content a state records for a path.
func execManifestContent(t *testing.T, eng *Engine, stateID, path string) string {
	t.Helper()
	var hash string
	if err := eng.db.QueryRowContext(context.Background(),
		`SELECT blob_hash FROM manifest_entries WHERE state_id = ? AND path = ?`,
		stateID, path).Scan(&hash); err != nil {
		t.Fatalf("reading blob hash for %s: %v", path, err)
	}
	r, err := eng.blobs.Open(hash)
	if err != nil {
		t.Fatalf("opening blob %s: %v", hash, err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading blob %s: %v", hash, err)
	}
	return string(b)
}
