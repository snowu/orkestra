// Package ui is the bubbletea picker. It never touches stdout — bubbletea
// renders to stderr here (cd contract: stdout is reserved for the final
// cd-target line printed by main). It also never execs tmux attach itself:
// it returns a Result and lets main run the attach after the TUI has fully
// released the terminal.
package ui

import (
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"sort"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sahilm/fuzzy"

	"orkestra/internal/agentstate"
	"orkestra/internal/config"
	"orkestra/internal/tmux"
	"orkestra/internal/worktree"
)

type Action int

const (
	ActionQuit Action = iota
	ActionAttach
	ActionCD
	ActionNewTask
	ActionOpenAll // ctrl+a: attach base session with fe/be windows ensured
)

type Result struct {
	Action   Action
	Repo     string
	Task     string
	WtPath   string
	RepoRoot string // set for ActionNewTask
	// Set for ActionNewTask when the user picked a fe/be pair entry (or
	// hit ctrl-b on a paired repo): the sibling repo to create the same
	// task in, right after Repo's worktree.
	Repo2     string
	RepoRoot2 string
}

// Styles must bind to a stderr renderer: the ork() shell wrapper captures
// stdout with $(...), so lipgloss's default (stdout-probing) renderer sees
// a pipe and silently downgrades to no-color ASCII — the TUI actually
// draws on stderr.
var renderer = lipgloss.NewRenderer(os.Stderr)

// Colors are assigned by distinct-repo order (sorted), not hashed — the
// bash version hashed the name into the palette, which could land two
// repos on the same or near-identical color. Ordered assignment walks the
// palette instead, so no repeats until there are more repos than slots,
// and the palette itself is sequenced so neighbors are maximally distinct
// (hue jumps: blue, orange, green, magenta, yellow, cyan, red, purple...).
// Stable across runs as long as the set of repos on screen is stable.
var repoPalette = []int{39, 208, 84, 201, 220, 51, 196, 141, 154, 213, 43, 178, 99, 209, 48}

// Separate palette for task/session coloring so a task's color never
// coincides with the repo palette (rows already carry a repo color; the
// session color needs to read as a distinct signal, not a repeat).
var taskPalette = []int{135, 172, 65, 204, 227, 30, 168, 108, 216, 63}

// updateColors keeps color assignments STICKY, like ports: a name keeps
// its color for as long as it's on screen, no matter what appears or
// disappears around it (the old index-over-sorted-set scheme recolored
// everything whenever the set changed — e.g. ctrl-g creating a session).
// New names get a hash-seeded palette slot (stable-ish across restarts,
// same idea as TaskPorts), probing forward past colors already in use;
// a color is only reused once its holder leaves the screen.
func updateColors(prev map[string]lipgloss.Color, names []string, palette []int) map[string]lipgloss.Color {
	sort.Strings(names) // deterministic allocation order for new names
	out := make(map[string]lipgloss.Color, len(names))
	used := map[lipgloss.Color]int{}
	var fresh []string
	for _, n := range names {
		if c, ok := prev[n]; ok {
			out[n] = c
			used[c]++
		} else {
			fresh = append(fresh, n)
		}
	}
	for _, n := range fresh {
		h := fnv.New32a()
		h.Write([]byte(n))
		start := int(h.Sum32()) % len(palette)
		if start < 0 {
			start += len(palette)
		}
		best := start
		for i := 0; i < len(palette); i++ {
			s := (start + i) % len(palette)
			c := lipgloss.Color(fmt.Sprintf("%d", palette[s]))
			if used[c] == 0 {
				best = s
				break
			}
			if used[lipgloss.Color(fmt.Sprintf("%d", palette[best]))] > used[c] {
				best = s // palette exhausted: fall back to least-loaded slot
			}
		}
		c := lipgloss.Color(fmt.Sprintf("%d", palette[best]))
		out[n] = c
		used[c]++
	}
	return out
}

func (m *Model) updateRepoColors(rows []worktree.Row) {
	distinct := map[string]bool{}
	var names []string
	for _, r := range rows {
		if !distinct[r.Repo] {
			distinct[r.Repo] = true
			names = append(names, r.Repo)
		}
	}
	m.repoColors = updateColors(m.repoColors, names, repoPalette)
}

