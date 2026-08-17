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
	"net"
	"os"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sahilm/fuzzy"

	"orkestra/internal/agentstate"
	"orkestra/internal/config"
	"orkestra/internal/mux"
	"orkestra/internal/worktree"
)

type Action int

const (
	ActionQuit Action = iota
	ActionAttach
	ActionCD
	ActionNewTask
	ActionOpenAll // ctrl+a: attach base session with fe/be windows ensured
	ActionUseBranch
)

type Result struct {
	Action   Action
	Repo     string
	Task     string
	WtPath   string
	RepoRoot string // set for ActionNewTask
	// ExtraRepoRoots are additional repos to create the same task in
	// (a process group's other members). Empty for a single-repo task.
	ExtraRepoRoots []string
	// Set for ActionUseBranch: the existing branch to build the worktree
	// on. Force is the user's answer to the "already checked out
	// elsewhere" prompt, resolved in the TUI because bubbletea exits as
	// soon as a Result is returned.
	Branch string
	Force  bool
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
// shared across worktrees, or 2+ members of a configured process group under
// the same task (which deserve the link color even before any session
// exists).
func (m *Model) updateTaskColors(rows []worktree.Row) {
	sessionRows := map[string]int{}
	groupTaskRows := map[string]int{} // groupName/task -> row count
	for _, r := range rows {
		if r.Session != "" {
			sessionRows[r.Session]++
		}
		if r.GroupName != "" {
			groupTaskRows[r.GroupName+"/"+r.Task]++
		}
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
		if r.GroupName != "" && groupTaskRows[r.GroupName+"/"+r.Task] > 1 {
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
	modeConfirmSteal
	modeScan
	modeHelp
	modeGroupPick
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
	repos         []string // group rows (first) + repo basenames, favorites first
	repoPaths     map[string]string
	repoGroups    map[string]config.Group // row name -> group, for rows in repos that are group entries
	repoFilter    string
	repoCursor    int
	pickedRepo    string
	pickedGroup   *config.Group // set when the task-name screen was entered from a group row
	taskInput     string
	branches      []worktree.BranchCand
	branchCursor  int                // 0 = the typed-text row, 1..n = branches
	stealConflict *worktree.Conflict // pending "checked out elsewhere" prompt
	stealBranch   string             // branch the prompt is about
	stealRoot     string             // repo root the prompt is about
	stealReturn   mode               // mode to restore if the steal prompt is cancelled

	// ctrl-f scan flow
	scanCands  []worktree.BranchCand
	scanFilter string
	scanCursor int
	scanning   bool // candidates still loading

	// ctrl-g ambiguous-group picker
	groupCands  []config.Group
	groupCursor int
	groupRepo   string
	groupTask   string
	groupWt     string

	startInScan bool

	width, height int
	result        Result
	reloadCh      <-chan struct{}
	loadRows      func() []worktree.Row
	err           string
	cow           []string // fortune/cowsay sidebar lines, refreshed per reload
	repoColors    map[string]lipgloss.Color
	taskColors    map[string]lipgloss.Color
	proxyUp       bool // login proxy (:3000) reachability, probed off the render path
}

type rowsMsg []worktree.Row
type scanMsg struct {
	cands []worktree.BranchCand
	paths map[string]string
}
type stateChangedMsg struct{}
type tickMsg time.Time
type proxyStatusMsg bool
type spawnDoneMsg struct {
	err     error
	notes   []string
	spawned []string
	// ambiguous carries the pending spawn's identity when err is an
	// *AmbiguousGroupError, so Update can seed modeGroupPick without
	// re-deriving repo/task/wt from anywhere else.
	ambiguous []config.Group
	pickRepo  string
	pickTask  string
	pickWt    string
}
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
// startInScan opens straight on the scan screen (`ork scan`), reusing the
// whole model so conflict prompting is identical from both entry points.
func Run(cfg config.Config) (Result, error) { return run(cfg, false) }

// RunScan is Run, opened on the scan screen.
func RunScan(cfg config.Config) (Result, error) { return run(cfg, true) }

func run(cfg config.Config, startInScan bool) (Result, error) {
	m := New(cfg)
	m.startInScan = startInScan
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
	cmds := []tea.Cmd{m.reloadCmd(), m.watchCmd(), tick()}
	if len(m.cfg.Groups) > 0 {
		cmds = append(cmds, proxyProbeCmd())
	}
	if m.startInScan {
		cmds = append(cmds, m.openScan())
	}
	return tea.Batch(cmds...)
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

// proxyProbeCmd checks whether the login proxy (:3000) is up, off the
// render path — View() runs on every keystroke/tick and must never dial a
// socket. Runs on its own 5s cadence (much lower than the 1s UI tick) since
// a TCP dial, even with a short timeout, is real latency View() can't pay.
func proxyProbeCmd() tea.Cmd {
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg {
		return proxyStatusMsg(portListening("127.0.0.1:3000"))
	})
}

// portListening is a short-timeout TCP probe, mirroring cmd/ork/run.go's
// helper of the same name — kept separate since internal/ui must not import
// cmd.
func portListening(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 150*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
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
	case scanMsg:
		if m.repoPaths == nil {
			m.repoPaths = map[string]string{}
		}
		for k, v := range msg.paths {
			if m.repoPaths[k] == "" {
				m.repoPaths[k] = v
			}
		}
		m.scanCands, m.scanning = msg.cands, false
		return m, nil
	case proxyStatusMsg:
		m.proxyUp = bool(msg)
		return m, proxyProbeCmd()
	case stateChangedMsg:
		return m, tea.Batch(m.reloadCmd(), m.watchCmd())
	case spawnDoneMsg:
		if msg.err != nil {
			if msg.ambiguous != nil {
				m.groupCands = msg.ambiguous
				m.groupCursor = 0
				m.groupRepo, m.groupTask, m.groupWt = msg.pickRepo, msg.pickTask, msg.pickWt
				m.mode = modeGroupPick
				return m, nil
			}
			m.err = msg.err.Error()
			return m, nil
		}
		if len(msg.notes) > 0 {
			m.err = strings.Join(msg.notes, " · ")
		} else {
			m.err = ""
		}
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
			if !mux.HasSession(name) {
				return endDoneMsg{}
			}
			// Plain name, no "=" exact-match prefix: capture-pane (unlike
			// has-session) rejects it on tmux 3.2a.
			return previewMsg{forPath: endPreviewKey, text: lastLines(mux.CapturePane(name), lines)}
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
			text = gitStatusSplit(sel, lines, width)
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
