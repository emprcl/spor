package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/emprcl/spor/internal/textfmt"
)

// The overlays that sit above the tree: the scrollable diff, the confirmation
// (watch offer, go, and the destructive ops), and the text prompts (label, and
// the searchable pick).

// ---- diff overlay ---------------------------------------------------------

// The diff overlay is m.diff, a bubbles viewport holding the shared renderer's
// lines (they carry their own color; the program downsamples them to the
// terminal). The lines already open with a "from -> to" header, so the overlay
// adds no title.

// viewDiff draws the diff overlay full-screen: the viewport, then a footer,
// both behind the same one-column left margin as the content lines.
func (m *model) viewDiff() string {
	footer := fitLine(" "+m.help.ShortHelpView(m.keys.shortHelp(modeDiff)), m.width)
	return m.diff.View() + "\n" + footer
}

// handleDiffKey closes the diff overlay or hands the key to the viewport, whose
// own keymap covers line, half-page, and full-page scrolling.
func (m *model) handleDiffKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Close):
		m.mode = modeTree
		return m, m.resumePulse()
	case key.Matches(msg, m.keys.Top):
		m.diff.GotoTop()
		return m, nil
	case key.Matches(msg, m.keys.Bottom):
		m.diff.GotoBottom()
		return m, nil
	}
	var cmd tea.Cmd
	m.diff, cmd = m.diff.Update(msg)
	return m, cmd
}

// ---- confirmation overlay -------------------------------------------------

// confirmState is a pending yes/no: the startup watch offer, a go jump, quit,
// or a destructive op (drop, trim, fold, thin). danger picks the alarming
// styling.
type confirmState struct {
	action, ref, ref2 string
	prompt            string
	danger            bool
}

// viewConfirm draws the confirmation box centered in the body.
func (m *model) viewConfirm(bodyH int) string {
	choices := m.sty.Accent.Render("y") + m.sty.Muted.Render(" yes    ") +
		m.sty.Accent.Render("n") + m.sty.Muted.Render(" no")
	prompt := m.sty.ID.Render(m.confirm.prompt)
	if m.confirm.danger {
		prompt = m.sty.Bad.Render(m.confirm.prompt)
	}
	return m.centeredBox(bodyH, "confirm", prompt+"\n\n"+choices)
}

// handleConfirmKey resolves the confirmation: y runs the action, anything else
// cancels.
func (m *model) handleConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	c := m.confirm
	m.confirm = confirmState{}
	m.mode = modeTree
	if !key.Matches(msg, m.keys.Confirm) {
		return m, nil
	}
	ctx, eng := m.ctx, m.eng
	switch c.action {
	case "quit":
		return m, tea.Quit
	case "watch":
		return m, m.startWatch()
	case "go":
		return m, opCmd("go", func() (string, error) {
			res, err := eng.Go(ctx, c.ref)
			if err != nil {
				return "", err
			}
			return "went to " + textfmt.Abbrev(res.StateID), nil
		})
	case "drop":
		return m, opCmd("drop", func() (string, error) {
			res, err := eng.Drop(ctx, c.ref)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("dropped %s (%d %s)", textfmt.Abbrev(c.ref), res.Deleted,
				textfmt.Plural(res.Deleted, "snapshot", "snapshots")), nil
		})
	case "trim":
		return m, opCmd("trim", func() (string, error) {
			res, err := eng.Trim(ctx, c.ref)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("trimmed to %s (dropped %d %s)", textfmt.Abbrev(c.ref), res.Dropped,
				textfmt.Plural(res.Dropped, "snapshot", "snapshots")), nil
		})
	case "fold":
		return m, opCmd("fold", func() (string, error) {
			res, err := eng.Fold(ctx, c.ref, c.ref2)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("folded %d %s into %s", res.Dropped,
				textfmt.Plural(res.Dropped, "snapshot", "snapshots"), textfmt.Abbrev(res.Folded)), nil
		})
	case "thin":
		return m, opCmd("thin", func() (string, error) {
			res, err := eng.Thin(ctx)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("thinned the history (dropped %d %s)", res.Dropped,
				textfmt.Plural(res.Dropped, "snapshot", "snapshots")), nil
		})
	}
	return m, nil
}

