package view

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/emprcl/spor/internal/core"
	"github.com/emprcl/spor/internal/textfmt"
)

// This file lays the history out as newest-first swimlanes: each branch keeps
// its own column, and long unbranched stretches are hidden down to their most
// recent few (docs/design-spec.md §6). It is the one layout shared by
// `spor log` and the TUI tree.

// LogRow is one laid-out history line. Text is the fully styled line (metadata
// plus graph); ID is the state id on a node row or the hidden run's anchor on a
// summary row; IsNode/IsFold mark the kinds a TUI can select.
type LogRow struct {
	Text   string
	ID     string
	IsNode bool
	IsFold bool
	// FoldAnchor, when set, is the anchor id of the expanded run this row belongs
	// to, so the run can be collapsed again from any row within it.
	FoldAnchor string
	// FoldOldest, on a summary row, is the oldest state of the hidden run, so the
	// whole run (FoldOldest up to ID) can be squashed permanently (`spor fold`).
	FoldOldest string
}

// LogRows lays the history out into rows, honoring the set of expanded run
// anchors (nil hides every long linear run).
func LogRows(th *Theme, res core.LogResult, expanded map[string]bool) []LogRow {
	lines := renderLogLines(logLines(th, res, expanded))
	rows := make([]LogRow, len(lines.text))
	for i, ln := range lines.lines {
		rows[i] = LogRow{
			Text:       lines.text[i],
			ID:         ln.id,
			IsNode:     ln.isNode,
			IsFold:     ln.isFold,
			FoldAnchor: ln.foldAnchor,
			FoldOldest: ln.foldOldest,
		}
	}
	return rows
}

// logLines builds the fully laid-out history rows for res, newest first.
func logLines(th *Theme, res core.LogResult, expanded map[string]bool) []graphLine {
	byID := make(map[string]core.StateInfo, len(res.States))
	ids := make([]string, 0, len(res.States))
	for _, s := range res.States {
		byID[s.ID] = s
		ids = append(ids, s.ID)
	}
	// Count each state's children that are actually present, so leaves (0) and
	// branch points (>1) are distinguishable from linear interior states (1).
	childCount := make(map[string]int, len(res.States))
	for _, s := range res.States {
		if _, ok := byID[s.Parent]; ok {
			childCount[s.Parent]++
		}
	}
	short := ShortLen(ids)

	// labelField renders a state's label plus, for HEAD, the (@) marker that rides
	// in the same sub-column. It returns the styled text and its display width, so
	// the column can be both measured and padded from one place.
	labelField := func(s core.StateInfo) (string, int) {
		text, w := th.Label.Render(s.Label), lipgloss.Width(s.Label)
		if s.ID == res.Head {
			sep := ""
			if s.Label != "" {
				sep = " "
			}
			text += sep + th.HeadTag.Render("(@)")
			w += lipgloss.Width(sep + "(@)")
		}
		return text, w
	}

	// Every field in the metadata column is padded to a fixed width so id, time
	// and label line up in their own sub-columns. ids are already uniform (short);
	// time and label vary, so measure the widest of each.
	timeW, labelW := 0, 0
	for _, s := range res.States {
		if w := lipgloss.Width(textfmt.HumanizeSince(s.CreatedAt)); w > timeW {
			timeW = w
		}
		if _, w := labelField(s); w > labelW {
			labelW = w
		}
	}
	padTo := func(rendered string, plainW, target int) string {
		if pad := target - plainW; pad > 0 {
			return rendered + strings.Repeat(" ", pad)
		}
		return rendered
	}

	label := func(s core.StateInfo) string {
		id := s.ID
		if len(id) > short {
			id = id[:short]
		}
		out := padTo(th.ID.Render(id), lipgloss.Width(id), short)

		t := textfmt.HumanizeSince(s.CreatedAt)
		out += " " + padTo(th.Time.Render(t), lipgloss.Width(t), timeW)

		if labelW > 0 { // reserve the label sub-column even for unlabeled rows
			field, w := labelField(s)
			out += " " + padTo(field, w, labelW)
		}
		return out
	}

	cols := assignColumns(res.States, byID)
	return foldRuns(th, layoutGraph(th, res, byID, childCount, cols, label), expanded)
}

