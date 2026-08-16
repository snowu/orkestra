package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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
		{Repo: "repoA", Name: "feat/one", Tip: time.Now().Add(-2 * time.Hour)},
		{Repo: "repoB", Name: "fix-two", Tip: time.Now().Add(-30 * time.Hour)},
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
