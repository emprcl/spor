package tui

import (
	"context"
	"errors"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/emprcl/spor/internal/lock"
	"github.com/emprcl/spor/internal/textfmt"
	"github.com/emprcl/spor/internal/view"
	"github.com/emprcl/spor/internal/watch"
)

func (m *model) Init() tea.Cmd {
	if m.autoWatch {
		m.autoWatch = false
		return tea.Batch(reloadTick(), m.startWatch())
	}
	return reloadTick()
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.help.SetWidth(m.width)
		m.diff.SetWidth(m.width)
		m.diff.SetHeight(m.diffBodyHeight())
		if m.mode == modePrompt {
			m.prompt.input.SetWidth(m.promptInputWidth())
		}
		m.clampScroll()
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.MouseWheelMsg:
		return m.handleWheel(msg)

	case reloadTickMsg:
		if m.mode == modeTree {
			m.maybeReload()
		}
		return m, reloadTick()

	case pulseTickMsg:
		// The indicator is hidden behind the full-screen diff, so the animation
		// pauses there; closing the diff resumes it (see resumePulse).
		if !m.watching || m.mode == modeDiff {
			m.pulsing = false
			return m, nil
		}
		m.pulseStep++
		return m, pulseTick()

	case flashExpireMsg:
		if msg.seq == m.flashSeq {
			m.flash = ""
		}
		return m, nil

	case watchEventMsg:
		switch msg.Kind {
		case watch.Settling:
			m.activity = "settling…"
		case watch.Created:
			m.activity, m.errMsg = "", ""
			m.reload()
		case watch.NoChange:
			m.activity = ""
		case watch.Error:
			if msg.Err != nil {
				m.errMsg = msg.Err.Error()
			}
		}
		return m, nil

	case watchStoppedMsg:
		m.stoppingWatch = false
		m.activity = ""
		return m, m.flashInfo("stopped watching")

	case reloadMsg:
		m.reload()
		return m, nil

	case indexProgressMsg:
		m.indexing = true
		m.indexPhase, m.indexDone, m.indexTotal = msg.phase, msg.done, msg.total
		return m, nil

	case indexDoneMsg:
		m.indexing = false
		return m, nil

	case opDoneMsg:
		var cmd tea.Cmd
		if msg.err != nil {
			cmd = m.flashError(msg.err.Error())
		} else if msg.msg != "" {
			cmd = m.flashInfo(msg.msg)
		}
		m.reload()
		return m, cmd

	case pickReadyMsg:
		if msg.err != nil {
			return m, m.flashError(msg.err.Error())
		}
		return m, m.openPick(msg.ref, msg.paths)

	case confirmMsg:
		m.confirm = confirmState(msg)
		m.mode = modeConfirm
		return m, nil

	case diffLoadedMsg:
		if msg.err != nil {
			m.mode = modeTree
			return m, m.flashError(msg.err.Error())
		}
		m.diff.SetWidth(m.width)
		m.diff.SetHeight(m.diffBodyHeight())
		// A one-column left margin so the diff doesn't sit flush against the
		// terminal edge.
		lines := view.DiffLines(&m.sty, msg.res)
		for i, ln := range lines {
			lines[i] = " " + ln
		}
		m.diff.SetContentLines(lines)
		m.diff.GotoTop()
		m.mode = modeDiff
		return m, nil

	case fatalMsg:
		m.err = msg.err
		return m, tea.Quit
	}

	// Anything else (cursor blink ticks and the like) belongs to the focused
	// prompt input while it is up.
	if m.mode == modePrompt {
		var cmd tea.Cmd
		m.prompt.input, cmd = m.prompt.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

// handleKey routes a keypress to the active overlay, or to the tree.
func (m *model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeDiff:
		return m.handleDiffKey(msg)
	case modeConfirm:
		return m.handleConfirmKey(msg)
	case modePrompt:
		return m.handlePromptKey(msg)
	case modeHelp:
		m.mode = modeTree
		return m, nil
	}
	return m.handleTreeKey(msg)
}

