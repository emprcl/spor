package tui

import (
	"math"
	"strings"

	"github.com/emprcl/spor/internal/view"
)

// watchIndicator renders the animated "● watching" banner for the status bar.
// A bright band sweeps left to right across the dot and the label over the
// injected ramp (dim to bright), slipping off both ends before looping, so the
// watcher reads alive without a spinner. The dot swells as the band passes
// over it. step is the model's pulse counter (one increment per pulse tick),
// keeping View a pure function of the model.
func watchIndicator(sty view.Theme, step int) string {
	const text = "● watching"
	ramp := sty.Pulse
	if len(ramp) == 0 {
		return sty.WatchBanner.Render(text)
	}

	runes := []rune(text)
	// The band's half-width, and the sweep cycle: the center travels from
	// spread cells before the text to spread cells past it, one cell per tick.
	const spread = 3.0
	cycle := len(runes) + int(2*spread)
	center := float64(step%cycle) - spread

	// The resting tone sits mid-ramp so the band has headroom to brighten;
	// bold keeps the label reading as the banner it replaced.
	base := (len(ramp) - 1) / 2
	top := len(ramp) - 1

	var b strings.Builder
	for i, r := range runes {
		t := 1 - math.Abs(float64(i)-center)/spread
		if t < 0 {
			t = 0
		}
		glyph := string(r)
		if i == 0 {
			glyph = dotGlyph(t)
		}
		idx := base + int(t*float64(top-base)+0.5)
		b.WriteString(ramp[idx].Bold(true).Render(glyph))
	}
	return b.String()
}

// dotGlyph picks the heartbeat dot's size for the band's local intensity, so
// the dot visibly swells and settles as the band passes over it.
func dotGlyph(t float64) string {
	switch {
	case t > 0.66:
		return "·"
	case t > 0.25:
		return "•"
	default:
		return "●"
	}
}
