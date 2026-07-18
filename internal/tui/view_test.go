package tui

import (
	"github.com/emprcl/spor/internal/view"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestSelectedLineRestylesPlain checks the selected tree row is rebuilt from the
// row's plain text: the pre-rendered foreground spans are stripped (so the
// selection bar stays solid without any escape-sequence patching), the accent
// gutter leads the line, and the line still fills the full tree width.
func TestSelectedLineRestylesPlain(t *testing.T) {
	fg := lipgloss.NewStyle().Foreground(lipgloss.Color("#7AA2F7"))
	text := fg.Render("abc123") + "  " + fg.Render("mylabel")

	m := &model{}
	m.sty.Selected = lipgloss.NewStyle().Background(lipgloss.Color("#6E5F9A")).Bold(true)
	m.sty.Accent = lipgloss.NewStyle().Foreground(lipgloss.Color("#BB9AF7")).Bold(true)

	const w = 40
	out := m.selectedLine(text, w)

	if strings.Contains(out, "122;162;247") {
		t.Error("selected line still carries the row's original foreground ANSI")
	}
	plain := ansi.Strip(out)
	if !strings.HasPrefix(plain, "▌ abc123  mylabel") {
		t.Errorf("plain selected line = %q, want gutter then row text", plain)
	}
	if got := ansi.StringWidth(out); got != w {
		t.Errorf("selected line width = %d, want %d", got, w)
	}
}

// TestWatchIndicatorStableWidth checks the animated banner keeps a constant
// display width across the whole sweep (the status bar must not jitter) and
// always reads "watching" under the styling.
func TestWatchIndicatorStableWidth(t *testing.T) {
	sty := view.Theme{Pulse: []lipgloss.Style{
		lipgloss.NewStyle().Foreground(lipgloss.Color("#7982A9")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#9A8ED0")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#BB9AF7")),
	}}
	for step := 0; step < 40; step++ {
		out := watchIndicator(sty, step)
		if w := ansi.StringWidth(out); w != 10 {
			t.Fatalf("step %d: width = %d, want 10", step, w)
		}
		if plain := ansi.Strip(out); !strings.HasSuffix(plain, " watching") {
			t.Fatalf("step %d: text = %q, want suffix %q", step, plain, " watching")
		}
	}
}

// TestFitLine locks in ANSI-aware truncation and exact padding.
func TestFitLine(t *testing.T) {
	styled := lipgloss.NewStyle().Foreground(lipgloss.Color("#9ECE6A")).Render("hello world")
	if got := ansi.StringWidth(fitLine(styled, 5)); got != 5 {
		t.Errorf("truncated width = %d, want 5", got)
	}
	if got := fitLine("hi", 6); got != "hi    " {
		t.Errorf("padded = %q, want %q", got, "hi    ")
	}
}
