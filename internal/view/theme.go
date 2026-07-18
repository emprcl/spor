// Package view owns spor's shared presentation: the theme, the history-tree
// layout, and the diff renderer. Both front-ends (the one-shot CLI and the
// interactive TUI) import it, so `spor log`, `spor diff`, and the TUI's views
// stay identical without either front-end depending on the other
// (docs/design-spec.md §6, §8).
package view

import (
	"fmt"
	"image/color"
	"os"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/fang"
	"github.com/charmbracelet/x/term"
)

// Theme is the style bundle every view renders with, grouped by semantic role
// and resolved once for the terminal background. A zero Theme renders plain
// text, which is what tests use.
type Theme struct {
	// accent: HEAD, banners, the current-state marker.
	DiffHead    lipgloss.Style
	HeadDot     lipgloss.Style
	HeadTag     lipgloss.Style
	WatchBanner lipgloss.Style
	// info: structural dots and diff hunk headers.
	Dot      lipgloss.Style
	WatchDot lipgloss.Style
	DiffHunk lipgloss.Style
	// success: additions, labels, healthy status (added is green by diff convention).
	DiffAdd  lipgloss.Style
	Label    lipgloss.Style
	StatusOn lipgloss.Style
	VerifyOK lipgloss.Style
	// danger: deletions, errors, failed checks (removed is red by diff convention).
	DiffDel   lipgloss.Style
	WatchErr  lipgloss.Style
	VerifyBad lipgloss.Style
	// muted: timestamps, hints, keys, diff metadata.
	Time      lipgloss.Style
	WatchHint lipgloss.Style
	StatusKey lipgloss.Style
	DiffMeta  lipgloss.Style
	// secondary: state ids and paths.
	ID        lipgloss.Style
	WatchPath lipgloss.Style
	// faint: history-tree connectors and the hidden-run summary.
	Conn lipgloss.Style
	Fold lipgloss.Style
	// General command-output roles: Accent for the token a command acts on or
	// produces, Good/Bad for created/removed counts and reassuring vs
	// destructive wording, Muted for secondary prose and no-op messages.
	Accent lipgloss.Style
	Good   lipgloss.Style
	Bad    lipgloss.Style
	Muted  lipgloss.Style
	// Pulse is the dim-to-bright ramp for the TUI's watch indicator.
	Pulse []lipgloss.Style
	// Selected is the TUI's selected-row highlight: a background bar spanning
	// the whole line.
	Selected lipgloss.Style
}

var (
	defaultOnce  sync.Once
	defaultTheme *Theme
)

// Default returns the theme for the current terminal, built once from a
// background-adaptive palette so output reads on both light and dark
// terminals. Only a real terminal can answer the background query (an escape
// round-trip through stdin/stdout); piped output is stripped of color anyway,
// so it falls back to the dark palette (the common terminal default).
func Default() *Theme {
	defaultOnce.Do(func() {
		isDark := true
		if term.IsTerminal(os.Stdout.Fd()) {
			isDark = lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
		}
		defaultTheme = themeFrom(tokyo(lipgloss.LightDark(isDark)))
	})
	return defaultTheme
}

// palette is one theme's seven semantic colors, already resolved for the
// current background.
type palette struct {
	accent, info, success, danger, muted, secondary, faint color.Color
}

// tokyo is a Tokyo Night palette: a purple/magenta accent with blue highlights
// over deep blue-grey comment tones.
func tokyo(c lipgloss.LightDarkFunc) palette {
	return palette{
		accent:    c(lipgloss.Color("#7C3AED"), lipgloss.Color("#BB9AF7")), // magenta/purple
		info:      c(lipgloss.Color("#2563EB"), lipgloss.Color("#7AA2F7")), // blue
		success:   c(lipgloss.Color("#4D7C0F"), lipgloss.Color("#9ECE6A")), // green
		danger:    c(lipgloss.Color("#DC2626"), lipgloss.Color("#F7768E")), // red
		muted:     c(lipgloss.Color("#4C5473"), lipgloss.Color("#7982A9")), // comment
		secondary: c(lipgloss.Color("#3B4261"), lipgloss.Color("#A9B1D6")),
		faint:     c(lipgloss.Color("#A9B1D6"), lipgloss.Color("#3B4261")),
	}
}

