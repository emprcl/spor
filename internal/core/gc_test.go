package core

import (
	"context"
	"strings"
	"testing"
)

// TestGCRemovesOrphansKeepsReferenced injects an unreferenced blob and checks GC
// deletes exactly it, leaving every referenced blob intact and reporting bytes.
func TestGCRemovesOrphansKeepsReferenced(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "f.txt", "hello")
	snap(t, eng)

	referenced, err := eng.referencedBlobHashes(ctx)
	if err != nil {
		t.Fatalf("referencedBlobHashes: %v", err)
	}
	if len(referenced) == 0 {
		t.Fatal("expected at least one referenced blob")
	}

	// An orphan blob, referenced by no state (as an abandoned snapshot or drop
	// would leave behind).
	orphan, err := eng.blobs.Put(strings.NewReader("orphan data"))
	if err != nil {
		t.Fatalf("Put orphan: %v", err)
	}
	if _, isRef := referenced[orphan]; isRef {
		t.Fatal("test blob unexpectedly matches a referenced hash")
	}

	res, err := eng.GC(ctx)
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if res.Removed != 1 {
		t.Fatalf("Removed = %d, want 1", res.Removed)
	}
	if res.Bytes <= 0 {
		t.Errorf("Bytes = %d, want > 0", res.Bytes)
	}
	if eng.blobs.Has(orphan) {
		t.Error("orphan blob still present after GC")
	}
	for h := range referenced {
		if !eng.blobs.Has(h) {
			t.Errorf("GC removed a referenced blob %s", h)
		}
	}

	// A second sweep has nothing to do.
	res2, err := eng.GC(ctx)
	if err != nil {
		t.Fatalf("GC #2: %v", err)
	}
	if res2.Removed != 0 {
		t.Errorf("second GC removed %d, want 0", res2.Removed)
	}
}
