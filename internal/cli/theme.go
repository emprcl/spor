package cli

import (
	"fmt"
	"image/color"
	"os"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/fang"
	"github.com/charmbracelet/x/term"
)

// The styles spor renders every view with. They live in one place so the palette
// is easy to change (docs/design-spec.md §6), and they are populated once by loadTheme
// from a background-adaptive palette, so output reads on both light and dark
// terminals the way the help screen does. Bold is baked into the roles that use it,
// so the render call sites stay unchanged. Grouped by semantic role:
var (
	// accent: HEAD, banners, the current-state marker.
	styleDiffHead, styleHeadDot, styleHeadTag, styleWatchBanner lipgloss.Style
	// info: structural dots and diff hunk headers.
	styleDot, styleWatchDot, styleDiffHunk lipgloss.Style
	// success: additions, labels, healthy status (added is green by diff convention).
	styleDiffAdd, styleLabel, styleStatusOn, styleVerifyOK lipgloss.Style
	// danger: deletions, errors, failed checks (removed is red by diff convention).
	styleDiffDel, styleWatchErr, styleVerifyBad lipgloss.Style
	// muted: timestamps, hints, keys, diff metadata.
	styleTime, styleWatchHint, styleStatusKey, styleDiffMeta lipgloss.Style
	// secondary: state ids and paths.
	styleID, styleWatchPath lipgloss.Style
	// faint: history-tree connectors and the folded-run summary.
	styleConn, styleFold lipgloss.Style
	// General command-output roles, used by the one-shot commands' result lines so
	// their output is themed like the rest of the tool: styleAccent for the token a
	// command acts on or produces (a result state id, a headline number, a byte
	// size), styleGood/styleBad for created/removed counts and for reassuring vs
	// destructive wording, styleMuted for secondary prose and no-op messages.
	styleAccent, styleGood, styleBad, styleMuted lipgloss.Style
	// stylePulse is the dim→bright ramp for the watch heartbeat dot (see pulseDot).
	stylePulse []lipgloss.Style
)

var themeOnce sync.Once

// loadTheme populates the style vars from the active palette, detecting the
// terminal background once (mirroring how the help screen adapts). Every render
// entry point calls it; only the first call does the work, so it is cheap to call
// repeatedly, e.g. on each watch repaint.
func loadTheme() {
	themeOnce.Do(func() {
		// Only a real terminal can answer the background query, and it is an escape
		// round-trip through stdin/stdout; piped output is stripped of color anyway,
		// so fall back to the dark palette (the common terminal default).
		isDark := true
		if term.IsTerminal(os.Stdout.Fd()) {
			isDark = lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
		}
		applyPalette(tokyo(lipgloss.LightDark(isDark)))
	})
}

// palette is one theme's seven semantic colors, already resolved for the current
// background.
type palette struct {
	accent, info, success, danger, muted, secondary, faint color.Color
}

// tokyo is a Tokyo Night palette: a purple/magenta accent with blue highlights over
// deep blue-grey comment tones.
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

// HelpColorScheme maps the active palette onto fang's help/usage screen (the bare
// `spor` output), so the command list matches the rest of the tool instead of
// fang's default charm colors. Pass it via fang.WithColorSchemeFunc; fang supplies
// its own light/dark function, so the help screen adapts to the background too.
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

// applyPalette builds every named style from a resolved palette.
func applyPalette(p palette) {
	fg := func(c color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }
	bold := func(c color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c).Bold(true) }

	accent := bold(p.accent)
	styleDiffHead, styleHeadDot, styleHeadTag, styleWatchBanner = accent, accent, accent, accent

	info := fg(p.info)
	styleDot, styleWatchDot, styleDiffHunk = info, info, info

	styleDiffAdd = fg(p.success)
	successBold := bold(p.success)
	styleLabel, styleStatusOn, styleVerifyOK = successBold, successBold, successBold

	styleDiffDel = fg(p.danger)
	dangerBold := bold(p.danger)
	styleWatchErr, styleVerifyBad = dangerBold, dangerBold

	muted := fg(p.muted)
	styleTime, styleWatchHint, styleStatusKey, styleDiffMeta = muted, muted, muted, muted

	secondary := fg(p.secondary)
	styleID, styleWatchPath = secondary, secondary

	styleConn, styleFold = fg(p.faint), fg(p.faint)

	// The general command-output roles reuse the same resolved palette: accent for
	// emphasis, success/danger (bold) for good/bad counts and wording, muted for
	// secondary prose.
	styleAccent, styleGood, styleBad, styleMuted = accent, successBold, dangerBold, muted

	// A breathing ramp from muted (dim) to accent (bright): the heartbeat dot never
	// vanishes but gently pulses. Both ends come from the resolved palette, so it
	// adapts to the terminal background like everything else.
	const pulseSteps = 12
	stylePulse = make([]lipgloss.Style, pulseSteps)
	for i := range stylePulse {
		t := float64(i) / float64(pulseSteps-1)
		stylePulse[i] = fg(lerpColor(p.muted, p.accent, t))
	}
}

// lerpColor blends a→b in RGB by t in [0,1], returning a hex color. The palette
// colors are opaque, so the alpha channel is ignored.
func lerpColor(a, b color.Color, t float64) color.Color {
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	mix := func(x, y uint32) uint8 {
		return uint8(float64(x>>8)*(1-t) + float64(y>>8)*t + 0.5)
	}
	return lipgloss.Color(fmt.Sprintf("#%02X%02X%02X", mix(ar, br), mix(ag, bg), mix(ab, bb)))
}
