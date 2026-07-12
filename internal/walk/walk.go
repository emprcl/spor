// Package walk enumerates the tracked files of a project. The filesystem walk,
// not file events, is spor's source of truth (docs/SPEC.md §4), so callers
// rebuild state from whatever Walk returns.
package walk

import (
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

// Walk returns the tracked files under root, sorted by Rel. It always skips
// spor's own storage directory, applies the built-in editor-temp defaults, and
// layers the project's .sporignore (if present) on top. Matched directories are
// pruned wholesale.
func Walk(root string) ([]File, error) {
	m := gitignore.New("")
	m.AddPatterns(defaultIgnorePatterns, "")
	ignorePath := filepath.Join(root, IgnoreFile)
	if _, err := os.Stat(ignorePath); err == nil {
		m.AddFromFile(ignorePath, "")
	}

	var files []File
	err := filepath.WalkDir(root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
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
		files = append(files, File{Rel: relSlash, Abs: abs})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Rel < files[j].Rel })
	return files, nil
}
