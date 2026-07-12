package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverFindsAncestorRoot(t *testing.T) {
	root := t.TempDir()
	// Establish a store at root.
	eng, err := OpenOrInit(context.Background(), root)
	if err != nil {
		t.Fatalf("OpenOrInit: %v", err)
	}
	eng.Close()

	sub := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	got, found, err := Discover(sub)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !found {
		t.Fatal("Discover did not find the ancestor store")
	}
	// t.TempDir may sit behind symlinks (e.g. /var -> /private/var on macOS);
	// compare resolved paths.
	if resolve(t, got) != resolve(t, root) {
		t.Fatalf("Discover root = %s, want %s", got, root)
	}
}

func TestOpenOrInitFromSubdirDoesNotNest(t *testing.T) {
	root := t.TempDir()
	eng, err := OpenOrInit(context.Background(), root)
	if err != nil {
		t.Fatalf("OpenOrInit root: %v", err)
	}
	eng.Close()

	sub := filepath.Join(root, "nested", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	eng2, err := OpenOrInit(context.Background(), sub)
	if err != nil {
		t.Fatalf("OpenOrInit subdir: %v", err)
	}
	defer eng2.Close()

	// It must resolve to the existing root, not create a nested store.
	if resolve(t, eng2.root) != resolve(t, root) {
		t.Fatalf("engine root = %s, want %s", eng2.root, root)
	}
	if _, err := os.Stat(filepath.Join(sub, storageDir)); !os.IsNotExist(err) {
		t.Fatalf("a nested .spor was created in the subdirectory")
	}
}

func TestOpenOrInitGuardsHomeAndRoot(t *testing.T) {
	// Home directory: point HOME at a temp dir and try to init there.
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := OpenOrInit(context.Background(), home); err == nil {
		t.Fatal("expected OpenOrInit to refuse creating a store in the home directory")
	} else if _, statErr := os.Stat(filepath.Join(home, storageDir)); !os.IsNotExist(statErr) {
		t.Fatal("guard refused but still created a .spor in home")
	}

	// Filesystem root: must refuse without touching the filesystem.
	if _, err := OpenOrInit(context.Background(), "/"); err == nil {
		t.Fatal("expected OpenOrInit to refuse creating a store at the filesystem root")
	}
}

func resolve(t *testing.T, path string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return r
}
