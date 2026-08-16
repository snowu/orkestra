package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"orkestra/internal/config"
	"orkestra/internal/worktree"
)

func testModel() *Model {
	m := New(config.Config{WorktreeRoots: []string{"/nowhere"}})
	m.width, m.height = 120, 40
	m.rows = []worktree.Row{
		{Repo: "repoA", Task: "task-one", Branch: "task-one", Path: "/w/repoA/task-one", Live: true, Session: "task-one", Agent: "running", Cmd: "node"},
		{Repo: "repoB", Task: "other", Branch: "fix", Path: "/w/repoB/other"},
	}
	m.applyFilter()
	return m
}

func testModelWithPairs() *Model {
	m := New(config.Config{WorktreeRoots: []string{"/nowhere"}, Pairs: []config.Pair{{FERepo: "fe", BERepo: "be"}}})
	m.width, m.height = 120, 40
	m.rows = []worktree.Row{
		{Repo: "repoA", Task: "task-one", Branch: "task-one", Path: "/w/repoA/task-one"},
	}
	m.applyFilter()
	return m
}

func TestProxyIndicatorShownOnlyWithPairs(t *testing.T) {
	v := testModel().View()
	if strings.Contains(v, "proxy :3000") {
		t.Error("proxy indicator shown with no configured pairs")
	}

	mp := testModelWithPairs()
	v = mp.View()
	if !strings.Contains(v, "proxy :3000") {
		t.Error("proxy indicator missing with pairs configured")
	}
}

func TestProxyIndicatorReflectsUpDown(t *testing.T) {
	m := testModelWithPairs()
	m.proxyUp = false
	if !strings.Contains(m.View(), "proxy :3000 down") {
		t.Error("expected 'down' status when proxyUp is false")
	}

	m.proxyUp = true
	if !strings.Contains(m.View(), "proxy :3000 up") {
		t.Error("expected 'up' status when proxyUp is true")
	}
}

func TestProxyStatusMsgUpdatesModel(t *testing.T) {
	m := testModelWithPairs()
	m.proxyUp = false
	updated, _ := m.Update(proxyStatusMsg(true))
	if !updated.(*Model).proxyUp {
		t.Error("proxyStatusMsg(true) did not set proxyUp")
	}
	updated, _ = updated.(*Model).Update(proxyStatusMsg(false))
	if updated.(*Model).proxyUp {
		t.Error("proxyStatusMsg(false) did not clear proxyUp")
	}
}

func TestViewRendersRows(t *testing.T) {
	v := testModel().View()
	for _, want := range []string{"REPO", "repoA", "task-one", "live", "running", "repoB", "idle"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q", want)
		}
	}
	// branch == task renders as "="
	if !strings.Contains(v, "=") {
		t.Error("branch shorthand '=' missing")
	}
}

func TestFilterNarrows(t *testing.T) {
	m := testModel()
	m.filter = "other"
	m.applyFilter()
	if len(m.visible) != 1 || m.rows[m.visible[0]].Task != "other" {
		t.Fatalf("filter failed: %v", m.visible)
	}
}

func TestEnterReturnsAttach(t *testing.T) {
	m := testModel()
	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should quit")
	}
	if m.result.Action != ActionAttach || m.result.Task != "task-one" {
		t.Errorf("result = %+v", m.result)
	}
}

func TestConfirmDefaultsToNo(t *testing.T) {
	m := testModel()
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlX})
	if m.mode != modeConfirmEnd || m.confirmYes {
		t.Fatalf("mode=%v yes=%v", m.mode, m.confirmYes)
	}
	// reflexive enter = safe "no", back to list, nothing destroyed
	m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != modeList {
		t.Error("enter on 'no' should return to list")
	}
}

func TestPickRepoEntryPair(t *testing.T) {
	m := &Model{
		repoPaths: map[string]string{
			"cr-frontend": "/x/cr-frontend", "cr-managament": "/x/cr-managament",
		},
		pairEntries: map[string][2]string{
			"cr-frontend + cr-managament": {"cr-frontend", "cr-managament"},
		},
	}
	m.pickRepoEntry("cr-frontend + cr-managament")
	if m.pickedRepo != "cr-frontend" || m.pickedRepo2 != "cr-managament" || m.mode != modeTaskName {
		t.Fatalf("pair pick: repo=%q repo2=%q mode=%v", m.pickedRepo, m.pickedRepo2, m.mode)
	}
	m.pickRepoEntry("cr-frontend")
	if m.pickedRepo != "cr-frontend" || m.pickedRepo2 != "" {
		t.Fatalf("plain pick should clear repo2, got %q", m.pickedRepo2)
	}
}

