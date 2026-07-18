package tui

import (
	"context"
	"sync"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/emprcl/spor/internal/core"
	"github.com/emprcl/spor/internal/lock"
	"github.com/emprcl/spor/internal/view"
	"github.com/emprcl/spor/internal/watch"
)

// tuiMode is the active input layer: the tree, or an overlay that captures keys.
type tuiMode int

const (
	modeTree tuiMode = iota
	modeDiff
	modeConfirm
	modePrompt
	modeHelp
)

// flashLevel classifies the transient status-bar message so the view picks the
// right style without sniffing the text.
type flashLevel int

const (
	flashGood flashLevel = iota
	flashBad
)

// model is the whole TUI state. It reads through the shared engine (concurrent
// reads are safe under WAL) and drives mutations as async commands so a slow
// materialize never freezes the view.
type model struct {
	cfg  Config
	eng  *core.Engine
	root string
	// ctx is the program's lifetime; every engine command runs under it, so
	// quitting cancels in-flight work before Run's caller closes the engine.
	ctx context.Context
	sty view.Theme
	// send forwards a message into the running program from outside the update
	// loop (the watcher goroutine, snap progress). Set by Run.
	send func(tea.Msg)
	// buildRows lays the history out (view.LogRows under the injected theme);
	// a hook so tests can supply a canned tree.
	buildRows func(res core.LogResult, expanded map[string]bool) []view.LogRow

	width, height int
	mode          tuiMode

	keys keyMap
	help help.Model

	// The watcher, when this process is recording: the model owns its lock and
	// lifetime (the w toggle). stoppingWatch guards the async stop so the
	// toggle cannot double-start while the old watcher unwinds.
	watching      bool
	wlock         *lock.Watcher
	watchCancel   context.CancelFunc
	watchWG       sync.WaitGroup
	stoppingWatch bool

	// Indexing progress for a snapshot in flight. When a fresh or large project
	// is first recorded, the initial index can take a while; indexing is true
	// and the panel is drawn until that snapshot completes. indexPhase picks
	// the caption (scanning, indexing, syncing, saving).
	indexing   bool
	indexPhase core.SnapPhase
	indexDone  int
	indexTotal int
	progress   progress.Model

	log        core.LogResult
	status     core.StatusResult
	byID       map[string]core.StateInfo
	childCount map[string]int

	rows       []view.LogRow
	selectable []int // indices into rows the cursor can land on
	expanded   map[string]bool
	cursor     int // index into selectable
	top        int // first visible row (scroll offset)

	// dataVersion is the store's SQLite data_version at the last reload; the 1s
	// tick reloads only when the probe sees it move (an out-of-band mutation).
	// newestAt and lastLayout drive the relative-time refresh: rows are re-laid
	// out from the cached log (no reads) just often enough for the "2s ago"
	// column to keep counting.
	dataVersion int64
	newestAt    time.Time
	lastLayout  time.Time

	// autoWatch asks Init to start the watcher on the first frame (the --watch
	// flag), skipping the startup offer.
	autoWatch bool

	activity string
	errMsg   string

	flash    string
	flashLvl flashLevel
	flashSeq int // generation guard so a stale expiry never clears a newer flash

	diff    viewport.Model
	confirm confirmState
	prompt  promptState

	// pulseStep advances once per pulse tick; pulsing tracks whether a pulse tick
	// is in flight so the loop is never doubled when the diff overlay closes.
	pulseStep int
	pulsing   bool

	err error
}

// Messages.
type (
	reloadTickMsg   struct{}
	pulseTickMsg    struct{}
	flashExpireMsg  struct{ seq int }
	watchEventMsg   watch.Event
	watchStoppedMsg struct{}
	reloadMsg       struct{}
	// indexProgressMsg reports a running snapshot's progress in its current
	// phase; indexDoneMsg marks that snapshot complete.
	indexProgressMsg struct {
		phase       core.SnapPhase
		done, total int
	}
	indexDoneMsg struct{}
	opDoneMsg    struct {
		verb, msg string
		err       error
	}
	diffLoadedMsg struct {
		res core.DiffResult
		err error
	}
	// pickReadyMsg carries the pickable paths of the state a pick was requested
	// on, loaded async so a huge manifest never stalls a keypress.
	pickReadyMsg struct {
		ref   string
		paths []string
		err   error
	}
	confirmMsg struct {
		action, ref, ref2 string
		prompt            string
		danger            bool
	}
	fatalMsg struct{ err error }
)