// ---- text prompt overlay --------------------------------------------------

// pickShown is how many path suggestions the pick overlay lists at once.
const pickShown = 8

// promptState is a single-line text entry for label and pick; the editing itself
// is a bubbles textinput, so cursor movement, word ops, and paste come for free.
// For pick, candidates holds the files of the target state's manifest and the
// input doubles as a live search over them.
type promptState struct {
	kind  string // "label" | "pick"
	ref   string
	title string
	prior string // label only: the label the state already had (empty submit removes it)
	input textinput.Model

	candidates []string // pick only: the manifest's files
	matches    []string // pick only: candidates matching the input
	sel        int      // pick only: highlighted match
}

// newPromptInput builds the styled text input every prompt overlay uses.
func (m *model) newPromptInput(label, initial string) textinput.Model {
	ti := textinput.New()
	ti.Prompt = label
	ti.SetStyles(textinput.Styles{
		Focused: textinput.StyleState{Text: m.sty.ID, Prompt: m.sty.StatusKey, Placeholder: m.sty.Muted, Suggestion: m.sty.Muted},
		Blurred: textinput.StyleState{Text: m.sty.ID, Prompt: m.sty.StatusKey, Placeholder: m.sty.Muted, Suggestion: m.sty.Muted},
		Cursor:  textinput.CursorStyle{Color: m.sty.Accent.GetForeground(), Blink: true},
	})
	ti.SetValue(initial)
	ti.CursorEnd()
	return ti
}

// openPrompt starts the label prompt on ref and returns the input's focus
// command (the cursor blink). Submitting an empty value removes an existing
// label.
func (m *model) openPrompt(kind, ref, label, initial string) tea.Cmd {
	ti := m.newPromptInput(label, initial)
	cmd := ti.Focus()
	m.prompt = promptState{kind: kind, ref: ref, title: "label snapshot " + textfmt.Abbrev(ref), prior: initial, input: ti}
	m.prompt.input.SetWidth(m.promptInputWidth())
	m.mode = modePrompt
	return cmd
}

// openPick starts the pick overlay on ref: a search input over the files of the
// state's manifest.
func (m *model) openPick(ref string, paths []string) tea.Cmd {
	ti := m.newPromptInput("search: ", "")
	cmd := ti.Focus()
	m.prompt = promptState{
		kind:       "pick",
		ref:        ref,
		title:      "pick from " + textfmt.Abbrev(ref),
		input:      ti,
		candidates: paths,
		matches:    paths,
	}
	m.prompt.input.SetWidth(m.promptInputWidth())
	m.mode = modePrompt
	return cmd
}

// promptInputWidth fits the input inside the centered box: its inner width minus
// the rendered prompt label.
func (m *model) promptInputWidth() int {
	w := m.boxWidth() - 6 - lipgloss.Width(m.prompt.input.Prompt)
	if w < 8 {
		return 8
	}
	return w
}

// refilter recomputes the pick matches for the current input (case-insensitive
// substring) and resets the highlight.
func (p *promptState) refilter() {
	q := strings.ToLower(strings.TrimSpace(p.input.Value()))
	if q == "" {
		p.matches = p.candidates
	} else {
		matches := make([]string, 0, len(p.matches))
		for _, c := range p.candidates {
			if strings.Contains(strings.ToLower(c), q) {
				matches = append(matches, c)
			}
		}
		p.matches = matches
	}
	p.sel = 0
}

// viewPrompt draws the input box centered in the body; the pick overlay adds
// the live-filtered file list under the search field.
func (m *model) viewPrompt(bodyH int) string {
	p := &m.prompt
	content := p.input.View()
	if p.candidates != nil {
		content += "\n\n" + m.viewPickMatches()
	}
	return m.centeredBox(bodyH, p.title, content)
}