// HelpColorScheme maps the active palette onto fang's help/usage screen (the
// bare `spor` output), so the command list matches the rest of the tool instead
// of fang's default charm colors. Pass it via fang.WithColorSchemeFunc; fang
// supplies its own light/dark function, so the help screen adapts too.
func HelpColorScheme(c lipgloss.LightDarkFunc) fang.ColorScheme {
	p := tokyo(c)
	text := c(lipgloss.Color("#3A3943"), lipgloss.Color("#DFDBDD"))
	return fang.ColorScheme{
		Base:           text,
		Title:          p.accent,    // section headings (COMMON, MAINTENANCE, …)
		Description:    p.secondary, // command and flag descriptions
		Codeblock:      c(lipgloss.Color("#F1EFEF"), lipgloss.Color("#2A2830")),
		Program:        p.accent, // the program name in the usage line
		Command:        p.info,   // subcommand names in the list
		DimmedArgument: p.muted,
		Comment:        p.muted,
		Flag:           p.success, // --flags
		FlagDefault:    p.faint,
		Argument:       text,
		QuotedString:   p.accent,
		Help:           p.muted,
		Dash:           p.muted,
		ErrorHeader:    [2]color.Color{lipgloss.Color("#FFFAF1"), p.danger}, // light text on a red badge
		ErrorDetails:   p.danger,
	}
}

// themeFrom builds every named style from a resolved palette.
func themeFrom(p palette) *Theme {
	fg := func(c color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }
	bold := func(c color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c).Bold(true) }

	t := &Theme{}

	accent := bold(p.accent)
	t.DiffHead, t.HeadDot, t.HeadTag, t.WatchBanner = accent, accent, accent, accent

	info := fg(p.info)
	t.Dot, t.WatchDot, t.DiffHunk = info, info, info

	t.DiffAdd = fg(p.success)
	successBold := bold(p.success)
	t.Label, t.StatusOn, t.VerifyOK = successBold, successBold, successBold

	t.DiffDel = fg(p.danger)
	dangerBold := bold(p.danger)
	t.WatchErr, t.VerifyBad = dangerBold, dangerBold

	muted := fg(p.muted)
	t.Time, t.WatchHint, t.StatusKey, t.DiffMeta = muted, muted, muted, muted

	secondary := fg(p.secondary)
	t.ID, t.WatchPath = secondary, secondary

	t.Conn, t.Fold = fg(p.faint), fg(p.faint)

	t.Accent, t.Good, t.Bad, t.Muted = accent, successBold, dangerBold, muted

	// A breathing ramp from muted (dim) to accent (bright) for the watch
	// indicator; both ends come from the resolved palette, so it adapts to the
	// terminal background like everything else.
	const pulseSteps = 12
	t.Pulse = make([]lipgloss.Style, pulseSteps)
	for i := range t.Pulse {
		f := float64(i) / float64(pulseSteps-1)
		t.Pulse[i] = fg(lerpColor(p.muted, p.accent, f))
	}

	// The selection bar blends the faint connector tone toward the accent, so
	// it sits clearly above the background; bold weight makes the selected
	// row's text stand out on the bar.
	t.Selected = lipgloss.NewStyle().Background(p.faint).Bold(true)

	return t
}

// lerpColor blends a to b in RGB by f in [0,1], returning a hex color. The
// palette colors are opaque, so the alpha channel is ignored.
func lerpColor(a, b color.Color, f float64) color.Color {
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	mix := func(x, y uint32) uint8 {
		return uint8(float64(x>>8)*(1-f) + float64(y>>8)*f + 0.5)
	}
	return lipgloss.Color(fmt.Sprintf("#%02X%02X%02X", mix(ar, br), mix(ag, bg), mix(ab, bb)))
}