// newModel builds the model and loads the initial history so the first frame is
// populated. When the watcher lock is free it opens on the watch offer
// (docs/design-spec.md §6), so recording is one keypress away but never implicit.
func newModel(ctx context.Context, cfg Config) *model {
	m := &model{
		cfg:      cfg,
		eng:      cfg.Engine,
		root:     cfg.Root,
		ctx:      ctx,
		expanded: map[string]bool{},
		keys:     newKeyMap(),
	}
	if cfg.Theme != nil {
		m.sty = *cfg.Theme
	}
	m.help = newHelp(m.sty)
	m.progress = newProgress(m.sty)
	m.diff = viewport.New()
	m.buildRows = func(res core.LogResult, expanded map[string]bool) []view.LogRow {
		return view.LogRows(&m.sty, res, expanded)
	}
	m.reload()

	running := false
	if r, err := m.eng.WatcherRunning(); err == nil {
		running = r
	}
	switch {
	case running:
		// Another process holds the watcher; browse only, whatever was asked.
		m.activity = "another process is watching this project"
	case cfg.ForceWatch:
		// --watch: start recording on the first frame, no offer.
		m.autoWatch = true
	case cfg.ForceBrowse:
		// --browse: open straight into browse mode, no offer.
	default:
		m.confirm = confirmState{
			action: "watch",
			prompt: "watch this project and record changes automatically?",
		}
		m.mode = modeConfirm
	}
	return m
}

// newProgress builds the indexing progress bar, blended from the muted to the
// accent tone so it reads like the rest of the palette (and the heartbeat ramp).
// The numeric percentage is hidden; the counts under the bar carry the detail.
func newProgress(sty view.Theme) progress.Model {
	return progress.New(
		progress.WithColors(sty.Muted.GetForeground(), sty.Accent.GetForeground()),
		progress.WithoutPercentage(),
	)
}

// newHelp builds the help renderer, styled from the injected theme so the bottom
// bar and the ? overlay match the rest of the TUI.
func newHelp(sty view.Theme) help.Model {
	h := help.New()
	h.ShortSeparator = " · "
	h.Styles.ShortKey = sty.Accent
	h.Styles.ShortDesc = sty.Muted
	h.Styles.ShortSeparator = sty.Muted
	h.Styles.Ellipsis = sty.Muted
	h.Styles.FullKey = sty.Accent
	h.Styles.FullDesc = sty.Muted
	h.Styles.FullSeparator = sty.Muted
	return h
}

// reload re-reads the history and status and rebuilds the tree, preserving the
// selection by state id across the rebuild. A selection sitting on the first
// row stays on the first row instead, so a cursor left at the top keeps
// tracking the newest snapshot as the watcher records. It is a pure read,
// cheap enough to run inline on the UI goroutine.
func (m *model) reload() {
	wasFirst := m.cursor == 0
	selID := m.selectedID()

	// Read the change counter before the data: a commit landing between the
	// two bumps it relative to what we store, so the next probe catches it.
	version, versionErr := m.eng.DataVersion(m.ctx)

	log, err := m.eng.Log(m.ctx)
	if err != nil {
		m.errMsg = err.Error()
		return
	}
	status, err := m.eng.Status(m.ctx)
	if err != nil {
		m.errMsg = err.Error()
		return
	}
	m.log, m.status = log, status
	if versionErr == nil {
		m.dataVersion = version
	}

	m.byID = make(map[string]core.StateInfo, len(log.States))
	m.childCount = make(map[string]int, len(log.States))
	m.newestAt = time.Time{}
	for _, s := range log.States {
		m.byID[s.ID] = s
		if s.CreatedAt.After(m.newestAt) {
			m.newestAt = s.CreatedAt
		}
	}
	for _, s := range log.States {
		if _, ok := m.byID[s.Parent]; ok {
			m.childCount[s.Parent]++
		}
	}

	m.rebuild()
	m.selectID(selID)
	if wasFirst {
		m.cursor = 0
	}
	m.clampScroll()
}

