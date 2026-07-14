package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
)

// newLogCmd builds `spor log`, which renders the project history as a tree
// (docs/design-spec.md §6). History is a tree (single parent, no merges), so it draws
// cleanly with box-drawing connectors.
func newLogCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "log",
		Short: "Show the project history as a tree",
		Args:  cobra.NoArgs,
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
			// The colorprofile writer downsamples or strips the styles' ANSI to
			// match the destination (full color on a terminal, plain when piped).
			out := colorprofile.NewWriter(cmd.OutOrStdout(), os.Environ())
			renderLog(out, res)
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
		fmt.Fprintln(w, "No snaps yet. Run 'spor snap' to create one.")
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

	label := func(s core.StateInfo) string {
		id := s.ID
		if len(id) > short {
			id = id[:short]
		}
		out := styleID.Render(id)
		if s.Label != "" {
			out += "  " + styleLabel.Render(s.Label)
		}
		out += "  " + styleTime.Render(humanizeSince(s.CreatedAt))
		if s.ID == res.Head {
			out += "  " + styleHeadTag.Render("(@)")
		}
		return out
	}

	for _, ln := range foldRuns(layoutGraph(res, byID, childCount, label)) {
		fmt.Fprintln(w, ln.graph+ln.meta)
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

// layoutGraph assigns each state a lane and emits the history's rows newest first.
// History is a tree, so lanes only ever split; read top-down toward the root those
// splits appear as merges, and since there are no cross-branch merges the routing
// stays simple.
func layoutGraph(
	res core.LogResult,
	byID map[string]core.StateInfo,
	childCount map[string]int,
	label func(core.StateInfo) string,
) []graphLine {
	order := topoNewestFirst(res.States, byID, childCount)

	// lanes[i] is the parent id column i's edge is heading toward, "" when free.
	lanes := []string{}
	trimTrailing := func() {
		for len(lanes) > 0 && lanes[len(lanes)-1] == "" {
			lanes = lanes[:len(lanes)-1]
		}
	}
	firstFree := func() int {
		for i, l := range lanes {
			if l == "" {
				return i
			}
		}
		lanes = append(lanes, "")
		return len(lanes) - 1
	}
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
		var myCols []int
		for i, l := range lanes {
			if l == s.ID {
				myCols = append(myCols, i)
			}
		}
		var col int
		if len(myCols) == 0 {
			// A tip: start a fresh lane in the leftmost free column.
			col = firstFree()
			lanes[col] = s.ID
			myCols = []int{col}
		} else {
			col = myCols[0]
		}
		// A branch point: the extra lanes converge into col. Draw the merge, then
		// free them before the node row so the metadata stays aligned.
		if len(myCols) > 1 {
			lines = append(lines, graphLine{graph: drawLink(lanes, col, myCols[1:])})
			for _, j := range myCols[1:] {
				lanes[j] = ""
			}
			trimTrailing()
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
		trimTrailing()
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
		if !(lines[i].isNode && lines[i].width1 && lines[i].foldable) {
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
				meta:  styleTime.Render(fmt.Sprintf("%d %s folded", folded, plural(folded, "snap", "snaps"))),
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

// drawFold renders a fold row: a dotted marker in the run's (single) lane.
func drawFold(col int) string {
	var b strings.Builder
	for i := 0; i <= col; i++ {
		if i == col {
			b.WriteString(styleConn.Render("┊"))
		} else {
			b.WriteString(" ")
		}
		b.WriteString(" ")
	}
	return b.String()
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

// humanizeSince renders a compact relative time like "just now", "5 min ago",
// "3h ago", "2d ago", or an absolute date for anything older than a week.
func humanizeSince(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("Jan 2, 2006")
	}
}
