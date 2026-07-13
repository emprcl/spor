package walk

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWalkSkipsStorageAndTempFiles(t *testing.T) {
	root := t.TempDir()

	writeFile(t, root, "main.go", "package main")
	writeFile(t, root, "shaders/frag.glsl", "void main(){}")
	writeFile(t, root, "a.txt", "a")

	// Should all be ignored:
	writeFile(t, root, ".spor/spor.db", "db")         // storage dir
	writeFile(t, root, ".spor/blobs/deadbeef", "b")   // nested in storage dir
	writeFile(t, root, "scratch.tmp", "t")            // editor temp
	writeFile(t, root, "notes.md~", "backup")         // editor backup
	writeFile(t, root, ".DS_Store", "macos")          // macOS
	writeFile(t, root, "sub/.hidden.swp", "vim swap") // vim swap

	files, err := Walk(root)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	var got []string
	for _, f := range files {
		got = append(got, f.Rel)
	}
	want := []string{"a.txt", "main.go", "shaders/frag.glsl"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Walk = %v, want %v", got, want)
	}
}

func TestWalkRespectsSporignore(t *testing.T) {
	root := t.TempDir()

	writeFile(t, root, "main.go", "package main")
	writeFile(t, root, "keep.txt", "keep")
	writeFile(t, root, "debug.log", "noise")            // ignored by *.log
	writeFile(t, root, "build/out.bin", "artifact")     // ignored by build/
	writeFile(t, root, "node_modules/dep/index.js", "") // ignored by node_modules/
	writeFile(t, root, "src/app.log", "nested log")     // ignored by *.log at any depth

	writeFile(t, root, ".sporignore", "# generated stuff\n*.log\nbuild/\nnode_modules/\n")

	files, err := Walk(root)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.Rel)
	}
	// .sporignore itself is tracked (like .gitignore), ignored paths are gone.
	want := []string{".sporignore", "keep.txt", "main.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Walk = %v, want %v", got, want)
	}
}

func TestWalkIgnoresGitByDefault(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", "package main")
	writeFile(t, root, ".gitignore", "node_modules/")  // a project file: tracked
	writeFile(t, root, ".git/config", "[core]")        // git internals: ignored
	writeFile(t, root, ".git/objects/ab/cdef", "blob") // ignored, no .sporignore needed

	files, err := Walk(root)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.Rel)
	}
	want := []string{".gitignore", "main.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Walk = %v, want %v", got, want)
	}
}

func TestWalkNegationReincludes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "scratch.tmp", "a") // ignored by the *.tmp default
	writeFile(t, root, "keep.tmp", "b")    // re-included by .sporignore negation
	writeFile(t, root, ".sporignore", "!keep.tmp\n")

	files, err := Walk(root)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.Rel)
	}
	want := []string{".sporignore", "keep.tmp"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Walk = %v, want %v", got, want)
	}
}

func TestWalkRelPathsAreSlashSeparated(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, filepath.Join("a", "b", "c.txt"), "x")

	files, err := Walk(root)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Rel != "a/b/c.txt" {
		t.Fatalf("Rel = %q, want slash-separated %q", files[0].Rel, "a/b/c.txt")
	}
}

// An unreadable directory is a hard error (docs/SPEC.md §4): fix it or ignore
// it via .sporignore. Only vanished paths are tolerated.
func TestWalkFailsOnUnreadableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits are not enforced for root")
	}
	root := t.TempDir()
	writeFile(t, root, "ok.txt", "fine")
	writeFile(t, root, "locked/hidden.txt", "unreachable")
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	if _, err := Walk(root); err == nil {
		t.Fatal("Walk succeeded despite an unreadable directory; want an error")
	}
}

func TestWalkReportsStatFields(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "f.txt", "12345")

	files, err := Walk(root)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	f := files[0]
	if f.Size != 5 {
		t.Fatalf("Size = %d, want 5", f.Size)
	}
	if f.MtimeNs == 0 {
		t.Fatal("MtimeNs = 0, want the file's mtime")
	}
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestMatcherMatchesWalkRules checks the exposed matcher applies the same
// defaults and .sporignore layering the walk uses, so the watcher filters events
// identically.
func TestMatcherMatchesWalkRules(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, IgnoreFile, "build/\n!keep.tmp\n")

	m := NewMatcher(root)
	cases := []struct {
		rel     string
		isDir   bool
		ignored bool
	}{
		{"main.go", false, false},    // tracked
		{"notes.tmp", false, true},   // built-in *.tmp default
		{"keep.tmp", false, false},   // re-included by .sporignore negation
		{"build", true, true},        // directory-only rule from .sporignore
		{"src/app.js", false, false}, // nested tracked
		{".git", true, true},         // built-in .git default
	}
	for _, c := range cases {
		if got := m.Ignored(c.rel, c.isDir); got != c.ignored {
			t.Errorf("Ignored(%q, isDir=%v) = %v, want %v", c.rel, c.isDir, got, c.ignored)
		}
	}
}
