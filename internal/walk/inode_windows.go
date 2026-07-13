//go:build windows

package walk

import "io/fs"

// inodeOf returns 0 on Windows, which exposes no inode through os.FileInfo; the
// stat cache then matches on size and mtime alone. See inode_unix.go.
func inodeOf(fs.FileInfo) uint64 { return 0 }