func TestStickyColors(t *testing.T) {
	pal := []int{1, 2, 3, 4}
	first := updateColors(nil, []string{"alpha", "beta"}, pal)
	// New name appears: existing assignments must not move.
	second := updateColors(first, []string{"alpha", "beta", "gamma"}, pal)
	if second["alpha"] != first["alpha"] || second["beta"] != first["beta"] {
		t.Fatalf("colors reassigned: %v -> %v", first, second)
	}
	if second["gamma"] == "" || second["gamma"] == second["alpha"] || second["gamma"] == second["beta"] {
		t.Fatalf("gamma got bad color: %v", second)
	}
	// alpha leaves: beta/gamma keep theirs.
	third := updateColors(second, []string{"beta", "delta", "gamma"}, pal)
	if third["beta"] != second["beta"] || third["gamma"] != second["gamma"] {
		t.Fatalf("colors moved on removal: %v", third)
	}
}

func TestPairColoredWithoutSession(t *testing.T) {
	m := New(config.Config{
		WorktreeRoots: []string{"/nowhere"},
		Pairs:         []config.Pair{{FERepo: "fe-r", BERepo: "be-r"}},
	})
	rows := []worktree.Row{
		{Repo: "fe-r", Task: "tsk"},
		{Repo: "be-r", Task: "tsk"},
		{Repo: "solo", Task: "lonely"},
	}
	m.updateTaskColors(rows)
	if _, ok := m.taskColors["tsk"]; !ok {
		t.Error("sessionless pair task should be colored")
	}
	if _, ok := m.taskColors["lonely"]; ok {
		t.Error("solo task should stay uncolored")
	}
}

func taskNameModel(t *testing.T) *Model {
	t.Helper()
	m := testModel()
	m.pickedRepo = "repoA"
	m.repoPaths = map[string]string{"repoA": "/nowhere/repoA"}
	m.mode = modeTaskName
	m.taskInput = ""
	m.branchCursor = 0
	m.branches = []worktree.BranchCand{
		{Repo: "repoA", Name: "feat/alpha"},
		{Repo: "repoA", Name: "fix-beta"},
	}
	return m
}

func TestTaskNameCursorZeroCreatesNewBranch(t *testing.T) {
	m := taskNameModel(t)
	m.taskInput = "brand-new"
	m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.result.Action != ActionNewTask || m.result.Task != "brand-new" {
		t.Errorf("result = %+v, want ActionNewTask/brand-new", m.result)
	}
}

func TestTaskNameTypingFiltersBranches(t *testing.T) {
	m := taskNameModel(t)
	m.taskInput = "alpha"
	got := m.filteredBranches()
	if len(got) != 1 || got[0].Name != "feat/alpha" {
		t.Errorf("filtered = %+v", got)
	}
}

func TestTaskNameDownSelectsBranch(t *testing.T) {
	m := taskNameModel(t)
	m.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	if m.branchCursor != 1 {
		t.Fatalf("branchCursor = %d, want 1", m.branchCursor)
	}
	// cursor cannot run past the last branch
	m.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	m.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	if m.branchCursor != 2 {
		t.Errorf("branchCursor = %d, want clamped to 2", m.branchCursor)
	}
	// back to the text row
	for i := 0; i < 5; i++ {
		m.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	}
	if m.branchCursor != 0 {
		t.Errorf("branchCursor = %d, want clamped to 0", m.branchCursor)
	}
}

func TestTaskNameEnterOnBranchUsesIt(t *testing.T) {
	m := taskNameModel(t)
	m.branchCursor = 1 // feat/alpha
	m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.result.Action != ActionUseBranch {
		t.Fatalf("action = %v, want ActionUseBranch", m.result.Action)
	}
	if m.result.Branch != "feat/alpha" || m.result.Task != "feat-alpha" {
		t.Errorf("result = %+v, want branch feat/alpha task feat-alpha", m.result)
	}
	if m.result.Force {
		t.Error("Force must be false when no conflict was confirmed")
	}
}