// updateTaskColors colors tasks that span 2+ rows — either a live session
// shared across worktrees, or both sides of a configured fe/be pair (which
// deserve the link color even before any session exists).
func (m *Model) updateTaskColors(rows []worktree.Row) {
	sessionRows := map[string]int{}
	repoTask := map[string]bool{}
	for _, r := range rows {
		if r.Session != "" {
			sessionRows[r.Session]++
		}
		repoTask[r.Repo+"/"+r.Task] = true
	}
	distinct := map[string]bool{}
	var names []string
	add := func(task string) {
		if !distinct[task] {
			distinct[task] = true
			names = append(names, task)
		}
	}
	for _, r := range rows {
		if r.Session != "" && sessionRows[r.Session] > 1 {
			add(r.Task)
			continue
		}
		if p, ok := m.cfg.PairFor(r.Repo); ok &&
			repoTask[p.FERepo+"/"+r.Task] && repoTask[p.BERepo+"/"+r.Task] {
			add(r.Task)
		}
	}
	m.taskColors = updateColors(m.taskColors, names, taskPalette)
}

var (
	styleGreen  = renderer.NewStyle().Foreground(lipgloss.Color("114"))
	styleYellow = renderer.NewStyle().Foreground(lipgloss.Color("179"))
	styleCyan   = renderer.NewStyle().Foreground(lipgloss.Color("80"))
	styleDim    = renderer.NewStyle().Foreground(lipgloss.Color("244"))
	styleBold   = renderer.NewStyle().Bold(true).Foreground(lipgloss.Color("254"))
	colorSelBg  = lipgloss.Color("237")
	styleSel    = renderer.NewStyle().Background(colorSelBg)
)

type mode int

const (
	modeList mode = iota
	modeConfirmEnd
	modeConfirmKill
	modePickRepo
	modeTaskName
)

type previewKind int

const (
	previewOff previewKind = iota
	previewInfo
	previewGitStatus
	previewSplit // git status | live info, 50/50
)

type Model struct {
	cfg     config.Config
	rows    []worktree.Row
	visible []int // indexes into rows after filter
	cursor  int   // index into visible
	filter  string

	mode        mode
	confirmYes  bool
	preview     previewKind
	previewText string
	endSession  string // temp "ork-end-*" tmux session being tailed in the live pane

	// ctrl-n flow
	repos       []string // repo basenames, favorites first
	repoPaths   map[string]string
	pairEntries map[string][2]string // pair display line -> {feRepo, beRepo}
	repoFilter  string
	repoCursor  int
	pickedRepo  string
	pickedRepo2 string // sibling repo when a pair entry was picked
	taskInput   string
	branches    []string

	width, height int
	result        Result
	reloadCh      <-chan struct{}
	loadRows      func() []worktree.Row
	err           string
	cow           []string // fortune/cowsay sidebar lines, refreshed per reload
	repoColors    map[string]lipgloss.Color
	taskColors    map[string]lipgloss.Color
}

type rowsMsg []worktree.Row
type stateChangedMsg struct{}
type tickMsg time.Time
type spawnDoneMsg struct{ err error }
type endDoneMsg struct{} // the temp end-task session has exited
type previewMsg struct {
	forPath string // selection the text was computed for
	text    string
}

// endPreviewKey marks a previewMsg carrying the end-task session tail —
// not tied to any row, so it must bypass the stale-selection check.
const endPreviewKey = "\x00end"

func New(cfg config.Config) *Model {
	// Preview visible by default, like the bash picker's always-on
	// --preview-window; ? toggles it away.
	m := &Model{cfg: cfg, preview: previewInfo}
	m.loadRows = func() []worktree.Row {
		deps := worktree.LiveDeps(agentstate.Dir(), agentstate.StaleAfter, agentstate.Read)
		return worktree.BuildRows(cfg, cfg.WorktreeRoots, deps)
	}
	return m
}

// Run blocks until the user picks something; returns what main should do.
func Run(cfg config.Config) (Result, error) {
	m := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if ch, err := agentstate.Watch(ctx, agentstate.Dir()); err == nil {
		m.reloadCh = ch
	}
	// Silence subprocess output (git worktree remove, branch -D, ...) while
	// bubbletea owns stderr — raw lines injected mid-frame shear the layout
	// (the "delete breaks line wrapping" bug). Restored after, so post-TUI
	// operations (new-task, attach) still report normally.
	worktree.Log = io.Discard
	defer func() { worktree.Log = os.Stderr }()
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithOutput(os.Stderr))
	out, err := p.Run()
	if err != nil {
		return Result{}, err
	}
	return out.(*Model).result, nil
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.reloadCmd(), m.watchCmd(), tick())
}

func (m *Model) reloadCmd() tea.Cmd {
	return func() tea.Msg { return rowsMsg(m.loadRows()) }
}

