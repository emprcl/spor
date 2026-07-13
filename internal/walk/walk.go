// Package walk enumerates the tracked files of a project. The filesystem walk,
// not file events, is spor's source of truth (docs/SPEC.md §4), so callers
// rebuild state from whatever Walk returns.
package walk

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/git-pkgs/gitignore"
)

// File is one tracked file. Rel is slash-separated and relative to the project
// root, so manifests are stable across operating systems; Abs is the absolute
// path for reading contents.
type File struct {
	Rel string
	Abs string
	// Exec is the owner-execute bit as reported by the filesystem. It is always
	// false on platforms that cannot report it (Windows), where the snapshot
	// inherits the bit from the parent state instead of observing it here.
	Exec bool
	// Size, MtimeNs, and Inode identify the file for the stat cache
	// (docs/SPEC.md §4). Inode is 0 where unavailable (Windows).
	Size    int64
	MtimeNs int64
	Inode   uint64
}

// StorageDir is the project-local directory spor owns; it is never tracked and
// cannot be re-included by an ignore rule.
const StorageDir = ".spor"

// IgnoreFile is the project's optional gitignore-style exclusion list, read from
// the project root (docs/SPEC.md §4).
const IgnoreFile = ".sporignore"

// defaultIgnorePatterns are things spor ignores out of the box: editor temp/swap
// artifacts and the .git directory (high-churn, tool-owned, and meaningless to
// version). They are applied before .sporignore, so a project can re-include one
// with a negation (e.g. "!keep.tmp"). Unlike .spor, these are overridable.
var defaultIgnorePatterns = []byte(`.git/
.DS_Store
*~
*.tmp
*.swp
*.swo
4913
`)

// Walk returns the tracked files under root, sorted by Rel, plus warnings for
// paths it had to skip. It always skips spor's own storage directory, applies
// the built-in editor-temp defaults, and layers the project's .sporignore (if
// present) on top. Matched directories are pruned wholesale.
//
// Walk never aborts over a single bad path (docs/SPEC.md §4): one that vanishes
// mid-walk (editor atomic saves) is simply gone, and an unreadable one is
// skipped with a warning.
func Walk(root string) (files []File, warnings []string, err error) {
	m := gitignore.New("")
	m.AddPatterns(defaultIgnorePatterns, "")
	ignorePath := filepath.Join(root, IgnoreFile)
	if _, err := os.Stat(ignorePath); err == nil {
		m.AddFromFile(ignorePath, "")
	}

	warn := func(abs string, cause error) {
		rel, relErr := filepath.Rel(root, abs)
		if relErr != nil {
			rel = abs
		}
		warnings = append(warnings, fmt.Sprintf("skipping %s: %v", filepath.ToSlash(rel), cause))
	}

	err = filepath.WalkDir(root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // vanished between enumeration and stat
			}
			if errors.Is(err, fs.ErrPermission) {
				warn(abs, err)
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			return err
		}
		if abs == root {
			return nil
		}
		// .spor is always excluded and cannot be re-included by an ignore rule.
		if d.IsDir() && d.Name() == StorageDir {
			return filepath.SkipDir
		}

		rel, err := filepath.Rel(root, abs)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)

		if m.MatchPath(relSlash, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir // prune the whole subtree (e.g. node_modules)
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		// Only regular files become blobs; skip symlinks, sockets, devices.
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // vanished between enumeration and stat
			}
			warn(abs, err)
			return nil
		}
		files = append(files, File{
			Rel: relSlash,
			Abs: abs,
			// mode&0o111 is the execute bits; Windows never sets them (see File.Exec).
			Exec:    info.Mode()&0o111 != 0,
			Size:    info.Size(),
			MtimeNs: info.ModTime().UnixNano(),
			Inode:   inodeOf(info),
		})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Rel < files[j].Rel })
	return files, warnings, nil
}
