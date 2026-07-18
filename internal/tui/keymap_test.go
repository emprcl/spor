package tui

import (
	"github.com/emprcl/spor/internal/view"
	"strings"
	"testing"
)

// TestHelpBar locks in the wiring from the keymap to the bottom help bar: each mode
// shows its own keys, rendered through charm.land/bubbles/v2/help.
func TestHelpBar(t *testing.T) {
	// The zero Styles render plain text.
	m := &model{keys: newKeyMap(), help: newHelp(view.Theme{}), width: 200}
	m.help.SetWidth(200)

	cases := map[tuiMode]string{
		modeTree:    "↑/↓ move · ? help · q quit",
		modeConfirm: "y confirm · n cancel",
		modePrompt:  "enter submit · esc cancel",
		modeDiff:    "↑/↓ scroll · space page · esc close",
		modeHelp:    "any key close",
	}
	for mode, want := range cases {
		m.mode = mode
		// The bar carries a two-column left margin.
		want = "  " + want
		if got := strings.TrimRight(m.viewHelpBar(), " "); got != want {
			t.Errorf("mode %d help bar = %q, want %q", mode, got, want)
		}
	}
}
