package ui

import (
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sahilm/fuzzy"

	"orkestra/internal/worktree"
)

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeConfirmEnd, modeConfirmKill:
		return m.handleConfirmKey(msg)
	case modePickRepo:
		return m.handlePickRepoKey(msg)
	case modeTaskName:
		return m.handleTaskNameKey(msg)
	}
	return m.handleListKey(msg)
}

func (m *Model) handleListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "esc":
		m.result = Result{Action: ActionQuit}
		return m, tea.Quit

	case "up", "ctrl+p":
		if m.cursor > 0 {
			m.cursor--
			return m, m.previewCmd()
		}
	case "down", "ctrl+j":
		if m.cursor < len(m.visible)-1 {
			m.cursor++
			return m, m.previewCmd()
		}

	case "enter":
		if sel, ok := m.selected(); ok {
			worktree.TouchAccess(sel.Repo, sel.Task)
			m.result = Result{Action: ActionAttach, Repo: sel.Repo, Task: sel.Task, WtPath: sel.Path}
			return m, tea.Quit
		}
	case "alt+enter":
		// cd only, no tmux session — deliberately no fallback to the
		// session path: NOT touching tmux is the whole point.
		if sel, ok := m.selected(); ok {
			worktree.TouchAccess(sel.Repo, sel.Task)
			m.result = Result{Action: ActionCD, Repo: sel.Repo, Task: sel.Task, WtPath: sel.Path}
			return m, tea.Quit
		}

	case "ctrl+r":
		return m, m.reloadCmd()

	case "ctrl+x":
		if _, ok := m.selected(); ok {
			m.mode = modeConfirmEnd
			m.confirmYes = false // "no" is the reflexive-ENTER answer
		}
	case "ctrl+k":
		if _, ok := m.selected(); ok {
			m.mode = modeConfirmKill
			m.confirmYes = false
		}

	case "ctrl+n":
		m.startPickRepo()
		return m, nil

	case "tab":
		// Cycle: info -> git status -> off -> info. (From the ctrl-s split
		// view, tab folds back into the cycle at git status.)
		switch m.preview {
		case previewInfo:
			m.preview = previewGitStatus
		case previewGitStatus, previewSplit:
			m.preview = previewOff
			m.previewText = ""
			return m, nil
		default:
			m.preview = previewInfo
		}
		return m, m.previewCmd()
	case "ctrl+s":
		if m.preview == previewSplit {
			m.preview = previewOff
			m.previewText = ""
			return m, nil
		}
		m.preview = previewSplit
		return m, m.previewCmd()

	case "backspace":
		if len(m.filter) > 0 {
			m.filter = m.filter[:len(m.filter)-1]
			m.applyFilter()
		}
	default:
		if msg.Type == tea.KeyRunes && !msg.Alt {
			m.filter += string(msg.Runes)
			m.applyFilter()
		}
	}
	return m, nil
}

func (m *Model) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "n", "N":
		m.mode = modeList
	case "left", "right", "tab", "up", "down":
		m.confirmYes = !m.confirmYes
	case "y", "Y":
		m.confirmYes = true
		return m.confirmAccept()
	case "enter":
		return m.confirmAccept()
	}
	return m, nil
}

func (m *Model) confirmAccept() (tea.Model, tea.Cmd) {
	mode := m.mode
	m.mode = modeList
	if !m.confirmYes {
		return m, nil
	}
	sel, ok := m.selected()
	if !ok {
		return m, nil
	}
	ops := worktree.LiveTmuxOps()
	switch mode {
	case modeConfirmKill:
		worktree.KillSessionFor(m.cfg, ops, sel.Repo, sel.Task)
	case modeConfirmEnd:
		repos := worktree.AllRepoDirs(homeDir(), m.cfg.ScanMaxDepth, repoCachePath(), 60*time.Second)
		worktree.EndTask(m.cfg, ops, repos, sel.Repo, sel.Task)
	}
	return m, m.reloadCmd()
}

// --- ctrl-n: repo picker, then task-name input ---

func (m *Model) startPickRepo() {
	dirs := worktree.AllRepoDirs(homeDir(), m.cfg.ScanMaxDepth, repoCachePath(), 60*time.Second)
	m.repoPaths = map[string]string{}
	var rest []string
	fav := map[string]bool{}
	for _, f := range m.cfg.Favorites {
		fav[f] = true
	}
	seen := map[string]bool{}
	for _, d := range dirs {
		base := filepath.Base(d)
		if _, dup := m.repoPaths[base]; !dup {
			m.repoPaths[base] = d
		}
		if !fav[base] && !seen[base] {
			rest = append(rest, base)
			seen[base] = true
		}
	}
	sort.Strings(rest)
	m.repos = append(append([]string{}, m.cfg.Favorites...), rest...)
	m.repoFilter, m.repoCursor = "", 0
	m.mode = modePickRepo
}

func (m *Model) filteredRepos() []string {
	if m.repoFilter == "" {
		return m.repos
	}
	var out []string
	for _, match := range fuzzy.Find(m.repoFilter, m.repos) {
		out = append(out, m.repos[match.Index])
	}
	return out
}

func (m *Model) handlePickRepoKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	repos := m.filteredRepos()
	switch msg.String() {
	case "ctrl+c", "esc":
		m.mode = modeList
	case "up", "ctrl+p":
		if m.repoCursor > 0 {
			m.repoCursor--
		}
	case "down", "ctrl+j":
		if m.repoCursor < len(repos)-1 {
			m.repoCursor++
		}
	case "enter":
		if m.repoCursor < len(repos) {
			m.pickedRepo = repos[m.repoCursor]
			m.taskInput = ""
			m.branches = existingBranches(m.repoPaths[m.pickedRepo])
			m.mode = modeTaskName
		}
	case "backspace":
		if len(m.repoFilter) > 0 {
			m.repoFilter = m.repoFilter[:len(m.repoFilter)-1]
			m.repoCursor = 0
		}
	default:
		if msg.Type == tea.KeyRunes && !msg.Alt {
			m.repoFilter += string(msg.Runes)
			m.repoCursor = 0
		}
	}
	return m, nil
}

func (m *Model) handleTaskNameKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "esc":
		m.mode = modePickRepo
	case "enter":
		task := strings.TrimSpace(m.taskInput)
		if task == "" {
			return m, nil
		}
		m.result = Result{
			Action: ActionNewTask, Repo: m.pickedRepo, Task: task,
			RepoRoot: m.repoPaths[m.pickedRepo],
		}
		return m, tea.Quit
	case "backspace":
		if len(m.taskInput) > 0 {
			m.taskInput = m.taskInput[:len(m.taskInput)-1]
		}
	default:
		if msg.Type == tea.KeyRunes && !msg.Alt {
			m.taskInput += string(msg.Runes)
		}
	}
	return m, nil
}
