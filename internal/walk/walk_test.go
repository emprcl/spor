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

	files, _, err := Walk(root)
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

	files, _, err := Walk(root)
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

	files, _, err := Walk(root)
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

	files, _, err := Walk(root)
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

	files, _, err := Walk(root)
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

func TestWalkSkipsUnreadableDirWithWarning(t *testing.T) {
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

	files, warnings, err := Walk(root)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.Rel)
	}
	want := []string{"ok.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Walk = %v, want %v", got, want)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", warnings)
	}
}

func TestWalkReportsStatFields(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "f.txt", "12345")

	files, _, err := Walk(root)
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
