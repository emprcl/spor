package watch

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestErrorTriggersReconcile checks that a watcher error (typically a kernel
// event-queue overflow, which means events were dropped) marks the tree dirty,
// so a reconciling snapshot runs even though no filesystem event survived.
func TestErrorTriggersReconcile(t *testing.T) {
	var snaps, errs atomic.Int32
	snap := func(context.Context) (bool, string, error) {
		snaps.Add(1)
		return false, "", nil
	}
	log := func(ev Event) {
		if ev.Kind == Error {
			errs.Add(1)
		}
	}
	w, err := New(t.TempDir(), snap, log, WithTiming(20*time.Millisecond, 500*time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() { _ = w.Run(ctx); close(runDone) }()
	time.Sleep(100 * time.Millisecond) // let Run place its watches

	w.fsw.Errors <- errors.New("simulated event-queue overflow")

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && snaps.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-runDone

	if errs.Load() == 0 {
		t.Error("the error was not reported to the live monitor")
	}
	if snaps.Load() == 0 {
		t.Error("a watcher error did not trigger a reconciling snapshot")
	}
}
