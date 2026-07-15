package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestPickBringsBackOneFile covers the core case: restoring a single file
// from an older state rewrites just that file, records the result, and leaves
// every other file and HEAD's position in history alone.
func TestPickBringsBackOneFile(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "sketch.js", "v1")
	write(t, root, "other.txt", "keep")
	a := snapID(t, eng)

	write(t, root, "sketch.js", "v2")
	write(t, root, "other.txt", "keep edited")
	snapID(t, eng)

	res, err := eng.Pick(ctx, a, "sketch.js")
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if res.Written != 1 {
		t.Errorf("Written = %d, want 1", res.Written)
	}
	if !res.Created || res.StateID == "" {
		t.Errorf("pick was not recorded: %+v", res)
	}
	if got := readFile(t, root, "sketch.js"); got != "v1" {
		t.Errorf("sketch.js = %q, want the picked v1", got)
	}
	if got := readFile(t, root, "other.txt"); got != "keep edited" {
		t.Errorf("other.txt = %q; pick must not touch other files", got)
	}
	if head := mustHead(t, eng); head != res.StateID {
		t.Errorf("HEAD = %s, want the recorded pick snapshot %s", head, res.StateID)
	}
}

// TestPickSettlesFirst checks that uncommitted edits are recorded before the
// pick overwrites them, so nothing is lost.
func TestPickSettlesFirst(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "f.txt", "old")
	a := snapID(t, eng)
	write(t, root, "f.txt", "new")
	snapID(t, eng)

	write(t, root, "f.txt", "unsaved edit") // not snapshotted
	res, err := eng.Pick(ctx, a, "f.txt")
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if !res.Settled || res.SettledID == "" {
		t.Fatalf("expected the unsaved edit to be settled first: %+v", res)
	}
	if got := readFile(t, root, "f.txt"); got != "old" {
		t.Errorf("f.txt = %q, want the picked old content", got)
	}
	// The settled state still holds the edit; going back to it recovers it.
	if _, err := eng.Go(ctx, res.SettledID); err != nil {
		t.Fatalf("Go(settled): %v", err)
	}
	if got := readFile(t, root, "f.txt"); got != "unsaved edit" {
		t.Errorf("settled state = %q, want the unsaved edit", got)
	}
}

// TestPickDeletedFileAndDirectory checks that a deleted file comes back, and
// that a directory path picks every file beneath it.
func TestPickDeletedFileAndDirectory(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "shaders/a.glsl", "a1")
	write(t, root, "shaders/sub/b.glsl", "b1")
	a := snapID(t, eng)

	if err := os.RemoveAll(filepath.Join(root, "shaders")); err != nil {
		t.Fatal(err)
	}
	write(t, root, "note.txt", "so the tree is not empty")
	snapID(t, eng)

	res, err := eng.Pick(ctx, a, "shaders")
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if res.Written != 2 {
		t.Errorf("Written = %d, want 2", res.Written)
	}
	if got := readFile(t, root, "shaders/a.glsl"); got != "a1" {
		t.Errorf("shaders/a.glsl = %q, want a1", got)
	}
	if got := readFile(t, root, "shaders/sub/b.glsl"); got != "b1" {
		t.Errorf("shaders/sub/b.glsl = %q, want b1", got)
	}
}

// TestPickNoopAndUnknownPath checks the two edges: content that already
// matches writes nothing and records nothing, and a path the target state never
// held is an error.
func TestPickNoopAndUnknownPath(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "f.txt", "same")
	a := snapID(t, eng)

	res, err := eng.Pick(ctx, a, "f.txt")
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if res.Written != 0 || res.Created {
		t.Errorf("no-op pick wrote %d and created=%v, want 0 and false", res.Written, res.Created)
	}

	if _, err := eng.Pick(ctx, a, "missing.txt"); err == nil {
		t.Error("restoring a path the state never held should error")
	}
}
