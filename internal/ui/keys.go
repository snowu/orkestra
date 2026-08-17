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

	"orkestra/internal/config"
	"orkestra/internal/mux"
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

// tmux session names may not contain '.' or ':'.
var endSessionSafe = strings.NewReplacer(".", "_", ":", "_")

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeConfirmEnd, modeConfirmKill:
		return m.handleConfirmKey(msg)
	case modePickRepo:
		return m.handlePickRepoKey(msg)
	case modeTaskName:
		return m.handleTaskNameKey(msg)
	case modeConfirmSteal:
		return m.handleConfirmStealKey(msg)
	case modeScan:
		return m.handleScanKey(msg)
	case modeHelp:
		return m.handleHelpKey(msg)
	case modeGroupPick:
		return m.handleGroupPickKey(msg)
	}
	return m.handleListKey(msg)
}

func (m *Model) handleListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.result = Result{Action: ActionQuit}
		return m, tea.Quit
	case "esc":
		// First esc clears an active filter; only a second one quits.
		if m.filter != "" {
			m.filter = ""
			m.applyFilter()
			return m, m.previewCmd()
		}
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
				spawned, notes, err := worktree.EnsureGroupWindows(cfg, repo, task, wt)
				if amb, ok := err.(*worktree.AmbiguousGroupError); ok {
					return spawnDoneMsg{err: err, ambiguous: amb.Candidates, pickRepo: repo, pickTask: task, pickWt: wt}
				}
				return spawnDoneMsg{err: err, notes: notes, spawned: spawned}
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

	case "ctrl+f":
		return m, m.openScan()

	case "?":
		m.mode = modeHelp
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
						mux.SendKeys(p.Target, s)
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

func (m *Model) handleHelpKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "?", "esc", "q", "enter":
		m.mode = modeList
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
	m.err = ""
	// If ork's own window lives in a session about to die, move it out
	// first (with the client following) — otherwise the kill takes ork
	// down with it and the user is dumped out of the TUI.
	m.evacuateIfDoomed(sel)
	switch mode {
	case modeConfirmKill:
		worktree.KillSessionFor(m.cfg, ops, sel.Repo, sel.Task)
	case modeConfirmEnd:
		// Cleanup runs in a throwaway detached tmux session; the live pane
		// tails it (previewCmd) so the git output is visible in place. The
		// session self-destructs when its command exits; a short sleep keeps
		// the last lines readable before the pane snaps back to the row
		// preview. Works outside tmux too — detached sessions need no client.
		repo, task := sel.Repo, sel.Task
		name := "ork-end-" + endSessionSafe.Replace(repo+"-"+task)
		cmd := fmt.Sprintf("ork _end-task %q %q 2>&1; echo; echo '[done]'; sleep 3", repo, task)
		if err := mux.NewDetached(name, cmd); err != nil {
			// tmux refused — inline fallback, summary in the status line.
			repos := worktree.AllRepoDirs(homeDir(), m.cfg.ScanMaxDepth, repoCachePath(), 60*time.Second)
			m.err = worktree.EndTask(m.cfg, ops, repos, repo, task)
			return m, m.reloadCmd()
		}
		m.endSession = name
		if m.preview == previewOff {
			m.preview = previewInfo // force the lower pane open for the tail
		}
		return m, m.previewCmd()
	}
	return m, m.reloadCmd()
}

// evacuateIfDoomed checks whether the tmux session hosting ork's own
// window is one of the sessions the pending kill/end will destroy (task
// name, repo__task, or the session whose pane cwd is the worktree) and, if
// so, moves ork's window into the fallback "ork-home" session so ork — and
// the user's client — survive the kill.
func (m *Model) evacuateIfDoomed(sel worktree.Row) {
	cur, win := mux.CurrentWindow()
	if cur == "" {
		return
	}
	doomed := cur == sel.Task || cur == sel.Repo+"__"+sel.Task
	if !doomed {
		if p := resolvePane(m.cfg, sel); p != nil && p.Session == cur {
			doomed = true
		}
	}
	if doomed {
		mux.EvacuateWindow(win, "ork-home")
	}
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

	groupRows := m.resolvedGroupRows()
	m.repos = append(groupRows, append(append([]string{}, m.cfg.Favorites...), rest...)...)

	m.repoFilter, m.repoCursor = "", 0
	m.mode = modePickRepo
}