func TestStealConfirmSetsForce(t *testing.T) {
	m := taskNameModel(t)
	m.mode = modeConfirmSteal
	m.stealReturn = modeTaskName
	m.stealBranch = "feat/alpha"
	m.stealConflict = &worktree.Conflict{Branch: "feat/alpha", Path: "/code/repoA", IsMain: true}
	m.pickedRepo = "repoA"

	// esc cancels back to the task-name screen, no result
	m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != modeTaskName || m.result.Action == ActionUseBranch {
		t.Errorf("esc should cancel, mode=%v result=%+v", m.mode, m.result)
	}

	m.mode = modeConfirmSteal
	m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.result.Action != ActionUseBranch || !m.result.Force {
		t.Errorf("enter should confirm with Force, got %+v", m.result)
	}
}

func scanModel(t *testing.T) *Model {
	t.Helper()
	m := testModel()
	m.mode = modeScan
	m.repoPaths = map[string]string{"repoA": "/nowhere/repoA", "repoB": "/nowhere/repoB"}
	m.scanCands = []worktree.BranchCand{
		{Repo: "repoA", Root: "/nowhere/repoA", Name: "feat/one", Tip: time.Now().Add(-2 * time.Hour)},
		{Repo: "repoB", Root: "/nowhere/repoB", Name: "fix-two", Tip: time.Now().Add(-30 * time.Hour)},
	}
	return m
}

func TestScanFilterAndSelect(t *testing.T) {
	m := scanModel(t)
	m.scanFilter = "fix"
	if got := m.filteredScan(); len(got) != 1 || got[0].Name != "fix-two" {
		t.Fatalf("filtered = %+v", got)
	}

	m.scanFilter = ""
	m.scanCursor = 1 // fix-two
	m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.result.Action != ActionUseBranch {
		t.Fatalf("action = %v, want ActionUseBranch", m.result.Action)
	}
	if m.result.Branch != "fix-two" || m.result.Task != "fix-two" {
		t.Errorf("result = %+v", m.result)
	}
	if m.result.Repo != "repoB" || m.result.RepoRoot != "/nowhere/repoB" {
		t.Errorf("scan must carry the row's OWN repo, got %+v", m.result)
	}
}

func TestScanEscReturnsToList(t *testing.T) {
	m := scanModel(t)
	m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != modeList {
		t.Errorf("mode = %v, want modeList", m.mode)
	}
}

// TestOpenScanFreshModelNoPanic guards bug 1: ork scan (or ctrl+f before any
// ctrl+n) starts from a model whose repoPaths is nil. openScan's Cmd must not
// touch m.repoPaths directly (nil-map write panics), and Update's scanMsg
// case must allocate repoPaths before merging into it.
//
// This feeds a scanMsg with a NON-EMPTY paths map directly into Update — the
// merge loop's map write is exactly the crash site (`m.repoPaths[k] = v`
// against a nil map). Deliberately not routed through openScan()'s real Cmd:
// that Cmd calls worktree.AllRepoDirs against the real filesystem, which in
// a sandboxed/CI environment may legitimately discover zero repos, making
// msg.paths empty and the merge loop never execute — a false pass that
// exercises nothing. Constructing the message directly makes the crash site
// deterministic regardless of environment.
func TestOpenScanFreshModelNoPanic(t *testing.T) {
	m := New(config.Config{WorktreeRoots: []string{"/nowhere"}})
	if m.repoPaths != nil {
		t.Fatal("test assumption broken: repoPaths already allocated")
	}
	msg := scanMsg{
		cands: []worktree.BranchCand{{Repo: "repoA", Root: "/nowhere/repoA", Name: "feat/x"}},
		paths: map[string]string{"repoA": "/nowhere/repoA"},
	}
	m.Update(msg) // must not panic: assignment to entry in nil map
	if m.repoPaths == nil {
		t.Fatal("repoPaths still nil after scanMsg processed")
	}
	if m.repoPaths["repoA"] != "/nowhere/repoA" {
		t.Errorf("repoPaths[repoA] = %q, want /nowhere/repoA", m.repoPaths["repoA"])
	}
}

