package ui

import (
	"errors"
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

func testModelWithGroups() *Model {
	m := New(config.Config{WorktreeRoots: []string{"/nowhere"}, Groups: []config.Group{
		{Name: "g", Processes: []config.Process{{Label: "fe", Repo: "fe"}, {Label: "be", Repo: "be"}}},
	}})
	m.width, m.height = 120, 40
	m.rows = []worktree.Row{
		{Repo: "repoA", Task: "task-one", Branch: "task-one", Path: "/w/repoA/task-one"},
	}
	m.applyFilter()
	return m
}

func TestProxyIndicatorShownOnlyWithGroups(t *testing.T) {
	v := testModel().View()
	if strings.Contains(v, "proxy :3000") {
		t.Error("proxy indicator shown with no configured groups")
	}

	mp := testModelWithGroups()
	v = mp.View()
	if !strings.Contains(v, "proxy :3000") {
		t.Error("proxy indicator missing with groups configured")
	}
}

func TestProxyIndicatorReflectsUpDown(t *testing.T) {
	m := testModelWithGroups()
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
	m := testModelWithGroups()
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

func TestGroupCountColumn(t *testing.T) {
	m := New(config.Config{WorktreeRoots: []string{"/nowhere"}})
	m.width, m.height = 120, 40
	m.rows = []worktree.Row{
		{Repo: "shell-repo", Task: "task-one", Branch: "task-one", Path: "/w/shell-repo/task-one", GroupLive: 2, GroupSize: 3},
		{Repo: "repoB", Task: "other", Branch: "fix", Path: "/w/repoB/other"},
	}
	m.applyFilter()
	v := m.View()
	if !strings.Contains(v, "2/3") {
		t.Error("expected group live count '2/3' in view")
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
		Groups: []config.Group{{Name: "g", Processes: []config.Process{
			{Label: "fe", Repo: "fe-r"}, {Label: "be", Repo: "be-r"},
		}}},
	})
	rows := []worktree.Row{
		{Repo: "fe-r", Task: "tsk", GroupName: "g"},
		{Repo: "be-r", Task: "tsk", GroupName: "g"},
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

func TestSpawnDoneMsgNotesReachStatus(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(spawnDoneMsg{notes: []string{"be: no cr-managament/mytask worktree — skipped", "remote: port 4023 already in use — skipped"}})
	nm := updated.(*Model)
	if !strings.Contains(nm.err, "already in use") || !strings.Contains(nm.err, "no cr-managament/mytask worktree") {
		t.Errorf("err = %q, want it to contain both notes joined", nm.err)
	}
}

func TestSpawnDoneMsgErrTakesPrecedenceOverNotes(t *testing.T) {
	m := testModel()
	testErr := errors.New("boom")
	updated, _ := m.Update(spawnDoneMsg{err: testErr, notes: []string{"some note"}})
	nm := updated.(*Model)
	if nm.err != testErr.Error() {
		t.Errorf("err = %q, want %q", nm.err, testErr.Error())
	}
}

func ambiguousGroupCands() []config.Group {
	return []config.Group{
		{Name: "credit-risk-mfe", Processes: []config.Process{
			{Label: "remote", Repo: "ops-web-credit-risk"},
			{Label: "host", Repo: "ops-web-core"},
			{Label: "be", Repo: "cr-managament"},
		}},
		{Name: "cr", Processes: []config.Process{
			{Label: "fe", Repo: "cr-frontend"},
			{Label: "be", Repo: "cr-managament"},
		}},
	}
}

func TestSpawnDoneMsgAmbiguousEntersGroupPick(t *testing.T) {
	m := testModel()
	cands := ambiguousGroupCands()
	updated, _ := m.Update(spawnDoneMsg{
		err:       &worktree.AmbiguousGroupError{Repo: "cr-managament", Task: "mytask", Candidates: cands},
		ambiguous: cands, pickRepo: "cr-managament", pickTask: "mytask", pickWt: "/w/cr-managament/mytask",
	})
	nm := updated.(*Model)
	if nm.mode != modeGroupPick {
		t.Fatalf("mode = %v, want modeGroupPick", nm.mode)
	}
	if len(nm.groupCands) != 2 {
		t.Fatalf("groupCands len = %d, want 2", len(nm.groupCands))
	}
	if nm.groupRepo != "cr-managament" || nm.groupTask != "mytask" || nm.groupWt != "/w/cr-managament/mytask" {
		t.Errorf("pending spawn identity not carried over: %+v", nm)
	}
}

func TestGroupPickCursorClamps(t *testing.T) {
	m := testModel()
	m.mode = modeGroupPick
	m.groupCands = ambiguousGroupCands()
	m.groupCursor = 0

	m.handleGroupPickKey(tea.KeyMsg{Type: tea.KeyUp})
	if m.groupCursor != 0 {
		t.Errorf("cursor went above 0: %d", m.groupCursor)
	}

	m.groupCursor = len(m.groupCands) - 1
	m.handleGroupPickKey(tea.KeyMsg{Type: tea.KeyDown})
	if m.groupCursor != len(m.groupCands)-1 {
		t.Errorf("cursor went past last candidate: %d", m.groupCursor)
	}
}

func TestGroupPickEscReturnsToListWithoutResult(t *testing.T) {
	m := testModel()
	m.mode = modeGroupPick
	m.groupCands = ambiguousGroupCands()

	updated, _ := m.handleGroupPickKey(tea.KeyMsg{Type: tea.KeyEsc})
	nm := updated.(*Model)
	if nm.mode != modeList {
		t.Errorf("mode = %v, want modeList", nm.mode)
	}
	if nm.result.Action != ActionQuit || nm.result.Repo != "" || nm.result.RepoRoot != "" {
		t.Errorf("result set on cancel: %+v", nm.result)
	}
}

func TestViewGroupPickRendersCandidates(t *testing.T) {
	m := testModel()
	m.mode = modeGroupPick
	m.groupCands = ambiguousGroupCands()
	m.groupRepo, m.groupTask = "cr-managament", "mytask"

	v := m.View()
	if !strings.Contains(v, "credit-risk-mfe") || !strings.Contains(v, "cr") {
		t.Errorf("view missing candidate names: %q", v)
	}
	if !strings.Contains(v, "remote(ops-web-credit-risk)") || !strings.Contains(v, "host(ops-web-core)") || !strings.Contains(v, "be(cr-managament)") {
		t.Errorf("view missing process labels/repos: %q", v)
	}
	if !strings.Contains(v, "fe(cr-frontend)") {
		t.Errorf("view missing second candidate's processes: %q", v)
	}
}

// --- ctrl-n group rows in the repo picker ---

func groupPickerModel() *Model {
	m := New(config.Config{WorktreeRoots: []string{"/nowhere"}, Groups: []config.Group{
		{Name: "credit-risk-mfe", Processes: []config.Process{
			{Label: "remote", Repo: "remote-repo"},
			{Label: "host", Repo: "host-repo"},
			{Label: "be", Repo: "be-repo"},
		}},
		{Name: "missing-member", Processes: []config.Process{
			{Label: "fe", Repo: "fe-repo"},
			{Label: "ghost", Repo: "no-such-repo"},
		}},
	}})
	m.width, m.height = 120, 40
	m.repoPaths = map[string]string{
		"remote-repo": "/w/remote-repo",
		"host-repo":   "/w/host-repo",
		"be-repo":     "/w/be-repo",
		"fe-repo":     "/w/fe-repo",
		"other-repo":  "/w/other-repo",
	}
	groupRows := m.resolvedGroupRows()
	m.repos = append(groupRows, "other-repo", "fe-repo")
	m.repoFilter, m.repoCursor = "", 0
	m.mode = modePickRepo
	return m
}

func TestPickRepoIncludesResolvedGroupOnly(t *testing.T) {
	m := groupPickerModel()
	found := false
	for _, r := range m.repos {
		if strings.HasPrefix(r, "credit-risk-mfe ") {
			found = true
		}
		if strings.HasPrefix(r, "missing-member ") {
			t.Errorf("group with unresolvable member included: %q", r)
		}
	}
	if !found {
		t.Error("resolved group row missing from repo list")
	}
}

func TestPickRepoGroupRowIsFirst(t *testing.T) {
	m := groupPickerModel()
	if len(m.repos) == 0 || !strings.HasPrefix(m.repos[0], "credit-risk-mfe") {
		t.Errorf("group row not first: %v", m.repos)
	}
}

func TestSelectingGroupRowPopulatesExtraRepoRoots(t *testing.T) {
	m := groupPickerModel()
	row := m.repos[0] // "credit-risk-mfe (remote+host+be)"
	m.pickRepoEntry(row)

	// primary is the group's first process repo
	if m.pickedRepo != "remote-repo" {
		t.Fatalf("pickedRepo = %q, want remote-repo", m.pickedRepo)
	}

	m.taskInput = "my-task"
	updated, _ := m.handleTaskNameKey(tea.KeyMsg{Type: tea.KeyEnter})
	nm := updated.(*Model)

	if nm.result.Action != ActionNewTask {
		t.Fatalf("Action = %v, want ActionNewTask", nm.result.Action)
	}
	if nm.result.RepoRoot != "/w/remote-repo" {
		t.Errorf("RepoRoot = %q, want /w/remote-repo (primary)", nm.result.RepoRoot)
	}
	want := map[string]bool{"/w/host-repo": true, "/w/be-repo": true}
	if len(nm.result.ExtraRepoRoots) != 2 {
		t.Fatalf("ExtraRepoRoots = %v, want 2 entries", nm.result.ExtraRepoRoots)
	}
	for _, r := range nm.result.ExtraRepoRoots {
		if !want[r] {
			t.Errorf("unexpected extra root %q", r)
		}
		if r == nm.result.RepoRoot {
			t.Errorf("extra root %q duplicates primary RepoRoot", r)
		}
	}
}

func TestGroupRowFuzzyFilterMatchesByName(t *testing.T) {
	m := groupPickerModel()
	m.repoFilter = "credit-risk"
	repos := m.filteredRepos()
	if len(repos) != 1 || !strings.HasPrefix(repos[0], "credit-risk-mfe") {
		t.Errorf("fuzzy filter did not match group row: %v", repos)
	}
}

func TestTaskNameScreenShowsGroupNameAndCount(t *testing.T) {
	m := groupPickerModel()
	row := m.repos[0]
	m.pickRepoEntry(row)

	v := m.viewTaskName()
	if !strings.Contains(v, "credit-risk-mfe (3 repos)") {
		t.Errorf("task-name view missing group label: %q", v)
	}
}
