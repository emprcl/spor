package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/emprcl/spor/internal/core"
	"github.com/emprcl/spor/internal/textfmt"
	"github.com/emprcl/spor/internal/view"
)

// This file renders the TUI. Layout is stacked bands: a one-line status bar with the
// metadata line under it, the body (tree on the left, detail panel on the right),
// and a one-line help bar. The diff overlay takes the whole screen; the confirm/
// prompt/help overlays are boxes centered over the body.

// View composes the current frame. Everything is drawn to fixed cell dimensions so
// the vertical/horizontal joins line up exactly.
func (m *model) View() tea.View {
	if m.width == 0 || m.height == 0 {
		return altView("")
	}
	if m.mode == modeDiff {
		return altView(m.viewDiff())
	}
	// A blank line sits under the metadata line and above the help bar, so the body
	// breathes between the chrome bands.
	content := lipgloss.JoinVertical(lipgloss.Left,
		m.viewStatusBar(),
		m.viewMetaBar(),
		"",
		m.viewMiddle(),
		"",
		m.viewHelpBar(),
	)
	return altView(content)
}

// altView wraps a rendered string in a full-window (alternate-screen) view with
// mouse reporting on, so the wheel scrolls the tree and the diff.
func altView(s string) tea.View {
	v := tea.NewView(s)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// ---- dimensions -----------------------------------------------------------

// bodyHeight is the space left for the body between the chrome: the status bar, the
// metadata line, the help bar, and the blank margin above each of the body and the
// help bar (five lines total).
func (m *model) bodyHeight() int {
	if h := m.height - 5; h > 0 {
		return h
	}
	return 1
}

func (m *model) treeBodyHeight() int { return m.bodyHeight() }

// diffBodyHeight is the diff viewport's height: the full screen minus the footer.
func (m *model) diffBodyHeight() int {
	if h := m.height - 1; h > 0 {
		return h
	}
	return 1
}

func (m *model) showDetail() bool { return m.width >= minSplitWidth }

func (m *model) treeWidth() int {
	if !m.showDetail() {
		return m.width
	}
	if w := m.width - detailWidth - 1; w > 0 { // 1 col for the divider
		return w
	}
	return 1
}

// ---- status, metadata & help bars ------------------------------------------

// viewStatusBar draws the top line: the watch/browse indicator, the project path,
// and any transient note.
func (m *model) viewStatusBar() string {
	var b strings.Builder
	if m.watching {
		b.WriteString(watchIndicator(m.sty, m.pulseStep))
		b.WriteString(" ")
	} else {
		b.WriteString(m.sty.Muted.Render("● browsing "))
	}
	b.WriteString(m.sty.WatchPath.Render(prettyPath(m.root)))

	if note := m.statusNote(); note != "" {
		b.WriteString(m.sty.StatusKey.Render("   ") + note)
	}
	return fitLine(b.String(), m.width)
}

// viewMetaBar draws the line under the status bar: the history size, the store's
// disk use, and where @ sits. It is styled muted, quieter than the status bar above
// it, so it reads as ambient context rather than something to act on.
func (m *model) viewMetaBar() string {
	sep := m.sty.StatusKey.Render(" · ")

	stats := fmt.Sprintf("%d %s", m.status.StateCount, textfmt.Plural(m.status.StateCount, "snap", "snaps"))
	if m.status.Tips > 0 {
		stats += fmt.Sprintf(" · %d %s", m.status.Tips, textfmt.Plural(m.status.Tips, "timeline", "timelines"))
	}
	stats += " · " + textfmt.HumanBytes(m.status.StoreBytes)

	pos := "at tip"
	switch {
	case !m.status.HasHead:
		pos = "no snapshots"
	case m.status.Ahead > 0:
		pos = fmt.Sprintf("%d ahead", m.status.Ahead)
	}

	return fitLine("  "+m.sty.StatusKey.Render(stats)+sep+m.sty.StatusKey.Render("@ "+pos), m.width)
}

// statusNote is the transient message at the end of the status bar: a watcher error,
// the last action's result, or the current activity, in that priority.
func (m *model) statusNote() string {
	switch {
	case m.errMsg != "":
		return m.sty.WatchErr.Render("✗ " + m.errMsg)
	case m.flash != "":
		if m.flashLvl == flashBad {
			return m.sty.WatchErr.Render(m.flash)
		}
		return m.sty.Good.Render(m.flash)
	case m.activity != "":
		return m.sty.WatchHint.Render(m.activity)
	}
	return ""
}

// viewHelpBar draws the bottom line: the keys available in the current mode,
// rendered by charm.land/bubbles/v2/help so it truncates gracefully on a narrow
// terminal.
func (m *model) viewHelpBar() string {
	return fitLine("  "+m.help.ShortHelpView(m.keys.shortHelp(m.mode)), m.width)
}

// ---- body -----------------------------------------------------------------

// viewMiddle draws the body: the tree and detail panel, or a centered overlay box
// for the confirm/prompt/help modes.
func (m *model) viewMiddle() string {
	bodyH := m.bodyHeight()
	switch m.mode {
	case modeConfirm:
		return m.viewConfirm(bodyH)
	case modePrompt:
		return m.viewPrompt(bodyH)
	case modeHelp:
		return m.viewHelp(bodyH)
	}

	tree := m.viewTree(bodyH)
	if !m.showDetail() {
		return tree
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, tree, m.dividerCol(bodyH), m.viewDetail(bodyH))
}

func (m *model) dividerCol(bodyH int) string {
	line := m.sty.Conn.Render("│")
	lines := make([]string, bodyH)
	for i := range lines {
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

// viewTree draws the scrollable history tree, marking the selected row with a solid
// gutter block and a full-width background bar. It always returns exactly bodyH lines
// of treeWidth cells.
func (m *model) viewTree(bodyH int) string {
	tw := m.treeWidth()
	if m.indexing {
		return m.viewIndexing(bodyH, tw)
	}
	if len(m.rows) == 0 {
		return m.emptyTree(bodyH, tw)
	}

	selLine := -1
	if m.selectedRow() != nil {
		selLine = m.selectable[m.cursor]
	}
	lines := make([]string, 0, bodyH)
	for i := m.top; i < m.top+bodyH; i++ {
		switch {
		case i < 0 || i >= len(m.rows):
			lines = append(lines, strings.Repeat(" ", tw))
		case i == selLine:
			lines = append(lines, m.selectedLine(m.rows[i].Text, tw))
		default:
			lines = append(lines, fitLine("  "+m.rows[i].Text, tw))
		}
	}
	return strings.Join(lines, "\n")
}

// selectedLine renders the row under the cursor: an accent gutter block, then the
// row's text restyled from its plain form onto the selection bar. Restyling plain
// text (rather than repainting pre-styled ANSI) keeps the bar solid end to end
// with ordinary styling, no escape-sequence surgery.
func (m *model) selectedLine(text string, tw int) string {
	gutter := m.sty.Selected.Foreground(m.sty.Accent.GetForeground()).Render("▌ ")
	body := m.sty.Selected.Render(fitLine(ansi.Strip(text), tw-2))
	return gutter + body
}

// viewIndexing draws the first-snapshot progress panel: a heading, the progress
// bar, and the current phase's caption, shown in place of the tree while the
// initial snapshot runs. It always returns exactly bodyH lines of tw cells.
func (m *model) viewIndexing(bodyH, tw int) string {
	barW := tw - 4
	if barW > 48 {
		barW = 48
	}
	if barW < 8 {
		barW = 8
	}
	m.progress.SetWidth(barW)

	pct, caption := 0.0, ""
	switch m.indexPhase {
	case core.SnapScan:
		caption = textfmt.ScanningText(m.indexDone)
	case core.SnapStore:
		if m.indexTotal > 0 {
			pct = float64(m.indexDone) / float64(m.indexTotal)
		}
		caption = textfmt.IndexingText(m.indexDone, m.indexTotal)
	case core.SnapSync:
		pct = 1
		caption = textfmt.SyncingText()
	case core.SnapCommit:
		if m.indexTotal > 0 {
			pct = float64(m.indexDone) / float64(m.indexTotal)
		}
		caption = textfmt.SavingText(m.indexDone, m.indexTotal)
	}

	block := []string{
		m.sty.WatchBanner.Render("preparing the first snapshot"),
		"",
		m.progress.ViewAs(pct),
		"",
		m.sty.WatchHint.Render(caption),
	}

	lines := make([]string, bodyH)
	for i := range lines {
		lines[i] = strings.Repeat(" ", tw)
	}
	// Nudge the panel one line down so it isn't jammed against the status bar.
	for i, ln := range block {
		if row := i + 1; row >= 0 && row < bodyH {
			lines[row] = fitLine("  "+ln, tw)
		}
	}
	return strings.Join(lines, "\n")
}

// emptyTree is the placeholder shown when the project has no snapshots yet.
func (m *model) emptyTree(bodyH, tw int) string {
	msg := "No snapshots yet."
	if m.watching {
		msg += " Waiting for the first change…"
	} else {
		msg += " Run 'spor snap' to start one."
	}
	lines := make([]string, bodyH)
	for i := range lines {
		lines[i] = strings.Repeat(" ", tw)
	}
	if bodyH > 0 {
		lines[0] = fitLine("  "+m.sty.Muted.Render(msg), tw)
	}
	return strings.Join(lines, "\n")
}

// viewDetail draws the right panel for the current selection: a node's identity,
// timing, lineage, and the actions available on it, or a note for a fold row. It
// always returns exactly bodyH lines of detailWidth cells.
func (m *model) viewDetail(bodyH int) string {
	var rows []string
	add := func(s string) { rows = append(rows, " "+s) }

	r := m.selectedRow()
	switch {
	case r == nil:
		add(m.sty.Muted.Render("no selection"))
	case r.IsFold:
		add(m.sty.Fold.Render("hidden run"))
		add("")
		add(m.sty.Muted.Render("a linear stretch, hidden to"))
		add(m.sty.Muted.Render("keep the tree compact."))
		add("")
		add(m.sty.WatchHint.Render("→  expand it"))
		add(m.sty.WatchHint.Render("f  fold it into one snapshot"))
	default:
		s := m.byID[r.ID]
		add(m.sty.ID.Render(s.ID))
		add("")
		if s.Label != "" {
			add(m.sty.Label.Render(s.Label))
		} else {
			add(m.sty.Muted.Render("unlabeled"))
		}
		add(m.sty.Time.Render(s.CreatedAt.Format("2006-01-02 15:04")))
		add("")
		if s.ID == m.log.Head {
			add(m.sty.HeadTag.Render("(@) current snapshot"))
			add("")
		}
		if p, ok := m.byID[s.Parent]; ok {
			add(m.sty.StatusKey.Render("parent    ") + m.sty.ID.Render(textfmt.Abbrev(p.ID)))
		} else {
			add(m.sty.StatusKey.Render("parent    ") + m.sty.Muted.Render("root"))
		}
		add(m.sty.StatusKey.Render("children  ") + m.sty.ID.Render(fmt.Sprintf("%d", m.childCount[s.ID])))
		if r.FoldAnchor != "" {
			add("")
			add(m.sty.WatchHint.Render("←  collapse this run"))
		}
		add("")
		add(m.sty.StatusKey.Render("actions"))
		for _, b := range []key.Binding{m.keys.Go, m.keys.Diff, m.keys.Label, m.keys.Pick, m.keys.Drop, m.keys.Trim} {
			add(actionRow(m.sty, b))
		}
	}

	out := make([]string, bodyH)
	for i := 0; i < bodyH; i++ {
		if i < len(rows) {
			out[i] = fitLine(rows[i], detailWidth)
		} else {
			out[i] = strings.Repeat(" ", detailWidth)
		}
	}
	return strings.Join(out, "\n")
}

// actionRow formats one "key  description" line for the detail panel's action
// list, from the binding's own help text so the panel and the keymap agree.
func actionRow(sty view.Theme, b key.Binding) string {
	h := b.Help()
	pad := 7 - lipgloss.Width(h.Key)
	if pad < 1 {
		pad = 1
	}
	return "  " + sty.Accent.Render(h.Key) + strings.Repeat(" ", pad) + sty.Muted.Render(h.Desc)
}

// viewHelp draws the full keybinding reference, generated from the keymap's
// FullHelp groups, as a centered box.
func (m *model) viewHelp(bodyH int) string {
	return m.centeredBox(bodyH, "keys", m.help.FullHelpView(m.keys.FullHelp()))
}

// ---- helpers --------------------------------------------------------------

// boxWidth is the width of the centered overlay boxes, clamped to the terminal.
func (m *model) boxWidth() int {
	boxW := m.width - 8
	if boxW > 64 {
		boxW = 64
	}
	if boxW < 16 {
		boxW = 16
	}
	return boxW
}

// centeredBox renders body inside a rounded, titled box centered in the body area.
func (m *model) centeredBox(bodyH int, title, body string) string {
	content := body
	if title != "" {
		content = m.sty.WatchBanner.Render(title) + "\n\n" + body
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.sty.Conn.GetForeground()).
		Padding(1, 2).
		Width(m.boxWidth()).
		Render(content)
	return lipgloss.Place(m.width, bodyH, lipgloss.Center, lipgloss.Center, box)
}

// fitLine truncates s to w display cells (ANSI-aware) and right-pads it to exactly w.
func fitLine(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "")
	if pad := w - ansi.StringWidth(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// prettyPath shortens the home directory to ~ for a compact status bar.
func prettyPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			if rel == "." {
				return "~"
			}
			return "~" + string(filepath.Separator) + rel
		}
	}
	return p
}