func (m *Model) watchCmd() tea.Cmd {
	if m.reloadCh == nil {
		return nil
	}
	return func() tea.Msg {
		if _, ok := <-m.reloadCh; !ok {
			return nil
		}
		return stateChangedMsg{}
	}
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) applyFilter() {
	m.visible = m.visible[:0]
	if m.filter == "" {
		for i := range m.rows {
			m.visible = append(m.visible, i)
		}
	} else {
		targets := make([]string, len(m.rows))
		for i, r := range m.rows {
			targets[i] = r.Repo + " " + r.Task + " " + r.Branch
		}
		for _, match := range fuzzy.Find(m.filter, targets) {
			m.visible = append(m.visible, match.Index)
		}
	}
	if m.cursor >= len(m.visible) {
		m.cursor = len(m.visible) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *Model) selected() (worktree.Row, bool) {
	if len(m.visible) == 0 || m.cursor >= len(m.visible) {
		return worktree.Row{}, false
	}
	return m.rows[m.visible[m.cursor]], true
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case rowsMsg:
		m.rows = msg
		m.applyFilter()
		m.cow = cowSidebar()
		m.updateRepoColors(m.rows)
		m.updateTaskColors(m.rows)
		return m, m.previewCmd()
	case stateChangedMsg:
		return m, tea.Batch(m.reloadCmd(), m.watchCmd())
	case spawnDoneMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.err = ""
		return m, m.reloadCmd()
	case endDoneMsg:
		m.endSession = ""
		return m, m.reloadCmd()
	case previewMsg:
		if msg.forPath == endPreviewKey {
			if m.endSession != "" {
				m.previewText = msg.text
			}
			return m, nil
		}
		// Drop stale results — the cursor may have moved while this one
		// was being computed.
		if sel, ok := m.selected(); ok && sel.Path == msg.forPath {
			m.previewText = msg.text
		}
		return m, nil
	case tickMsg:
		// Keep ticking even while the preview is toggled off — the tick is
		// also what makes re-enabling it come back live immediately. The
		// end-task tail overrides everything: it must refresh every tick,
		// and the row list reloads alongside so the deleted worktree
		// vanishes as soon as the removal lands — not only after the tail
		// session's final sleep expires.
		if m.endSession != "" {
			return m, tea.Batch(m.previewCmd(), m.reloadCmd(), tick())
		}
		var cmd tea.Cmd
		if m.preview != previewOff && m.mode == modeList {
			cmd = m.previewCmd()
		}
		return m, tea.Batch(cmd, tick())
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// previewCmd computes the preview in a Cmd (background goroutine) — the
// tmux/git execs it runs take tens of ms, which felt like input lag when
// they ran synchronously inside Update on every cursor move.
func (m *Model) previewCmd() tea.Cmd {
	// While an end-task cleanup session is alive, the live pane tails it
	// instead of the selected row; when it dies, endDoneMsg swings the pane
	// back and reloads the (now shorter) row list.
	if m.endSession != "" {
		name, lines := m.endSession, m.previewLines()
		return func() tea.Msg {
			if !tmux.HasSession(name) {
				return endDoneMsg{}
			}
			// Plain name, no "=" exact-match prefix: capture-pane (unlike
			// has-session) rejects it on tmux 3.2a.
			return previewMsg{forPath: endPreviewKey, text: lastLines(tmux.CapturePane(name), lines)}
		}
	}
	sel, ok := m.selected()
	if !ok || m.preview == previewOff {
		m.previewText = ""
		return nil
	}
	kind, cfg, lines, width := m.preview, m.cfg, m.previewLines(), m.width
	// Same color the task column uses in the top pane; solo tasks are
	// uncolored there, so fall back to the old dim path look.
	pathStyle := styleDim
	if c, ok := m.taskColors[sel.Task]; ok {
		pathStyle = renderer.NewStyle().Foreground(c)
	}
	return func() tea.Msg {
		var text string
		switch kind {
		case previewInfo:
			text = infoPreview(cfg, sel, lines, width, pathStyle)
		case previewGitStatus:
			text = gitStatusPreview(sel)
		case previewSplit:
			text = splitPreview(cfg, sel, lines, width, pathStyle)
		}
		return previewMsg{forPath: sel.Path, text: text}
	}
}

func (m *Model) previewLines() int {
	// Preview takes the bottom ~60% of the screen. View() spends
	// help+header+filter (3) + listH (height - height*6/10 - 5) + divider
	// (1) lines above it, so the space actually left is height*6/10 + 1;
	// one line is held back for the error/status line.
	n := m.height*6/10
	if n < 5 {
		n = 5
	}
	return n
}