// renderedLines pairs the finished text lines with the graphLines they came
// from, index-aligned.
type renderedLines struct {
	lines []graphLine
	text  []string
}

// renderLogLines pads the metadata column to a common width across lines, so
// the graph column starts at the same place on every row and the tree reads
// vertically, and returns each finished row (metadata then graph, right-trimmed).
func renderLogLines(lines []graphLine) renderedLines {
	metaW := 0
	for _, ln := range lines {
		if w := lipgloss.Width(ln.meta); w > metaW {
			metaW = w
		}
	}
	text := make([]string, len(lines))
	for i, ln := range lines {
		meta := ln.meta + strings.Repeat(" ", metaW-lipgloss.Width(ln.meta))
		text[i] = strings.TrimRight(meta+" "+ln.graph, " ")
	}
	return renderedLines{lines: lines, text: text}
}

// graphLine is one laid-out row: a lane prefix (graph) plus optional node
// metadata. Link and hidden-run summary rows carry no metadata of their own.
type graphLine struct {
	graph    string
	meta     string
	isNode   bool
	width1   bool // exactly one lane active on this row (a purely linear point)
	foldable bool // a "boring" interior state: single child and parent, no label, not @
	col      int
	// id is the state id on a node row, or the anchor id (first hidden state) on a
	// summary row; it is what the TUI selects and, for a hidden run, expands.
	id     string
	isFold bool // this row summarizes a hidden run and can be expanded
	// foldAnchor is set on the rows of an expanded run to that run's anchor id, so
	// the TUI can collapse the run again from any row within it.
	foldAnchor string
	// foldOldest, on a summary row, is the oldest state of the hidden run.
	foldOldest string
}

// assignColumns gives every state a fixed horizontal column derived from the tree
// shape, not from recency, so a timeline keeps its column across renders no matter
// which one is currently active. The original line is the trunk (column 0): at each
// branch point the oldest child continues its parent's column, and every offshoot
// (and every extra root) gets its own column, ordered left-to-right by divergence
// time. Columns are never recycled, so positions stay put as history grows.
func assignColumns(states []core.StateInfo, byID map[string]core.StateInfo) map[string]int {
	earlier := func(a, b core.StateInfo) bool {
		if a.CreatedAt.Equal(b.CreatedAt) {
			return a.ID < b.ID
		}
		return a.CreatedAt.Before(b.CreatedAt)
	}

	// children[parent] holds the present children of each state; oldestChild is the
	// one that continues the parent's column (min CreatedAt, ID as tiebreak).
	children := make(map[string][]core.StateInfo, len(states))
	for _, s := range states {
		if _, ok := byID[s.Parent]; ok {
			children[s.Parent] = append(children[s.Parent], s)
		}
	}
	oldestChild := func(parent string) (core.StateInfo, bool) {
		cs := children[parent]
		if len(cs) == 0 {
			return core.StateInfo{}, false
		}
		best := cs[0]
		for _, c := range cs[1:] {
			if earlier(c, best) {
				best = c
			}
		}
		return best, true
	}

	// A branch-start is any root or any non-oldest child: it seeds a new column.
	var starts []core.StateInfo
	for _, s := range states {
		if _, ok := byID[s.Parent]; !ok {
			starts = append(starts, s) // a root
			continue
		}
		if oldest, ok := oldestChild(s.Parent); ok && oldest.ID != s.ID {
			starts = append(starts, s)
		}
	}
	// Older branches sit to the left; the oldest root lands in column 0.
	sort.Slice(starts, func(i, j int) bool { return earlier(starts[i], starts[j]) })

	col := make(map[string]int, len(states))
	for c, start := range starts {
		// Walk down the oldest-child chain, stamping the whole branch with column c.
		for cur, ok := start, true; ok; {
			col[cur.ID] = c
			cur, ok = oldestChild(cur.ID)
		}
	}
	return col
}