// handleTreeKey handles navigation and actions in the main tree view.
func (m *model) handleTreeKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := m.keys
	switch {
	case key.Matches(msg, k.Quit):
		prompt := "quit spor ui?"
		if m.watching {
			prompt = "quit spor ui? watching stops with it."
		}
		m.confirm = confirmState{action: "quit", prompt: prompt}
		m.mode = modeConfirm
		return m, nil
	case key.Matches(msg, k.Up):
		m.moveCursor(-1)
		return m, nil
	case key.Matches(msg, k.Down):
		m.moveCursor(1)
		return m, nil
	case key.Matches(msg, k.Top):
		m.cursor = 0
		m.clampScroll()
		return m, nil
	case key.Matches(msg, k.Bottom):
		m.cursor = len(m.selectable) - 1
		m.clampScroll()
		return m, nil
	case key.Matches(msg, k.Expand):
		m.setFold(true)
		return m, nil
	case key.Matches(msg, k.Collapse):
		m.setFold(false)
		return m, nil
	case key.Matches(msg, k.Help):
		m.mode = modeHelp
		return m, nil
	case key.Matches(msg, k.Watch):
		return m, m.toggleWatch()
	case key.Matches(msg, k.Snap):
		return m, m.snapCmd()
	case key.Matches(msg, k.Undo):
		return m, m.undoCmd()
	case key.Matches(msg, k.Redo):
		return m, m.redoCmd()
	case key.Matches(msg, k.Thin):
		return m, m.thinPlanCmd()
	}

	// Actions on a hidden-run summary row.
	if r := m.selectedRow(); r != nil && r.IsFold && key.Matches(msg, k.Squash) {
		return m, m.foldPlanCmd(r.FoldOldest, r.ID)
	}

	// Actions on the selected snapshot.
	s := m.selectedState()
	if s == nil {
		return m, nil
	}
	switch {
	case key.Matches(msg, k.Go):
		if s.ID == m.log.Head {
			return m, m.flashInfo("already on this snapshot")
		}
		return m, func() tea.Msg {
			return confirmMsg{
				action: "go",
				ref:    s.ID,
				prompt: "jump to " + textfmt.Abbrev(s.ID) + "?",
			}
		}
	case key.Matches(msg, k.Diff):
		if s.Parent == "" {
			return m, m.flashInfo("root snapshot, nothing before it")
		}
		return m, m.loadDiffCmd(s.Parent, s.ID)
	case key.Matches(msg, k.Label):
		return m, m.openPrompt("label", s.ID, "label: ", s.Label)
	case key.Matches(msg, k.Pick):
		return m, m.loadPickCmd(s.ID)
	case key.Matches(msg, k.Drop):
		return m, m.dropPlanCmd(s.ID)
	case key.Matches(msg, k.Trim):
		return m, m.trimPlanCmd(s.ID)
	}
	return m, nil
}

// handleWheel scrolls with the mouse: the tree moves its selection, the diff
// overlay scrolls its viewport (which also handles the wheel natively).
func (m *model) handleWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeTree:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.moveCursor(-1)
		case tea.MouseWheelDown:
			m.moveCursor(1)
		}
	case modeDiff:
		var cmd tea.Cmd
		m.diff, cmd = m.diff.Update(msg)
		return m, cmd
	}
	return m, nil
}

// setFold expands or collapses the hidden run the selected row relates to: a
// summary row expands (open), and any row of an expanded run collapses it
// (!open). The cursor stays on the run's anchor across the relayout, so expand
// and collapse land on the same line.
func (m *model) setFold(open bool) {
	r := m.selectedRow()
	if r == nil {
		return
	}
	var anchor string
	switch {
	case open && r.IsFold:
		anchor = r.ID
	case !open && r.FoldAnchor != "":
		anchor = r.FoldAnchor
	default:
		return
	}
	if open {
		m.expanded[anchor] = true
	} else {
		delete(m.expanded, anchor)
	}
	m.rebuild()
	m.selectID(anchor)
	m.clampScroll()
}

// moveCursor steps the selection by delta and keeps it on screen.
func (m *model) moveCursor(delta int) {
	if len(m.selectable) == 0 {
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.selectable) {
		m.cursor = len(m.selectable) - 1
	}
	m.clampScroll()
}

// ---- the watch toggle -----------------------------------------------------

// toggleWatch flips recording on or off (the w key).
func (m *model) toggleWatch() tea.Cmd {
	if m.stoppingWatch {
		return nil // the previous watcher is still unwinding
	}
	if m.watching {
		return m.stopWatch()
	}
	return m.startWatch()
}

// startWatch acquires the watcher lock and starts the watcher goroutine,
// forwarding its events into the program. The initial snapshot the watcher
// takes reports its progress, so recording a big project shows the indexing
// panel instead of a blank wait.
func (m *model) startWatch() tea.Cmd {
	if m.watching || m.stoppingWatch {
		return nil
	}
	wl, err := m.eng.AcquireWatcher()
	if err != nil {
		if errors.Is(err, lock.ErrWatcherRunning) {
			return m.flashError("another process is watching this project")
		}
		return m.flashError(err.Error())
	}
	wctx, cancel := context.WithCancel(m.ctx)
	m.wlock, m.watchCancel = wl, cancel
	m.watching = true
	m.keys.Snap.SetEnabled(false)
	m.activity = ""

	eng, root, send := m.eng, m.root, m.send
	m.watchWG.Add(1)
	go func() {
		defer m.watchWG.Done()
		runWatcher(wctx, eng, root, send)
	}()
	return m.resumePulse()
}

// stopWatch stops the watcher asynchronously: recording ends now, and the lock
// is released once the in-flight snapshot (if any) unwinds.
func (m *model) stopWatch() tea.Cmd {
	if !m.watching {
		return nil
	}
	m.watching = false
	m.stoppingWatch = true
	m.keys.Snap.SetEnabled(true)
	m.activity = "stopping watcher…"

	cancel, wl, wg := m.watchCancel, m.wlock, &m.watchWG
	m.watchCancel, m.wlock = nil, nil
	return func() tea.Msg {
		cancel()
		wg.Wait()
		_ = wl.Release()
		return watchStoppedMsg{}
	}
}

// resumePulse restarts the indicator tick after it lapsed (watch just started,
// or the diff overlay hid it). The pulsing flag keeps the loop single.
func (m *model) resumePulse() tea.Cmd {
	if !m.watching || m.pulsing {
		return nil
	}
	m.pulsing = true
	return pulseTick()
}
