package blob

import (
	"os"

	"golang.org/x/sys/unix"
)

// batchUsesSyncFS reports that this platform can make a whole batch of blob
// writes durable with one syscall, so Batch defers its per-blob fsyncs.
const batchUsesSyncFS = true

// syncFS flushes all pending writes of the filesystem holding dir with a single
// syncfs(2), making every blob a batch wrote (contents and renames alike)
// durable at once. This is the batch-fsync pattern Git uses: for a first
// snapshot of a large project it replaces thousands of per-blob fsyncs, which
// otherwise dominate the wall clock, with one flush (docs/design-spec.md §8).
func syncFS(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return unix.Syncfs(int(d.Fd()))
}