// resolvedGroupRows builds the group rows for the repo picker — one per
// configured group whose member repos ALL resolved in m.repoPaths (a group
// with an unresolvable repo can't have a worktree created for every
// member). Populates m.repoGroups as a side effect. Group rows come first —
// they're the higher-intent choice when a task spans multiple repos.
// Separated from startPickRepo so it's testable without a filesystem scan.
func (m *Model) resolvedGroupRows() []string {
	m.repoGroups = map[string]config.Group{}
	var groupRows []string
	for _, g := range m.cfg.Groups {
		allResolved := true
		for _, p := range g.Processes {
			if _, ok := m.repoPaths[p.Repo]; !ok {
				allResolved = false
				break
			}
		}
		if !allResolved {
			continue
		}
		row := groupRowName(g)
		m.repoGroups[row] = g
		groupRows = append(groupRows, row)
	}
	return groupRows
}

// groupRowName is the picker's display label for a group row, e.g.
// "credit-risk-mfe (remote+host+be)".
func groupRowName(g config.Group) string {
	labels := make([]string, len(g.Processes))
	for i, p := range g.Processes {
		labels[i] = p.Label
	}
	return fmt.Sprintf("%s (%s)", g.Name, strings.Join(labels, "+"))
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
			m.pickRepoEntry(repos[m.repoCursor])
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

// pickRepoEntry routes a picker row to the task-name prompt. A group row
// carries its primary repo (the group's first process) plus the group
// itself, so startTaskName can offer branch-reuse for the primary while
// remembering to fan the task out to the other members on submit.
func (m *Model) pickRepoEntry(name string) {
	if g, ok := m.repoGroups[name]; ok {
		m.startTaskName(g.Processes[0].Repo)
		gCopy := g
		m.pickedGroup = &gCopy
		return
	}
	m.startTaskName(name)
}

func (m *Model) startTaskName(repo string) {
	m.pickedRepo = repo
	m.pickedGroup = nil
	m.taskInput = ""
	m.branchCursor = 0
	// maxAge 0: when you are already naming a task, every branch without a
	// worktree is a candidate — the 48h cut belongs to the scan screen.
	m.branches = worktree.BranchCandidates(m.repoPaths[repo], 0)
	m.mode = modeTaskName
}

// extraGroupRoots returns the repo roots of every OTHER member of the
// currently picked group (excluding the primary, m.pickedRepo).
func (m *Model) extraGroupRoots() []string {
	if m.pickedGroup == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{m.pickedRepo: true}
	for _, p := range m.pickedGroup.Processes {
		if seen[p.Repo] {
			continue
		}
		seen[p.Repo] = true
		if root, ok := m.repoPaths[p.Repo]; ok {
			out = append(out, root)
		}
	}
	return out
}

// filteredBranches narrows the branch list by the same text being typed as
// a possible new branch name — one input, two meanings, disambiguated by
// branchCursor.
func (m *Model) filteredBranches() []worktree.BranchCand {
	if m.taskInput == "" {
		return m.branches
	}
	names := make([]string, len(m.branches))
	for i, b := range m.branches {
		names[i] = b.Name
	}
	var out []worktree.BranchCand
	for _, match := range fuzzy.Find(m.taskInput, names) {
		out = append(out, m.branches[match.Index])
	}
	return out
}

// useBranch returns the ActionUseBranch result, prompting first when the
// branch is checked out elsewhere. The check happens HERE, not in run.go,
// because returning a Result quits the TUI — there would be nothing left
// to prompt with.
func (m *Model) useBranch(b worktree.BranchCand, force bool) (tea.Model, tea.Cmd) {
	repoRoot := b.Root
	if repoRoot == "" {
		repoRoot = m.repoPaths[m.pickedRepo]
	}
	if !force {
		if c := worktree.BranchCheckout(repoRoot, b.Name); c != nil {
			m.stealConflict, m.stealBranch, m.stealRoot = c, b.Name, repoRoot
			m.stealReturn = m.mode
			m.mode = modeConfirmSteal
			return m, nil
		}
	}
	m.result = Result{
		Action: ActionUseBranch, Repo: m.pickedRepo, Task: worktree.TaskNameFor(b.Name),
		Branch: b.Name, Force: force,
		RepoRoot:       repoRoot,
		ExtraRepoRoots: m.extraGroupRoots(),
	}
	return m, tea.Quit
}

// scanMaxAge: branches whose tip is older than this are assumed handled —
// the screen exists to resurface recent work, not to list every branch.
const scanMaxAge = 48 * time.Hour

// openScan loads candidates in a tea.Cmd, never inline: one
// for-each-ref plus a worktree list PER REPO adds up, and the TUI must
// keep drawing while it runs.
func (m *Model) openScan() tea.Cmd {
	m.mode = modeScan
	m.scanFilter, m.scanCursor = "", 0
	m.scanCands, m.scanning = nil, true
	cfg := m.cfg
	return func() tea.Msg {
		var out []worktree.BranchCand
		paths := map[string]string{}
		for _, dir := range worktree.AllRepoDirs(homeDir(), cfg.ScanMaxDepth, repoCachePath(), 60*time.Second) {
			out = append(out, worktree.BranchCandidates(dir, scanMaxAge)...)
			if paths[filepath.Base(dir)] == "" {
				paths[filepath.Base(dir)] = dir
			}
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].Tip.After(out[j].Tip) })
		return scanMsg{cands: out, paths: paths}
	}
}

