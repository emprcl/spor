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
