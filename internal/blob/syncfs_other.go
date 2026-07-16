//go:build !linux

package blob

// batchUsesSyncFS is false where no cheap whole-filesystem sync is available
// (Windows, macOS, the BSDs). A Batch there fsyncs each blob inline, exactly as
// a non-batched Put does, so it stays correct (docs/design-spec.md §8) while
// forgoing the first-snapshot speedup. syncFS is unused in this case but defined
// so the package compiles.
const batchUsesSyncFS = false

func syncFS(string) error { return nil }
