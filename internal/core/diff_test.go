package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// snap runs a snapshot and fails the test if it does not record a new state.
func snap(t *testing.T, eng *Engine) string {
	t.Helper()
	res, err := eng.Snap(context.Background(), SnapOptions{})
	if err != nil {
		t.Fatalf("Snap: %v", err)
	}
	if !res.Created {
		t.Fatalf("Snap recorded nothing; expected a new state")
	}
	return res.StateID
}

// fileByPath finds a FileDiff by path, failing if it is absent.
func fileByPath(t *testing.T, res DiffResult, path string) FileDiff {
	t.Helper()
	for _, f := range res.Files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no diff for %q; files: %+v", path, res.Files)
	return FileDiff{}
}

// TestDiffAddedModifiedRemoved covers the three basic change kinds in one diff
// and checks the hunk content of the modified file.
func TestDiffAddedModifiedRemoved(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "keep.txt", "one\ntwo\nthree\n")
	write(t, root, "gone.txt", "bye\n")
	snap(t, eng)

	write(t, root, "keep.txt", "one\ntwo changed\nthree\n")
	write(t, root, "new.txt", "hello\n")
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	snap(t, eng)

	res, err := eng.Diff(ctx, "@~1", "@")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(res.Files) != 3 {
		t.Fatalf("expected 3 changed files, got %d: %+v", len(res.Files), res.Files)
	}

	if f := fileByPath(t, res, "new.txt"); f.Kind != Added {
		t.Errorf("new.txt: Kind = %v, want Added", f.Kind)
	}
	if f := fileByPath(t, res, "gone.txt"); f.Kind != Removed {
		t.Errorf("gone.txt: Kind = %v, want Removed", f.Kind)
	}

	f := fileByPath(t, res, "keep.txt")
	if f.Kind != Modified || len(f.Hunks) != 1 {
		t.Fatalf("keep.txt: Kind=%v, hunks=%d, want Modified with 1 hunk", f.Kind, len(f.Hunks))
	}
	var dels, adds, ctxLines []string
	for _, ln := range f.Hunks[0].Lines {
		switch ln.Op {
		case OpDel:
			dels = append(dels, ln.Text)
		case OpAdd:
			adds = append(adds, ln.Text)
		case OpContext:
			ctxLines = append(ctxLines, ln.Text)
		}
	}
	if len(dels) != 1 || dels[0] != "two" {
		t.Errorf("deletions = %v, want [two]", dels)
	}
	if len(adds) != 1 || adds[0] != "two changed" {
		t.Errorf("additions = %v, want [two changed]", adds)
	}
	if len(ctxLines) != 2 || ctxLines[0] != "one" || ctxLines[1] != "three" {
		t.Errorf("context = %v, want [one three]", ctxLines)
	}
}

// TestDiffToStateOfNoChange checks that comparing a state to itself yields no
// files (the "no changes" case the CLI reports).
func TestDiffNoChanges(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "a.txt", "x\n")
	id := snap(t, eng)

	res, err := eng.Diff(ctx, id, "@")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(res.Files) != 0 {
		t.Fatalf("expected no changes, got %+v", res.Files)
	}
	if res.From != id || res.To != id {
		t.Errorf("From/To = %s/%s, want both %s", res.From, res.To, id)
	}
}

// TestDiffBinary checks that a change to a file containing NUL bytes is reported
// coarsely, with no hunks.
func TestDiffBinary(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "blob.bin", "\x00\x01\x02data")
	snap(t, eng)
	write(t, root, "blob.bin", "\x00\x09other")
	snap(t, eng)

	res, err := eng.Diff(ctx, "@~1", "@")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	f := fileByPath(t, res, "blob.bin")
	if !f.Binary || len(f.Hunks) != 0 {
		t.Fatalf("blob.bin: Binary=%v hunks=%d, want binary with no hunks", f.Binary, len(f.Hunks))
	}
}

// TestDiffModeOnly checks that a bare chmod +x, same content, is reported as a
// mode-only modification.
func TestDiffModeOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("execute bit is not meaningfully togglable as root")
	}
	eng, root := newTestEngine(t)
	ctx := context.Background()
	sh := filepath.Join(root, "run.sh")
	write(t, root, "run.sh", "echo hi\n")
	snap(t, eng)
	if err := os.Chmod(sh, 0o755); err != nil {
		t.Fatal(err)
	}
	snap(t, eng)

	res, err := eng.Diff(ctx, "@~1", "@")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	f := fileByPath(t, res, "run.sh")
	if !f.ModeOnly() {
		t.Fatalf("run.sh: not ModeOnly (Binary=%v Trunc=%v hunks=%d oldExec=%v newExec=%v)",
			f.Binary, f.Truncated, len(f.Hunks), f.OldExec, f.NewExec)
	}
	if f.OldExec || !f.NewExec {
		t.Errorf("exec transition = %v->%v, want false->true", f.OldExec, f.NewExec)
	}
}

// TestDiffHunkGrouping checks that distant changes in one file produce separate
// hunks while nearby ones share a hunk.
func TestDiffHunkGrouping(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()

	lines := ""
	for i := 1; i <= 30; i++ {
		lines += string(rune('a'+((i-1)%26))) + "\n"
	}
	write(t, root, "f.txt", lines)
	snap(t, eng)

	// Change line 2 and line 25: far apart, so two hunks.
	b := []byte(lines)
	edited := ""
	i := 0
	for _, ln := range splitLines(b) {
		i++
		switch i {
		case 2:
			edited += "SECOND\n"
		case 25:
			edited += "TWENTYFIFTH\n"
		default:
			edited += ln + "\n"
		}
	}
	write(t, root, "f.txt", edited)
	snap(t, eng)

	res, err := eng.Diff(ctx, "@~1", "@")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	f := fileByPath(t, res, "f.txt")
	if len(f.Hunks) != 2 {
		t.Fatalf("expected 2 hunks for two distant edits, got %d", len(f.Hunks))
	}
}