// layoutGraph emits the history's rows newest first, placing each state in its
// fixed column (see assignColumns). History is a tree, so lanes only ever split;
// read top-down toward the root those splits appear as merges, and since there are
// no cross-branch merges the routing stays simple.
func layoutGraph(
	th *Theme,
	res core.LogResult,
	byID map[string]core.StateInfo,
	childCount map[string]int,
	cols map[string]int,
	label func(core.StateInfo) string,
) []graphLine {
	order := topoNewestFirst(res.States, byID, childCount)

	maxCol := 0
	for _, c := range cols {
		if c > maxCol {
			maxCol = c
		}
	}
	// lanes[i] is the parent id column i's edge is heading toward, "" when free.
	// Columns are fixed (see assignColumns) and never reused, so the slice never
	// grows and freed lanes simply stay blank; renderLogLines trims the trailing
	// ones.
	lanes := make([]string, maxCol+1)
	activeCount := func() int {
		n := 0
		for _, l := range lanes {
			if l != "" {
				n++
			}
		}
		return n
	}

	var lines []graphLine
	for _, s := range order {
		col := cols[s.ID]
		var myCols []int
		for i, l := range lanes {
			if l == s.ID {
				myCols = append(myCols, i)
			}
		}
		if len(myCols) == 0 { // a tip: claim its fixed column
			lanes[col] = s.ID
			myCols = []int{col}
		}
		// A branch point: the extra lanes converge into col. Draw the merge, then
		// free them before the node row so the metadata stays aligned.
		if len(myCols) > 1 {
			lines = append(lines, graphLine{graph: drawLink(th, lanes, col, myCols[1:])})
			for _, j := range myCols[1:] {
				lanes[j] = ""
			}
		}

		_, hasParent := byID[s.Parent]
		lines = append(lines, graphLine{
			graph:    drawNode(th, lanes, col, s.ID == res.Head),
			meta:     label(s),
			isNode:   true,
			width1:   activeCount() == 1,
			foldable: s.ID != res.Head && s.Label == "" && childCount[s.ID] == 1 && hasParent,
			col:      col,
			id:       s.ID,
		})

		if hasParent {
			lanes[col] = s.Parent
		} else {
			lanes[col] = "" // a root ends its lane
		}
	}
	return lines
}

// topoNewestFirst orders states so every state precedes its parent (children first)
// and, among states whose children are all placed, the newest goes next. Creation
// times increase down any ancestor chain, so this reads as reverse-chronological
// while never printing a parent before a child, even when timestamps tie.
func topoNewestFirst(states []core.StateInfo, byID map[string]core.StateInfo, childCount map[string]int) []core.StateInfo {
	remaining := make(map[string]int, len(states))
	for id, n := range childCount {
		remaining[id] = n
	}
	newer := func(a, b core.StateInfo) bool {
		if a.CreatedAt.Equal(b.CreatedAt) {
			return a.ID > b.ID
		}
		return a.CreatedAt.After(b.CreatedAt)
	}
	var ready []core.StateInfo
	for _, s := range states {
		if remaining[s.ID] == 0 {
			ready = append(ready, s)
		}
	}
	out := make([]core.StateInfo, 0, len(states))
	for len(ready) > 0 {
		best := 0
		for i := 1; i < len(ready); i++ {
			if newer(ready[i], ready[best]) {
				best = i
			}
		}
		s := ready[best]
		ready = append(ready[:best], ready[best+1:]...)
		out = append(out, s)
		if p, ok := byID[s.Parent]; ok {
			remaining[p.ID]--
			if remaining[p.ID] == 0 {
				ready = append(ready, p)
			}
		}
	}
	return out
}

// foldMax is the number of most-recent states kept when a linear run is hidden.
const foldMax = 3

