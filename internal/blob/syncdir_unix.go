//go:build !windows

package blob

import "os"

// syncDir fsyncs a directory so a rename inside it survives power loss
// (docs/SPEC.md §8): file fsync alone persists the data, not the directory
// entry pointing at it.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
