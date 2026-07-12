// Package walk enumerates the tracked files of a project. The filesystem walk -
// not file events, is spor's source of truth (docs/SPEC.md §4), so callers
// rebuild state from whatever Walk returns.
package walk

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// File is one tracked file. Rel is slash-separated and relative to the project
// root, so manifests are stable across operating systems; Abs is the absolute
// path for reading contents.
type File struct {
	Rel string
	Abs string
}

// StorageDir is the project-local directory spor owns; it is never tracked.
const StorageDir = ".spor"

// Walk returns the tracked files under root, sorted by Rel. It skips spor's own
// storage directory and common editor temp/swap files. Gitignore-style
// user patterns are deferred (docs/SPEC.md §4).
func Walk(root string) ([]File, error) {
	var files []File
	err := filepath.WalkDir(root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if abs == root {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if name == StorageDir {
				return filepath.SkipDir
			}
			return nil
		}
		if isIgnoredFile(name) {
			return nil
		}
		// Only regular files become blobs; skip symlinks, sockets, devices.
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			return err
		}
		files = append(files, File{Rel: filepath.ToSlash(rel), Abs: abs})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Rel < files[j].Rel })
	return files, nil
}

// isIgnoredFile reports whether a filename is a known editor temp/swap artifact.
func isIgnoredFile(name string) bool {
	switch {
	case name == ".DS_Store":
		return true
	case name == "4913": // vim's atomic-write probe file
		return true
	case strings.HasSuffix(name, "~"):
		return true
	case strings.HasSuffix(name, ".tmp"):
		return true
	case strings.HasSuffix(name, ".swp"), strings.HasSuffix(name, ".swo"):
		return true
	}
	return false
}
