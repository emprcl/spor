package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// blobPath returns the on-disk path of a blob from its hash (the git-style
// fanout), for tests that tamper with the store.
func blobPath(root, hash string) string {
	return filepath.Join(root, storageDir, "blobs", hash[:2], hash[2:])
}

// anyReferencedHash returns one blob hash referenced by the current states.
func anyReferencedHash(t *testing.T, eng *Engine) string {
	t.Helper()
	refs, err := eng.referencedBlobHashes(context.Background())
	if err != nil {
		t.Fatalf("referencedBlobHashes: %v", err)
	}
	for h := range refs {
		return h
	}
	t.Fatal("no referenced blobs")
	return ""
}

func TestVerifyClean(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "f.txt", "one")
	snap(t, eng)
	write(t, root, "f.txt", "two")
	write(t, root, "g.txt", "gee")
	snap(t, eng)

	res, err := eng.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !res.OK() {
		t.Fatalf("expected a clean store, got issues: %+v", res.Issues)
	}
	if res.StatesChecked != 2 {
		t.Errorf("StatesChecked = %d, want 2", res.StatesChecked)
	}
	if res.BlobsChecked < 1 {
		t.Errorf("BlobsChecked = %d, want >= 1", res.BlobsChecked)
	}
}

func TestVerifyDetectsMissingBlob(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "f.txt", "content")
	snap(t, eng)

	hash := anyReferencedHash(t, eng)
	if err := os.Remove(blobPath(root, hash)); err != nil {
		t.Fatal(err)
	}

	res, err := eng.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !hasIssue(res, "missing-blob") {
		t.Fatalf("expected a missing-blob issue, got %+v", res.Issues)
	}
}

func TestVerifyDetectsCorruptBlob(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "f.txt", "content")
	snap(t, eng)

	hash := anyReferencedHash(t, eng)
	if err := os.WriteFile(blobPath(root, hash), []byte("not a valid zstd blob"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := eng.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !hasIssue(res, "corrupt-blob") {
		t.Fatalf("expected a corrupt-blob issue, got %+v", res.Issues)
	}
}

func hasIssue(res VerifyResult, kind string) bool {
	for _, iss := range res.Issues {
		if iss.Kind == kind {
			return true
		}
	}
	return false
}
