package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/emprcl/spor/internal/db/gen"
)

// TestConcurrentReadsDuringWrites exercises the connection pool: many readers run
// while a writer snapshots, and none may error or deadlock (a deadlock trips the
// test timeout).
func TestConcurrentReadsDuringWrites(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "f.txt", "0")
	snap(t, eng)

	errs := make(chan error, 64)
	var wg sync.WaitGroup

	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				if _, err := eng.Log(ctx); err != nil {
					errs <- fmt.Errorf("Log: %w", err)
					return
				}
				if _, err := eng.Status(ctx); err != nil {
					errs <- fmt.Errorf("Status: %w", err)
					return
				}
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; i <= 30; i++ {
			// Write directly (not the t.Fatalf-based helper) from this goroutine.
			if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte(fmt.Sprintf("%d", i)), 0o644); err != nil {
				errs <- err
				return
			}
			if _, err := eng.Snap(ctx, SnapOptions{}); err != nil {
				errs <- fmt.Errorf("Snap: %w", err)
				return
			}
		}
	}()

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent op failed: %v", err)
	}
}

// TestOpenRefusesCorruptStore checks the on-open consistency guard: a store with a
// parent cycle fails OpenExisting with ErrCorruptStore, while OpenForRepair still
// opens it so verify/forget can act.
func TestOpenRefusesCorruptStore(t *testing.T) {
	eng, root := newTestEngine(t)
	ctx := context.Background()
	write(t, root, "f.txt", "A")
	a := snap(t, eng)
	write(t, root, "f.txt", "B")
	b := snap(t, eng) // a -> b

	// Point a's parent at b, making a <-> b a cycle, then close the store.
	if err := eng.q.SetStateParent(ctx, gen.SetStateParentParams{
		ParentID: sql.NullString{String: b, Valid: true},
		ID:       a,
	}); err != nil {
		t.Fatalf("SetStateParent: %v", err)
	}
	eng.Close()

	if _, err := OpenExisting(ctx, root); !errors.Is(err, ErrCorruptStore) {
		t.Fatalf("OpenExisting on a cyclic store = %v, want ErrCorruptStore", err)
	}

	// Repair-mode open must still succeed and let verify report the damage.
	repair, err := OpenForRepair(ctx, root)
	if err != nil {
		t.Fatalf("OpenForRepair: %v", err)
	}
	defer repair.Close()
	res, err := repair.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.OK() {
		t.Error("Verify should report the cycle on a corrupt store")
	}
}
