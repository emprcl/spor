package tui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/emprcl/spor/internal/core"
	"github.com/emprcl/spor/internal/textfmt"
)

// The periodic commands and the async engine operations. Each mutation runs off
// the UI goroutine under the program's context and reports back as a message.

func reloadTick() tea.Cmd {
	return tea.Tick(reloadInterval, func(time.Time) tea.Msg { return reloadTickMsg{} })
}

func pulseTick() tea.Cmd {
	return tea.Tick(pulseInterval, func(time.Time) tea.Msg { return pulseTickMsg{} })
}

// opCmd runs a mutating operation off the UI goroutine and reports its one-line
// result (or error) as an opDoneMsg, which triggers a reload.
func opCmd(verb string, run func() (string, error)) tea.Cmd {
	return func() tea.Msg {
		msg, err := run()
		return opDoneMsg{verb: verb, msg: msg, err: err}
	}
}

// snapCmd records one snapshot by hand (the s key, available while not
// watching). It reports indexing progress the same way the watcher's first
// snapshot does, so snapping a big project shows the progress panel.
func (m *model) snapCmd() tea.Cmd {
	ctx, eng, send := m.ctx, m.eng, m.send
	return func() tea.Msg {
		fwd := &indexForwarder{send: send, start: time.Now()}
		res, err := eng.Snap(ctx, core.SnapOptions{OnProgress: fwd.update})
		fwd.done()
		if err != nil {
			return opDoneMsg{verb: "snap", err: err}
		}
		if !res.Created {
			return opDoneMsg{verb: "snap", msg: "nothing to snap"}
		}
		return opDoneMsg{verb: "snap", msg: "snapshot " + textfmt.Abbrev(res.StateID)}
	}
}

func (m *model) undoCmd() tea.Cmd {
	ctx, eng := m.ctx, m.eng
	return opCmd("undo", func() (string, error) {
		res, err := eng.Undo(ctx, 1)
		if err != nil {
			return "", err
		}
		if res.Steps == 0 {
			return "already at the oldest snapshot", nil
		}
		return "undid to " + textfmt.Abbrev(res.StateID), nil
	})
}

func (m *model) redoCmd() tea.Cmd {
	ctx, eng := m.ctx, m.eng
	return opCmd("redo", func() (string, error) {
		res, err := eng.Redo(ctx, 1)
		if err != nil {
			return "", err
		}
		if res.Steps == 0 {
			return "nothing to redo", nil
		}
		return "redid to " + textfmt.Abbrev(res.StateID), nil
	})
}

// loadDiffCmd loads the changes from one state to another (the TUI diffs a
// snapshot against its parent: "what did this snapshot change").
func (m *model) loadDiffCmd(from, to string) tea.Cmd {
	ctx, eng := m.ctx, m.eng
	return func() tea.Msg {
		res, err := eng.Diff(ctx, from, to)
		return diffLoadedMsg{res: res, err: err}
	}
}

// loadPickCmd fetches the pickable paths of ref's snapshot, then opens the pick
// overlay via pickReadyMsg.
func (m *model) loadPickCmd(ref string) tea.Cmd {
	ctx, eng := m.ctx, m.eng
	return func() tea.Msg {
		paths, err := eng.Files(ctx, ref)
		return pickReadyMsg{ref: ref, paths: paths, err: err}
	}
}

func (m *model) dropPlanCmd(ref string) tea.Cmd {
	ctx, eng := m.ctx, m.eng
	return func() tea.Msg {
		p, err := eng.DropPlan(ctx, ref)
		if err != nil {
			return opDoneMsg{verb: "drop", err: err}
		}
		prompt := fmt.Sprintf("drop %s and %d %s below it? this cannot be undone.",
			textfmt.Abbrev(p.Target), p.StatesToDelete-1, textfmt.Plural(p.StatesToDelete-1, "snapshot", "snapshots"))
		switch {
		case p.WipesEntireStore:
			prompt = "drop the entire history? this cannot be undone."
		case p.StatesToDelete == 1:
			prompt = fmt.Sprintf("drop snapshot %s? this cannot be undone.", textfmt.Abbrev(p.Target))
		}
		return confirmMsg{action: "drop", ref: ref, prompt: prompt, danger: true}
	}
}

func (m *model) trimPlanCmd(ref string) tea.Cmd {
	ctx, eng := m.ctx, m.eng
	return func() tea.Msg {
		p, err := eng.TrimPlan(ctx, ref)
		if err != nil {
			return opDoneMsg{verb: "trim", err: err}
		}
		if p.IsNoop {
			return opDoneMsg{verb: "trim", msg: "already the root of the history"}
		}
		prompt := fmt.Sprintf("make %s the new root, dropping %d %s outside it? this cannot be undone.",
			textfmt.Abbrev(p.Target), p.StatesToDrop, textfmt.Plural(p.StatesToDrop, "snapshot", "snapshots"))
		return confirmMsg{action: "trim", ref: ref, prompt: prompt, danger: true}
	}
}

// foldPlanCmd plans folding a hidden run (from its oldest state up to its
// anchor) into one snapshot, then asks for confirmation.
func (m *model) foldPlanCmd(from, to string) tea.Cmd {
	ctx, eng := m.ctx, m.eng
	return func() tea.Msg {
		p, err := eng.FoldPlan(ctx, from, to)
		if err != nil {
			return opDoneMsg{verb: "fold", err: err}
		}
		prompt := fmt.Sprintf("fold %d %s into one? this cannot be undone.",
			p.StatesFolded, textfmt.Plural(p.StatesFolded, "snapshot", "snapshots"))
		return confirmMsg{action: "fold", ref: from, ref2: to, prompt: prompt, danger: true}
	}
}

// thinPlanCmd plans reducing the whole history to its tips, branch points, and
// labels, then asks for confirmation.
func (m *model) thinPlanCmd() tea.Cmd {
	ctx, eng := m.ctx, m.eng
	return func() tea.Msg {
		p, err := eng.ThinPlan(ctx)
		if err != nil {
			return opDoneMsg{verb: "thin", err: err}
		}
		if p.IsNoop {
			return opDoneMsg{verb: "thin", msg: "nothing to thin: only tips, branch points, and labels remain"}
		}
		prompt := fmt.Sprintf("thin the history to its tips, branch points, and labels, dropping %d %s? this cannot be undone.",
			p.StatesToDrop, textfmt.Plural(p.StatesToDrop, "snapshot", "snapshots"))
		return confirmMsg{action: "thin", prompt: prompt, danger: true}
	}
}
