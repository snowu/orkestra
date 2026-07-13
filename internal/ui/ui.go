// Package ui is the bubbletea picker. It never touches stdout — bubbletea
// renders to stderr here (cd contract: stdout is reserved for the final
// cd-target line printed by main). It also never execs tmux attach itself:
// it returns a Result and lets main run the attach after the TUI has fully
// released the terminal.
package ui

import (
	"context"
	"fmt"
	"hash/crc32"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sahilm/fuzzy"

	"orkestra/internal/agentstate"
	"orkestra/internal/config"
	"orkestra/internal/worktree"
)

type Action int

const (
	ActionQuit Action = iota
	ActionAttach
	ActionCD
	ActionNewTask
)

type Result struct {
	Action   Action
	Repo     string
	Task     string
	WtPath   string
	RepoRoot string // set for ActionNewTask
}

// Same palette + CRC32 hash as the bash version (cksum is CRC32) so every
// repo keeps the exact color it had before the rewrite.
var repoPalette = []int{39, 208, 178, 141, 71, 203, 74, 209, 135, 214, 84, 168, 45, 220, 111}

func repoColor(name string) lipgloss.Color {
	idx := crc32.ChecksumIEEE([]byte(name)) % uint32(len(repoPalette))
	return lipgloss.Color(fmt.Sprintf("%d", repoPalette[idx]))
}

var (
	styleGreen  = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	styleYellow = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))
	styleCyan   = lipgloss.NewStyle().Foreground(lipgloss.Color("80"))
	styleDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	styleBold   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("254"))
	styleSel    = lipgloss.NewStyle().Background(lipgloss.Color("237"))
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

	// ctrl-n flow
	repos      []string // repo basenames, favorites first
	repoPaths  map[string]string
	repoFilter string
	repoCursor int
	pickedRepo string
	taskInput  string
	branches   []string

	width, height int
	result        Result
	reloadCh      <-chan struct{}
	loadRows      func() []worktree.Row
	err           string
}

type rowsMsg []worktree.Row
type stateChangedMsg struct{}
type tickMsg time.Time

func New(cfg config.Config) *Model {
	m := &Model{cfg: cfg, preview: previewOff}
	m.loadRows = func() []worktree.Row {
		deps := worktree.LiveDeps(agentstate.Dir(), agentstate.StaleAfter, agentstate.Read)
		return worktree.BuildRows(cfg.WorktreeRoots, deps)
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
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithOutput(os.Stderr))
	out, err := p.Run()
	if err != nil {
		return Result{}, err
	}
	return out.(*Model).result, nil
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.reloadCmd(), m.watchCmd())
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
		return m, nil
	case stateChangedMsg:
		return m, tea.Batch(m.reloadCmd(), m.watchCmd())
	case tickMsg:
		if m.preview != previewOff && m.mode == modeList {
			m.refreshPreview()
			return m, tick()
		}
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) refreshPreview() {
	sel, ok := m.selected()
	if !ok {
		m.previewText = ""
		return
	}
	switch m.preview {
	case previewInfo:
		m.previewText = infoPreview(m.cfg, sel, m.previewLines())
	case previewGitStatus:
		m.previewText = gitStatusPreview(sel)
	}
}

func (m *Model) previewLines() int {
	// preview takes bottom ~60% of the screen
	n := m.height*6/10 - 6
	if n < 5 {
		n = 5
	}
	return n
}
