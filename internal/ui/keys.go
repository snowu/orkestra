package ui

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sahilm/fuzzy"

	"orkestra/internal/tmux"
	"orkestra/internal/worktree"
)

// openBrowser fires the OS default browser at url, fire-and-forget — errors
// deliberately ignored (nothing sane to do in a TUI if no browser exists).
func openBrowser(url string) {
	switch runtime.GOOS {
	case "darwin":
		exec.Command("open", url).Start()
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		exec.Command("xdg-open", url).Start()
	}
}

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

	case "ctrl+g":
		// Ensure fe/be dev-server windows exist in the base session,
		// detached — no attach, hot reload does the rest; see ideas.txt
		// fe/be friction note. Stays in the TUI: this is a background
		// trigger, not a "go do something else" action like attach/cd, so
		// there's no reason to lose your place in the list.
		if sel, ok := m.selected(); ok {
			cfg, repo, task, wt := m.cfg, sel.Repo, sel.Task, sel.Path
			return m, func() tea.Msg {
				return spawnDoneMsg{err: worktree.EnsureFEBEWindows(cfg, repo, task, wt)}
			}
		}
	case "ctrl+a":
		if sel, ok := m.selected(); ok {
			worktree.TouchAccess(sel.Repo, sel.Task)
			m.result = Result{Action: ActionOpenAll, Repo: sel.Repo, Task: sel.Task, WtPath: sel.Path}
			return m, tea.Quit
		}
	case "ctrl+o":
		// Open the task's FE in the default browser — port is derived, so
		// this works whether or not the dev server is up yet (browser just
		// shows connection refused until it is). Stays in the TUI.
		if sel, ok := m.selected(); ok {
			fePort, _ := worktree.TaskPorts(sel.Task)
			openBrowser(fmt.Sprintf("http://localhost:%d", fePort))
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
			s := string(msg.Runes)
			// Digits answer the selected row's agent prompt in place (claude
			// AskUserQuestion / permission menus) — only when that agent is
			// actually waiting for input, so digits keep working as filter
			// text everywhere else. Free-text answers need a real attach.
			if s >= "0" && s <= "9" && len(s) == 1 {
				if sel, ok := m.selected(); ok && (sel.Agent == "waiting" || sel.Agent == "input") {
					if p := resolvePane(m.cfg, sel); p != nil {
						tmux.SendKeys(p.Target, s)
						return m, m.previewCmd()
					}
				}
			}
			m.filter += s
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
