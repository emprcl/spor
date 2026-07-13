//go:build windows

package blob

// syncDir is a no-op on Windows, which cannot fsync directory handles; NTFS
// metadata journaling covers the rename durability the unix version provides.
func syncDir(string) error { return nil }
