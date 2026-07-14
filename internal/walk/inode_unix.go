//go:build !windows

package walk

import (
	"io/fs"
	"syscall"
)

// inodeOf extracts the inode number for the stat cache's identity check
// (docs/design-spec.md §4). It returns 0 when the filesystem does not expose one; the
// cache then matches on size and mtime alone.
func inodeOf(info fs.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}
