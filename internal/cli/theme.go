package cli

import (
	"github.com/emprcl/spor/internal/view"
)

// th is the active theme every command renders with. It starts as a zero theme
// (plain text, what tests exercise) and is resolved once by loadTheme, which
// the root command runs before any subcommand renders. The theme itself lives
// in internal/view, shared with the TUI (docs/design-spec.md §6, §8).
var th = &view.Theme{}

// loadTheme resolves the shared background-adaptive theme. Only the first call
// does the work, so it is cheap to call repeatedly.
func loadTheme() {
	th = view.Default()
}
