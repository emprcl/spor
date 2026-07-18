package tui

import (
	"github.com/emprcl/spor/internal/view"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/emprcl/spor/internal/core"
)

// TestViewIndexing checks the first-snapshot panel shows the heading, a partly
// filled bar, and the phase caption, and stays exactly bodyH lines wide.
func TestViewIndexing(t *testing.T) {
	m := &model{
		width:    80,
		height:   24,
		indexing: true,
		progress: newProgress(view.Theme{}),
	}

	bodyH := 20
	cases := []struct {
		phase       core.SnapPhase
		done, total int
		want        string
	}{
		{core.SnapScan, 300, 0, "scanning project... 300 files"},
		{core.SnapStore, 512, 2048, "indexing project... 512 / 2,048 files"},
		{core.SnapSync, 0, 0, "syncing to disk..."},
		{core.SnapCommit, 100, 2048, "saving snapshot... 100 / 2,048 files"},
	}
	for _, c := range cases {
		m.indexPhase, m.indexDone, m.indexTotal = c.phase, c.done, c.total
		out := m.viewIndexing(bodyH, m.treeWidth())
		if n := strings.Count(out, "\n") + 1; n != bodyH {
			t.Fatalf("phase %d: line count = %d, want %d", c.phase, n, bodyH)
		}
		plain := ansi.Strip(out)
		if !strings.Contains(plain, "preparing the first snapshot") {
			t.Errorf("phase %d: missing heading\n%s", c.phase, plain)
		}
		if !strings.Contains(plain, c.want) {
			t.Errorf("phase %d: missing caption %q\n%s", c.phase, c.want, plain)
		}
	}
}

// TestUpdateIndexingLifecycle checks the messages flip indexing on and off.
func TestUpdateIndexingLifecycle(t *testing.T) {
	m := &model{progress: newProgress(view.Theme{})}

	if _, _ = m.Update(indexProgressMsg{phase: core.SnapStore, done: 10, total: 100}); !m.indexing {
		t.Fatal("indexProgressMsg did not set indexing")
	}
	if m.indexPhase != core.SnapStore || m.indexDone != 10 || m.indexTotal != 100 {
		t.Fatalf("progress = phase %d %d/%d, want store 10/100", m.indexPhase, m.indexDone, m.indexTotal)
	}
	if _, _ = m.Update(indexDoneMsg{}); m.indexing {
		t.Fatal("indexDoneMsg did not clear indexing")
	}
}
