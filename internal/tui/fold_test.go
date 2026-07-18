package tui

import (
	"github.com/emprcl/spor/internal/view"
	"testing"

	"github.com/emprcl/spor/internal/core"
)

// foldModel builds a model over a tiny stand-in row builder: it hides a linear
// run of "n snaps" behind a summary row unless the anchor is expanded, mirroring
// how the real layout tags expanded rows with their run anchor.
func foldModel() *model {
	const anchor = "S3"
	m := &model{expanded: map[string]bool{}}
	m.buildRows = func(_ core.LogResult, expanded map[string]bool) []view.LogRow {
		if expanded[anchor] {
			return []view.LogRow{
				{ID: "S1", IsNode: true},
				{ID: "S2", IsNode: true},
				{ID: "S3", IsNode: true, FoldAnchor: anchor},
				{ID: "S4", IsNode: true, FoldAnchor: anchor},
			}
		}
		return []view.LogRow{
			{ID: "S1", IsNode: true},
			{ID: "S2", IsNode: true},
			{ID: anchor, IsFold: true},
		}
	}
	m.rebuild()
	return m
}

func TestFoldExpandCollapse(t *testing.T) {
	m := foldModel()

	// Land on the fold summary row and open it.
	m.selectID("S3")
	if r := m.selectedRow(); r == nil || !r.IsFold {
		t.Fatalf("expected to select the fold row, got %+v", r)
	}
	m.setFold(true)
	if !m.expanded["S3"] {
		t.Fatal("setFold(true) did not expand the run")
	}
	if r := m.selectedRow(); r == nil || r.ID != "S3" || r.IsFold {
		t.Fatalf("after expand, cursor should sit on the anchor node, got %+v", r)
	}

	// Collapse again from a *different* row of the expanded run (not the anchor).
	m.selectID("S4")
	if r := m.selectedRow(); r == nil || r.FoldAnchor != "S3" {
		t.Fatalf("expected a tagged run row, got %+v", r)
	}
	m.setFold(false)
	if m.expanded["S3"] {
		t.Fatal("setFold(false) did not collapse the run")
	}
	if r := m.selectedRow(); r == nil || !r.IsFold || r.ID != "S3" {
		t.Fatalf("after collapse, cursor should return to the fold row, got %+v", r)
	}
}
