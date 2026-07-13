package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/spf13/cobra"

	"github.com/emprcl/spor/internal/core"
)

// newLogCmd builds `spor log`, which renders the project history as a tree
// (docs/SPEC.md §6). History is a tree (single parent, no merges), so it draws
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

// Styles for the history tree. lipgloss v2 styles are standalone; color is
// reconciled to the terminal by the colorprofile.Writer they are printed through.
var (
	styleDot     = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	styleHeadDot = lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Bold(true)
	styleID      = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	styleTime    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleLabel   = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	styleConn    = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	styleHeadTag = lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Bold(true)
)

// renderLog draws the history tree to w. Callers wrap w in a colorprofile.Writer
// so the same styled output is colored on a terminal and plain under test or a
// pipe.
func renderLog(w io.Writer, res core.LogResult) {
	if len(res.States) == 0 {
		fmt.Fprintln(w, "No snapshots yet. Run 'spor snapshot' to create one.")
		return
	}

	// Index states, group children by parent, collect roots (parent missing).
	byID := make(map[string]core.StateInfo, len(res.States))
	ids := make([]string, 0, len(res.States))
	for _, s := range res.States {
		byID[s.ID] = s
		ids = append(ids, s.ID)
	}
	children := make(map[string][]core.StateInfo)
	var roots []core.StateInfo
	for _, s := range res.States {
		if _, ok := byID[s.Parent]; s.Parent == "" || !ok {
			roots = append(roots, s)
		} else {
			children[s.Parent] = append(children[s.Parent], s)
		}
	}
	byTime := func(a, b core.StateInfo) bool {
		if a.CreatedAt.Equal(b.CreatedAt) {
			return a.ID < b.ID
		}
		return a.CreatedAt.Before(b.CreatedAt)
	}
	sort.Slice(roots, func(i, j int) bool { return byTime(roots[i], roots[j]) })
	for p := range children {
		c := children[p]
		sort.Slice(c, func(i, j int) bool { return byTime(c[i], c[j]) })
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

	// render prints a node then its descendants. linePrefix precedes the node
	// marker on its own line; childPrefix is the left margin for everything below
	// it. A node with a single child continues in the same column (linear chains
	// stay a straight line); only real branch points introduce connectors.
	var render func(s core.StateInfo, linePrefix, childPrefix string)
	render = func(s core.StateInfo, linePrefix, childPrefix string) {
		mark := styleDot.Render("●")
		if s.ID == res.Head {
			mark = styleHeadDot.Render("●")
		}
		fmt.Fprintln(w, linePrefix+mark+" "+label(s))

		kids := children[s.ID]
		switch len(kids) {
		case 0:
			return
		case 1:
			render(kids[0], childPrefix, childPrefix)
		default:
			for i, k := range kids {
				branch, pad := "├─", "│ "
				if i == len(kids)-1 {
					branch, pad = "└─", "  "
				}
				render(k, childPrefix+styleConn.Render(branch), childPrefix+styleConn.Render(pad))
			}
		}
	}
	for _, r := range roots {
		render(r, "", "")
	}
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
