package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
)

// newLogCmd builds `spor log`, which renders the project history as a tree
// (docs/design-spec.md §6). History is a tree (single parent, no merges), so it draws
// cleanly with box-drawing connectors.
func newLogCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "log",
		Short: "Show the project history",
		Long: "Show the history newest first. Each branch keeps its own column, and long " +
			"unbranched stretches are folded down to their most recent few. The snapshot " +
			"you are on is marked (@).",
		Example: `  # Show the history
  spor log

  # Page it or search it like any other output
  spor log | less`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			eng, err := core.OpenExisting(ctx, root)
			if err != nil {
				return err
			}
			defer eng.Close()

			res, err := eng.Log(ctx)
			if err != nil {
				return err
			}
			// styledOut downsamples or strips the styles' ANSI to match the
			// destination (full color on a terminal, plain when piped).
			renderLog(styledOut(cmd), res)
			return nil
		},
	}
}

// renderLog draws the history as newest-first swimlanes: each branch keeps its own
// column, and a long linear run is folded down to its most recent few. Callers wrap
// w in a colorprofile.Writer so the styled output is colored on a terminal and plain
// under test or a pipe.
func renderLog(w io.Writer, res core.LogResult) {
	if len(res.States) == 0 {
		fmt.Fprintln(w, "No snapshots yet. Run 'spor snap' to create one.")
		return
	}

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
	short := shortLen(ids)

	// labelField renders a state's label plus, for HEAD, the (@) marker that rides in
	// the same sub-column. It returns the styled text and its display width, so the
	// column can be both measured and padded from one place.
	labelField := func(s core.StateInfo) (string, int) {
		text, w := styleLabel.Render(s.Label), lipgloss.Width(s.Label)
		if s.ID == res.Head {
			sep := ""
			if s.Label != "" {
				sep = " "
			}
			text += sep + styleHeadTag.Render("(@)")
			w += lipgloss.Width(sep + "(@)")
		}
		return text, w
	}

	// Every field in the metadata column is padded to a fixed width so id, time and
	// label line up in their own sub-columns. ids are already uniform (short); time
	// and label vary, so measure the widest of each.
	timeW, labelW := 0, 0
	for _, s := range res.States {
		if w := lipgloss.Width(humanizeSince(s.CreatedAt)); w > timeW {
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
		out := padTo(styleID.Render(id), lipgloss.Width(id), short)

		t := humanizeSince(s.CreatedAt)
		out += " " + padTo(styleTime.Render(t), lipgloss.Width(t), timeW)

		if labelW > 0 { // reserve the label sub-column even for unlabeled rows
			field, w := labelField(s)
			out += " " + padTo(field, w, labelW)
		}
		return out
	}

	cols := assignColumns(res.States, byID)
	lines := foldRuns(layoutGraph(res, byID, childCount, cols, label))
	// Metadata leads each row, the graph trails it. Pad the metadata to the widest
	// one so the graph column starts at the same place on every row and the tree
	// still reads vertically.
	metaW := 0
	for _, ln := range lines {
		if w := lipgloss.Width(ln.meta); w > metaW {
			metaW = w
		}
	}
	for _, ln := range lines {
		meta := ln.meta + strings.Repeat(" ", metaW-lipgloss.Width(ln.meta))
		fmt.Fprintln(w, strings.TrimRight(meta+" "+ln.graph, " "))
	}
}

// graphLine is one rendered row: a lane prefix (graph) plus optional node metadata.
// Link and fold rows carry no metadata of their own.
type graphLine struct {
	graph    string
	meta     string
	isNode   bool
	width1   bool // exactly one lane active on this row (a purely linear point)
	foldable bool // a "boring" interior state: single child and parent, no label, not @
	col      int
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
	// grows and freed lanes simply stay blank; renderLog trims the trailing ones.
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
			lines = append(lines, graphLine{graph: drawLink(lanes, col, myCols[1:])})
			for _, j := range myCols[1:] {
				lanes[j] = ""
			}
		}

		_, hasParent := byID[s.Parent]
		lines = append(lines, graphLine{
			graph:    drawNode(lanes, col, s.ID == res.Head),
			meta:     label(s),
			isNode:   true,
			width1:   activeCount() == 1,
			foldable: s.ID != res.Head && s.Label == "" && childCount[s.ID] == 1 && hasParent,
			col:      col,
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

// foldMax is the number of most-recent states kept when a linear run is folded.
const foldMax = 3

// foldRuns collapses each maximal run of foldable, single-lane rows longer than
// foldMax down to its foldMax most-recent rows plus one summary row for the rest.
// Only single-lane runs fold, so removing rows never hides a parallel branch.
func foldRuns(lines []graphLine) []graphLine {
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
			out = append(out, lines[i:i+foldMax]...)
			folded := run - foldMax
			out = append(out, graphLine{
				graph: drawFold(lines[i].col),
				meta:  styleFold.Render(fmt.Sprintf("%d %s folded", folded, plural(folded, "snap", "snaps"))),
			})
		} else {
			out = append(out, lines[i:j]...)
		}
		i = j
	}
	return out
}

// drawNode renders the lane prefix for a node row: the node's marker in its column,
// a vertical bar for every other active lane, and blanks elsewhere.
func drawNode(lanes []string, col int, isHead bool) string {
	var b strings.Builder
	for i := 0; i < len(lanes); i++ {
		switch {
		case i == col && isHead:
			b.WriteString(styleHeadDot.Render("●"))
		case i == col:
			b.WriteString(styleDot.Render("●"))
		case lanes[i] != "":
			b.WriteString(styleConn.Render("│"))
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
func drawLink(lanes []string, col int, merge []int) string {
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
		b.WriteString(styleConn.Render(glyph))
		if fill == "─" {
			b.WriteString(styleConn.Render("─"))
		} else {
			b.WriteString(" ")
		}
	}
	return b.String()
}

// drawFold renders a fold row: a dotted marker in the run's (single) lane, blank
// columns to its left.
func drawFold(col int) string {
	return strings.Repeat("  ", col) + styleConn.Render("┊") + " "
}

// shortLen returns the smallest prefix length (at least 7) that keeps every id
// distinct, like Git's abbreviated hashes. ULIDs are timestamp-prefixed, so
// close-in-time states share a long common prefix; this expands only as needed.
func shortLen(ids []string) int {
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

// timeFieldWidth is the fixed display width of every humanizeSince value. The
// widest cases ("59s ago", "59m ago", "23h ago", "51w ago") are 7 columns, and
// every shorter value is right-aligned into that width so the number, unit, and
// "ago" always land in the same place.
const timeFieldWidth = 7

// humanizeSince renders a fixed-width relative time: "now", or "<n><unit> ago"
// with a one-letter unit (s, m, h, d, w, y). Every value is exactly
// timeFieldWidth columns wide, right-aligned, so the log's time column stays
// perfectly uniform regardless of the age.
func humanizeSince(t time.Time) string {
	d := time.Since(t)
	var core string
	switch {
	case d < time.Minute && int(d.Seconds()) == 0:
		core = "now"
	case d < time.Minute:
		core = fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		core = fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		core = fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		core = fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		core = fmt.Sprintf("%dw ago", int(d.Hours()/(24*7)))
	default:
		core = fmt.Sprintf("%dy ago", int(d.Hours()/(24*365)))
	}
	return fmt.Sprintf("%*s", timeFieldWidth, core)
}