func (m *Model) filteredScan() []worktree.BranchCand {
	if m.scanFilter == "" {
		return m.scanCands
	}
	labels := make([]string, len(m.scanCands))
	for i, c := range m.scanCands {
		labels[i] = c.Repo + "/" + c.Name
	}
	var out []worktree.BranchCand
	for _, match := range fuzzy.Find(m.scanFilter, labels) {
		out = append(out, m.scanCands[match.Index])
	}
	return out
}

func (m *Model) handleScanKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cands := m.filteredScan()
	switch msg.String() {
	case "ctrl+c", "esc":
		m.mode = modeList
	case "up", "ctrl+p":
		if m.scanCursor > 0 {
			m.scanCursor--
		}
	case "down", "ctrl+j":
		if m.scanCursor < len(cands)-1 {
			m.scanCursor++
		}
	case "enter":
		if m.scanCursor < len(cands) {
			c := cands[m.scanCursor]
			// Scan rows span repos, so the row carries the repo — unlike the
			// task-name screen, where it was picked beforehand.
			m.pickedRepo = c.Repo
			m.pickedGroup = nil
			return m.useBranch(c, false)
		}
	case "backspace":
		if len(m.scanFilter) > 0 {
			m.scanFilter = m.scanFilter[:len(m.scanFilter)-1]
			m.scanCursor = 0
		}
	default:
		if msg.Type == tea.KeyRunes && !msg.Alt {
			m.scanFilter += string(msg.Runes)
			m.scanCursor = 0
		}
	}
	return m, nil
}

func (m *Model) handleTaskNameKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	branches := m.filteredBranches()
	switch msg.String() {
	case "ctrl+c", "esc":
		m.mode = modePickRepo
	case "up", "ctrl+p":
		if m.branchCursor > 0 {
			m.branchCursor--
		}
	case "down", "ctrl+j":
		if m.branchCursor < len(branches) {
			m.branchCursor++
		}
	case "enter":
		if m.branchCursor > 0 && m.branchCursor <= len(branches) {
			return m.useBranch(branches[m.branchCursor-1], false)
		}
		task := strings.TrimSpace(m.taskInput)
		if task == "" {
			return m, nil
		}
		m.result = Result{
			Action: ActionNewTask, Repo: m.pickedRepo, Task: task,
			RepoRoot:       m.repoPaths[m.pickedRepo],
			ExtraRepoRoots: m.extraGroupRoots(),
		}
		return m, tea.Quit
	case "backspace":
		if len(m.taskInput) > 0 {
			m.taskInput = m.taskInput[:len(m.taskInput)-1]
			m.branchCursor = 0
		}
	default:
		if msg.Type == tea.KeyRunes && !msg.Alt {
			m.taskInput += string(msg.Runes)
			m.branchCursor = 0 // typing re-filters; a held cursor would point at a different branch
		}
	}
	return m, nil
}

// handleGroupPickKey drives the ctrl-g ambiguity picker: repo/task resolved
// to two or more equally-materialized groups (see AmbiguousGroupError), so
// the user chooses which one to actually spawn.
func (m *Model) handleGroupPickKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "esc":
		m.mode = modeList
		m.groupCands = nil
	case "up", "ctrl+p":
		if m.groupCursor > 0 {
			m.groupCursor--
		}
	case "down", "ctrl+j":
		if m.groupCursor < len(m.groupCands)-1 {
			m.groupCursor++
		}
	case "enter":
		if m.groupCursor < len(m.groupCands) {
			g := m.groupCands[m.groupCursor]
			cfg, repo, task, wt := m.cfg, m.groupRepo, m.groupTask, m.groupWt
			m.mode = modeList
			m.groupCands = nil
			return m, func() tea.Msg {
				spawned, notes, err := worktree.EnsureGroupWindowsFor(cfg, g, repo, task, wt)
				return spawnDoneMsg{err: err, notes: notes, spawned: spawned}
			}
		}
	}
	return m, nil
}

func (m *Model) handleConfirmStealKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter", "y":
		b := worktree.BranchCand{Repo: m.pickedRepo, Root: m.stealRoot, Name: m.stealBranch}
		m.stealConflict = nil
		return m.useBranch(b, true)
	default: // esc, ctrl+c, n, anything else — cancel is the safe default
		m.stealConflict = nil
		m.mode = m.stealReturn
	}
	return m, nil
}