// viewPickMatches renders the pick overlay's suggestion window: up to pickShown
// matches with the highlight marked, the window following the highlight, and a
// count of what lies beyond it.
func (m *model) viewPickMatches() string {
	p := &m.prompt
	if len(p.matches) == 0 {
		return m.sty.Muted.Render("no matching files")
	}
	start := 0
	if p.sel >= pickShown {
		start = p.sel - pickShown + 1
	}
	w := m.boxWidth() - 6 // the box's border and padding
	var lines []string
	for i := start; i < len(p.matches) && i < start+pickShown; i++ {
		if i == p.sel {
			lines = append(lines, fitLine(m.sty.Accent.Render("› ")+m.sty.ID.Render(p.matches[i]), w))
		} else {
			lines = append(lines, fitLine("  "+m.sty.Muted.Render(p.matches[i]), w))
		}
	}
	if rest := len(p.matches) - (start + pickShown); rest > 0 {
		lines = append(lines, m.sty.Muted.Render(fmt.Sprintf("  … %d more", rest)))
	}
	return strings.Join(lines, "\n")
}

// handlePromptKey submits or cancels the prompt, moves the pick highlight, and
// hands every other key to the text input (refiltering the pick matches when
// the text changed).
func (m *model) handlePromptKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := &m.prompt
	switch {
	case key.Matches(msg, m.keys.Cancel):
		m.mode = modeTree
		return m, nil
	case key.Matches(msg, m.keys.Submit):
		if p.candidates != nil {
			// Picking always takes a listed file; with nothing matched there is
			// nothing to pick.
			if p.sel >= len(p.matches) {
				return m, nil
			}
			value, ref := p.matches[p.sel], p.ref
			m.mode = modeTree
			return m, m.submitPick(ref, value)
		}
		kind, ref, prior, value := p.kind, p.ref, p.prior, p.input.Value()
		m.mode = modeTree
		return m, m.submitLabel(kind, ref, prior, value)
	case p.candidates != nil && key.Matches(msg, m.keys.SuggestUp):
		if p.sel > 0 {
			p.sel--
		}
		return m, nil
	case p.candidates != nil && key.Matches(msg, m.keys.SuggestDown):
		if p.sel < len(p.matches)-1 {
			p.sel++
		}
		return m, nil
	}
	before := p.input.Value()
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	if p.candidates != nil && p.input.Value() != before {
		p.refilter()
	}
	return m, cmd
}

// submitLabel runs the label action for a completed prompt: a value labels the
// state, an empty value removes the existing label (if any), and an empty value
// on an unlabeled state just cancels.
func (m *model) submitLabel(kind, ref, prior, value string) tea.Cmd {
	if kind != "label" {
		return nil
	}
	value = strings.TrimSpace(value)
	ctx, eng := m.ctx, m.eng
	switch {
	case value != "":
		return opCmd("label", func() (string, error) {
			res, err := eng.Label(ctx, ref, value)
			if err != nil {
				return "", err
			}
			return "labeled " + textfmt.Abbrev(res.StateID) + " " + value, nil
		})
	case prior != "":
		return opCmd("unlabel", func() (string, error) {
			res, err := eng.Unlabel(ctx, prior)
			if err != nil {
				return "", err
			}
			return "removed label " + prior + " from " + textfmt.Abbrev(res.StateID), nil
		})
	}
	return nil
}

// submitPick restores the chosen path out of ref's snapshot.
func (m *model) submitPick(ref, value string) tea.Cmd {
	ctx, eng := m.ctx, m.eng
	return opCmd("pick", func() (string, error) {
		res, err := eng.Pick(ctx, ref, value)
		if err != nil {
			return "", err
		}
		if !res.Created && res.Written == 0 {
			return value + " already matches", nil
		}
		return "picked " + value + " from " + textfmt.Abbrev(ref), nil
	})
}