// TestScanStealCancelReturnsToScan guards bug 3: cancelling a steal prompt
// reached from the scan screen must land back on modeScan, not modeTaskName.
func TestScanStealCancelReturnsToScan(t *testing.T) {
	m := scanModel(t)
	m.scanCursor = 0
	m.stealReturn = modeScan
	m.mode = modeConfirmSteal
	m.stealBranch = "feat/one"
	m.stealConflict = &worktree.Conflict{Branch: "feat/one", Path: "/code/repoA", IsMain: true}
	m.pickedRepo = "repoA"

	m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != modeScan {
		t.Errorf("mode = %v, want modeScan", m.mode)
	}
}

// TestUseBranchPrefersCandidateRoot guards bug 2: a scan row must resolve to
// its OWN repo root (BranchCand.Root), not the basename lookup in
// m.repoPaths, which is deduped by basename and can point at a different
// repo sharing the same base name.
func TestQuestionMarkOpensHelp(t *testing.T) {
	m := testModel()
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	if m.mode != modeHelp {
		t.Fatalf("mode = %v, want modeHelp", m.mode)
	}
}

func TestHelpKeysReturnToList(t *testing.T) {
	m := testModel()
	m.mode = modeHelp
	m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != modeList {
		t.Errorf("esc from help: mode = %v, want modeList", m.mode)
	}
	m.mode = modeHelp
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if m.mode != modeList {
		t.Errorf("q from help: mode = %v, want modeList", m.mode)
	}
}

func TestHelpViewShowsBindings(t *testing.T) {
	m := testModel()
	m.preview = previewOff
	m.mode = modeHelp
	v := m.View()
	for _, want := range []string{"ctrl-f", "ctrl-n"} {
		if !strings.Contains(v, want) {
			t.Errorf("help view missing %q", want)
		}
	}
}

// TestHelpOverlayShowsBackgroundAndHelp proves the help screen is a true
// overlay: the worktree rows from testModel() must still be present in the
// rendered output, alongside the help content, in modeHelp.
func TestHelpOverlayShowsBackgroundAndHelp(t *testing.T) {
	m := testModel()
	m.mode = modeHelp
	v := m.View()
	for _, want := range []string{"repoA", "task-one", "ork help", "ctrl-n"} {
		if !strings.Contains(v, want) {
			t.Errorf("help overlay missing %q", want)
		}
	}
}

// TestHelpOverlayNoLineExceedsWidth guards against the shearing bug: ANSI
// escapes in the background frame must never be sliced naively, or spliced
// lines can end up visually wider than m.width.
func TestHelpOverlayNoLineExceedsWidth(t *testing.T) {
	m := testModel()
	m.mode = modeHelp
	v := m.View()
	for i, line := range strings.Split(v, "\n") {
		if w := ansi.StringWidth(line); w > m.width {
			t.Errorf("line %d width = %d, want <= %d: %q", i, w, m.width, line)
		}
	}
}

func TestHelpLineKeys(t *testing.T) {
	if !strings.Contains(helpLine, "ctrl-g") {
		t.Error("helpLine missing ctrl-g")
	}
	if !strings.Contains(helpLine, "ctrl-k") {
		t.Error("helpLine missing ctrl-k")
	}
	if strings.Contains(helpLine, "ctrl-f") {
		t.Error("helpLine should not contain ctrl-f")
	}
}

func TestQuestionMarkDoesNotFilter(t *testing.T) {
	m := testModel()
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	if m.filter != "" {
		t.Errorf("filter = %q, want empty — ? must not fall through to the rune-filter branch", m.filter)
	}
}

func TestUseBranchPrefersCandidateRoot(t *testing.T) {
	m := scanModel(t)
	// Seed repoPaths with a DIFFERENT path for the same basename as the
	// candidate's Repo — simulates a basename collision across repos.
	m.repoPaths["repoA"] = "/wrong/other-repoA"
	m.pickedRepo = "repoA"

	b := worktree.BranchCand{Repo: "repoA", Root: "/correct/repoA", Name: "feat/one"}
	m.useBranch(b, true) // force: skip the conflict check
	if m.result.RepoRoot != "/correct/repoA" {
		t.Errorf("RepoRoot = %q, want candidate's Root /correct/repoA", m.result.RepoRoot)
	}
}
