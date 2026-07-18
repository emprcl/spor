package tui

import (
	"reflect"
	"testing"
)

// TestPickCandidatesAreManifestFiles checks the pick overlay offers exactly the
// state's manifest files, unchanged.
func TestPickCandidatesAreManifestFiles(t *testing.T) {
	files := []string{"README.md", "docs/spec.md", "src/app/main.go"}
	m := &model{}
	_ = m.openPick("S1", files)
	if !reflect.DeepEqual(m.prompt.candidates, files) {
		t.Errorf("candidates = %v, want the manifest files %v", m.prompt.candidates, files)
	}
}

// TestPickRefilter checks the search narrows case-insensitively, resets the
// highlight, and never mutates the full candidate set.
func TestPickRefilter(t *testing.T) {
	m := &model{}
	_ = m.openPick("S1", []string{"src/app/main.go", "src/lib.go", "docs/spec.md"})
	p := &m.prompt

	if len(p.matches) != len(p.candidates) {
		t.Fatalf("initial matches = %d, want all %d", len(p.matches), len(p.candidates))
	}

	p.sel = 2
	p.input.SetValue("APP")
	p.refilter()
	want := []string{"src/app/main.go"}
	if !reflect.DeepEqual(p.matches, want) {
		t.Errorf("matches = %v, want %v", p.matches, want)
	}
	if p.sel != 0 {
		t.Errorf("sel = %d, want reset to 0", p.sel)
	}

	// Widen back out: the candidate set must have survived the narrow filter.
	p.input.SetValue("")
	p.refilter()
	if len(p.matches) != 3 {
		t.Errorf("after clearing, matches = %d, want 3", len(p.matches))
	}
}