// foldRuns collapses each maximal run of foldable, single-lane rows longer than
// foldMax down to its foldMax most-recent rows plus one summary row for the rest.
// Only single-lane runs hide, so removing rows never obscures a parallel branch.
// A run whose anchor (its first hidden state's id) is in expanded is shown in
// full instead, so the TUI can reveal a run on demand; a nil expanded hides
// everything.
func foldRuns(th *Theme, lines []graphLine, expanded map[string]bool) []graphLine {
	out := make([]graphLine, 0, len(lines))
	for i := 0; i < len(lines); {
		if !lines[i].isNode || !lines[i].width1 || !lines[i].foldable {
			out = append(out, lines[i])
			i++
			continue
		}
		j := i
		for j < len(lines) && lines[j].isNode && lines[j].width1 && lines[j].foldable {
			j++
		}
		if run := j - i; run > foldMax {
			anchor := lines[i+foldMax].id
			if expanded[anchor] {
				// The always-visible head, then the revealed rows tagged with the anchor
				// so the TUI can collapse the run again from any of them.
				out = append(out, lines[i:i+foldMax]...)
				for k := i + foldMax; k < j; k++ {
					row := lines[k]
					row.foldAnchor = anchor
					out = append(out, row)
				}
			} else {
				out = append(out, lines[i:i+foldMax]...)
				hidden := run - foldMax
				out = append(out, graphLine{
					graph:      drawFold(th, lines[i].col),
					meta:       th.Fold.Render(fmt.Sprintf("%d %s hidden", hidden, textfmt.Plural(hidden, "snap", "snaps"))),
					col:        lines[i].col,
					id:         anchor,
					isFold:     true,
					foldOldest: lines[j-1].id,
				})
			}
		} else {
			out = append(out, lines[i:j]...)
		}
		i = j
	}
	return out
}

// drawNode renders the lane prefix for a node row: the node's marker in its column,
// a vertical bar for every other active lane, and blanks elsewhere.
func drawNode(th *Theme, lanes []string, col int, isHead bool) string {
	var b strings.Builder
	for i := 0; i < len(lanes); i++ {
		switch {
		case i == col && isHead:
			b.WriteString(th.HeadDot.Render("●"))
		case i == col:
			b.WriteString(th.Dot.Render("●"))
		case lanes[i] != "":
			b.WriteString(th.Conn.Render("│"))
		default:
			b.WriteString(" ")
		}
		b.WriteString(" ")
	}
	return b.String()
}

// drawLink renders a merge row where the lanes in merge converge into col: col
// receives from the right (├), the horizontal reaches out to the furthest merging
// lane (╯), intermediate merging lanes join it (┴), and any lane merely crossed by
// the horizontal is drawn as ┼.
func drawLink(th *Theme, lanes []string, col int, merge []int) string {
	merging := make(map[int]bool, len(merge))
	maxM := col
	for _, m := range merge {
		merging[m] = true
		if m > maxM {
			maxM = m
		}
	}
	var b strings.Builder
	for i := 0; i < len(lanes); i++ {
		glyph, fill := " ", " "
		switch {
		case i < col:
			if lanes[i] != "" {
				glyph = "│"
			}
		case i == col:
			glyph, fill = "├", "─"
		case i < maxM:
			fill = "─"
			switch {
			case merging[i]:
				glyph = "┴"
			case lanes[i] != "":
				glyph = "┼"
			default:
				glyph = "─"
			}
		case i == maxM:
			glyph = "╯"
		default:
			if lanes[i] != "" {
				glyph = "│"
			}
		}
		b.WriteString(th.Conn.Render(glyph))
		if fill == "─" {
			b.WriteString(th.Conn.Render("─"))
		} else {
			b.WriteString(" ")
		}
	}
	return b.String()
}

// drawFold renders a hidden-run summary row: a dotted marker in the run's
// (single) lane, blank columns to its left.
func drawFold(th *Theme, col int) string {
	return strings.Repeat("  ", col) + th.Conn.Render("┊") + " "
}

// shortLen returns the smallest prefix length (at least 7) that keeps every id
// distinct, like Git's abbreviated hashes. ULIDs are timestamp-prefixed, so
// close-in-time states share a long common prefix; this expands only as needed.
func ShortLen(ids []string) int {
	const min = 7
	for l := min; l < 26; l++ {
		seen := make(map[string]struct{}, len(ids))
		collision := false
		for _, id := range ids {
			p := id
			if len(p) > l {
				p = p[:l]
			}
			if _, ok := seen[p]; ok {
				collision = true
				break
			}
			seen[p] = struct{}{}
		}
		if !collision {
			return l
		}
	}
	return 26
}