// maybeReload is the 1s tick's work: a near-free change probe, a full reload
// only when the store actually changed (or the probe failed), and otherwise
// just enough re-layout to keep the relative-time column honest.
func (m *model) maybeReload() {
	version, err := m.eng.DataVersion(m.ctx)
	if err != nil || version != m.dataVersion {
		m.reload()
		return
	}
	m.refreshTimes()
}

// refreshTimes re-lays-out the cached history so "2s ago" keeps counting:
// every second while the newest snapshot is under a minute old (the column
// shows seconds there), once a minute after that. No engine reads happen.
func (m *model) refreshTimes() {
	interval := time.Minute
	if time.Since(m.newestAt) < time.Minute {
		interval = time.Second
	}
	if time.Since(m.lastLayout) < interval {
		return
	}
	m.rebuild()
}

// rebuild recomputes rows/selectable from the current log and expanded set.
func (m *model) rebuild() {
	m.lastLayout = time.Now()
	m.rows = m.buildRows(m.log, m.expanded)
	m.selectable = m.selectable[:0]
	for i, r := range m.rows {
		if r.IsNode || r.IsFold {
			m.selectable = append(m.selectable, i)
		}
	}
	if m.cursor >= len(m.selectable) {
		m.cursor = len(m.selectable) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// selectedRow returns the row under the cursor, or nil when the history is empty.
func (m *model) selectedRow() *view.LogRow {
	if m.cursor < 0 || m.cursor >= len(m.selectable) {
		return nil
	}
	return &m.rows[m.selectable[m.cursor]]
}

func (m *model) selectedID() string {
	if r := m.selectedRow(); r != nil {
		return r.ID
	}
	return ""
}

// selectedState returns the selected state's info, or nil on a hidden-run row or
// empty history.
func (m *model) selectedState() *core.StateInfo {
	r := m.selectedRow()
	if r == nil || !r.IsNode {
		return nil
	}
	s, ok := m.byID[r.ID]
	if !ok {
		return nil
	}
	return &s
}

// selectID moves the cursor to the row with the given id, if it still exists.
func (m *model) selectID(id string) {
	if id == "" {
		return
	}
	for i, li := range m.selectable {
		if m.rows[li].ID == id {
			m.cursor = i
			return
		}
	}
}

// clampScroll adjusts the scroll offset so the selected row stays within the tree
// body's visible window.
func (m *model) clampScroll() {
	body := m.treeBodyHeight()
	if body <= 0 || len(m.rows) == 0 {
		m.top = 0
		return
	}
	sel := 0
	if m.selectedRow() != nil {
		sel = m.selectable[m.cursor]
	}
	if sel < m.top {
		m.top = sel
	}
	if sel >= m.top+body {
		m.top = sel - body + 1
	}
	if maxTop := len(m.rows) - body; m.top > maxTop {
		m.top = maxTop
	}
	if m.top < 0 {
		m.top = 0
	}
}

// setFlash shows a transient status-bar note and returns the command that clears
// it. The sequence number ties each expiry to the flash that scheduled it, so an
// older timer firing late never wipes a newer message.
func (m *model) setFlash(s string, lvl flashLevel, d time.Duration) tea.Cmd {
	m.flash, m.flashLvl = s, lvl
	m.flashSeq++
	seq := m.flashSeq
	return tea.Tick(d, func(time.Time) tea.Msg { return flashExpireMsg{seq: seq} })
}

func (m *model) flashInfo(s string) tea.Cmd { return m.setFlash(s, flashGood, 4*time.Second) }
func (m *model) flashError(s string) tea.Cmd {
	return m.setFlash("✗ "+s, flashBad, 6*time.Second)
}
