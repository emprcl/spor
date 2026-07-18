package tui

import "charm.land/bubbles/v2/key"

// keyMap is the single source of truth for the TUI's bindings: Update matches
// against it with key.Matches, and the help bubble renders it, so the keys shown
// and the keys handled can never drift apart.
type keyMap struct {
	// Tree navigation.
	Up       key.Binding
	Down     key.Binding
	Top      key.Binding
	Bottom   key.Binding
	Expand   key.Binding
	Collapse key.Binding

	// Actions on the selected snapshot (Squash acts on a hidden-run row).
	Go     key.Binding
	Diff   key.Binding
	Label  key.Binding
	Pick   key.Binding
	Drop   key.Binding
	Trim   key.Binding
	Squash key.Binding

	// Global actions. Snap is disabled while watching (the watcher records).
	Snap  key.Binding
	Watch key.Binding
	Undo  key.Binding
	Redo  key.Binding
	Thin  key.Binding
	Help  key.Binding
	Quit  key.Binding

	// Move aggregates Up and Down for the bottom bar, where one compact "↑/↓ move"
	// entry reads better than two. Display only; matching uses Up/Down.
	Move key.Binding

	// Confirm overlay.
	Confirm key.Binding
	Deny    key.Binding

	// Prompt overlay. SuggestUp/SuggestDown move the pick overlay's highlight;
	// they bind only the arrow keys, since letters (j/k) must keep typing into
	// the search input.
	Submit      key.Binding
	Cancel      key.Binding
	SuggestUp   key.Binding
	SuggestDown key.Binding

	// Diff overlay. Scroll and Page are display-only stand-ins for the viewport's
	// own keymap, which does the actual scrolling.
	Scroll key.Binding
	Page   key.Binding
	Close  key.Binding

	// Help overlay.
	AnyKey key.Binding
}

func newKeyMap() keyMap {
	up := []string{"up", "k"}
	down := []string{"down", "j"}
	return keyMap{
		Up:          key.NewBinding(key.WithKeys(up...), key.WithHelp("↑/k", "up")),
		Down:        key.NewBinding(key.WithKeys(down...), key.WithHelp("↓/j", "down")),
		Top:         key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g", "top")),
		Bottom:      key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G", "bottom")),
		Expand:      key.NewBinding(key.WithKeys("right"), key.WithHelp("→", "expand run")),
		Collapse:    key.NewBinding(key.WithKeys("left"), key.WithHelp("←", "collapse run")),
		Go:          key.NewBinding(key.WithKeys("enter", "@"), key.WithHelp("enter", "go here")),
		Diff:        key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "diff vs parent")),
		Label:       key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "label")),
		Pick:        key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pick")),
		Drop:        key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "drop")),
		Trim:        key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "trim")),
		Squash:      key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "fold")),
		Snap:        key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "snap")),
		Watch:       key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "watch on/off")),
		Undo:        key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "undo")),
		Redo:        key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "redo")),
		Thin:        key.NewBinding(key.WithKeys("T"), key.WithHelp("T", "thin")),
		Help:        key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:        key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		Move:        key.NewBinding(key.WithKeys(append(up, down...)...), key.WithHelp("↑/↓", "move")),
		Confirm:     key.NewBinding(key.WithKeys("y", "Y"), key.WithHelp("y", "confirm")),
		Deny:        key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "cancel")),
		Submit:      key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "submit")),
		Cancel:      key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		SuggestUp:   key.NewBinding(key.WithKeys("up"), key.WithHelp("↑/↓", "choose")),
		SuggestDown: key.NewBinding(key.WithKeys("down")),
		Scroll:      key.NewBinding(key.WithKeys("up", "down", "k", "j"), key.WithHelp("↑/↓", "scroll")),
		Page:        key.NewBinding(key.WithKeys("space", "pgdown", "b", "pgup"), key.WithHelp("space", "page")),
		Close:       key.NewBinding(key.WithKeys("esc", "q", "d"), key.WithHelp("esc", "close")),
		AnyKey:      key.NewBinding(key.WithKeys("any"), key.WithHelp("any key", "close")),
	}
}

// shortHelp returns the bindings the bottom bar shows in the given mode.
func (k keyMap) shortHelp(mode tuiMode) []key.Binding {
	switch mode {
	case modeConfirm:
		return []key.Binding{k.Confirm, k.Deny}
	case modePrompt:
		return []key.Binding{k.Submit, k.Cancel}
	case modeDiff:
		return []key.Binding{k.Scroll, k.Page, k.Close}
	case modeHelp:
		return []key.Binding{k.AnyKey}
	default:
		// The tree bar stays uncluttered: only the essentials, with the full list a
		// keypress away under ? (see FullHelp).
		return []key.Binding{k.Move, k.Help, k.Quit}
	}
}

// ShortHelp implements help.KeyMap with the tree bar's bindings.
func (k keyMap) ShortHelp() []key.Binding { return k.shortHelp(modeTree) }

// FullHelp implements help.KeyMap: the ? overlay's columns, grouped as
// navigation, actions on the selected snapshot, and global keys.
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Top, k.Bottom, k.Expand, k.Collapse},
		{k.Go, k.Diff, k.Label, k.Pick, k.Drop, k.Trim, k.Squash},
		{k.Snap, k.Watch, k.Undo, k.Redo, k.Thin, k.Help, k.Quit},
	}
}
