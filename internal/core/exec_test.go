package core

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestHashManifestExecBitMatters is platform-independent: flipping only the
// execute bit must change the manifest hash, so a bare chmod records a state.
func TestHashManifestExecBitMatters(t *testing.T) {
	off := hashManifest([]manifestEntry{{path: "run.sh", hash: "abc", exec: false}})
	on := hashManifest([]manifestEntry{{path: "run.sh", hash: "abc", exec: true}})
	if off == on {
		t.Fatal("execute bit must change the manifest hash")
	}
}

// execBit reads the stored executable flag for one path of a state.
func execBit(t *testing.T, eng *Engine, stateID, path string) int64 {
	t.Helper()
	var v int64
	if err := eng.db.QueryRowContext(context.Background(),
		`SELECT executable FROM manifest_entries WHERE state_id = ? AND path = ?`,
		stateID, path).Scan(&v); err != nil {
		t.Fatalf("reading executable for %s: %v", path, err)
	}
	return v
}

func TestSnapshotCapturesAndChmodMakesState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the filesystem does not report the execute bit on Windows")
	}
	eng, root := newTestEngine(t)
	ctx := context.Background()

	write(t, root, "run.sh", "#!/bin/sh\necho hi")
	write(t, root, "notes.txt", "plain")
	if err := os.Chmod(filepath.Join(root, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	s1, err := eng.Snapshot(ctx, SnapshotOptions{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := execBit(t, eng, s1.StateID, "run.sh"); got != 1 {
		t.Errorf("run.sh executable = %d, want 1", got)
	}
	if got := execBit(t, eng, s1.StateID, "notes.txt"); got != 0 {
		t.Errorf("notes.txt executable = %d, want 0", got)
	}

	// A bare chmod, no content change, must still record a new state.
	if err := os.Chmod(filepath.Join(root, "run.sh"), 0o644); err != nil {
		t.Fatal(err)
	}
	s2, err := eng.Snapshot(ctx, SnapshotOptions{})
	if err != nil {
		t.Fatalf("Snapshot after chmod: %v", err)
	}
	if !s2.Created {
		t.Fatal("chmod with no content change did not record a state")
	}
	if got := execBit(t, eng, s2.StateID, "run.sh"); got != 0 {
		t.Errorf("run.sh executable after chmod -x = %d, want 0", got)
	}

	// The single blob is shared across both states (content never changed).
	if n := countBlobs(t, root); n != 2 { // run.sh + notes.txt, one each
		t.Errorf("blob count = %d, want 2 (content unchanged by chmod)", n)
	}
}
