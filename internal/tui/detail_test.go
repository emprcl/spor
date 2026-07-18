package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/emprcl/spor/internal/view"

	"github.com/charmbracelet/x/ansi"
	"github.com/emprcl/spor/internal/core"
)

func TestViewDetailSingleParent(t *testing.T) {
	now := time.Now()
	m := &model{
		keys:       newKeyMap(),
		rows:       []view.LogRow{{ID: "S2", IsNode: true}},
		selectable: []int{0},
		cursor:     0,
		byID: map[string]core.StateInfo{
			"S1": {ID: "S1", CreatedAt: now},
			"S2": {ID: "S2", Parent: "S1", CreatedAt: now},
		},
		childCount: map[string]int{"S1": 1},
		log:        core.LogResult{Head: "S2"},
	}
	out := ansi.Strip(m.viewDetail(24))
	// "parent    S1" is the lineage field with its value; the "diff vs parent"
	// action row must not read as a second lineage line.
	if n := strings.Count(out, "parent    S1"); n != 1 {
		t.Errorf("parent lines = %d, want 1\n%s", n, out)
	}
	if n := strings.Count(out, "children"); n != 1 {
		t.Errorf("children lines = %d, want 1\n%s", n, out)
	}
	t.Logf("\n%s", out)
}
