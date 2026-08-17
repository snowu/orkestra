package worktree

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"orkestra/internal/config"
	"orkestra/internal/mux"
)

func fixtureRoots(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range []string{"repoBE/mytask", "repoFE/mytask", "repoBE/other"} {
		os.MkdirAll(filepath.Join(root, p), 0o755)
	}
	return root
}

func TestSiblingsShareSessionAndAgent(t *testing.T) {
	root := fixtureRoots(t)
	beWT := filepath.Join(root, "repoBE/mytask")
	stateReads := 0
	d := Deps{
		Panes:      []mux.Pane{{Session: "mytask", Target: "mytask:0.0", CWD: beWT, Cmd: "node"}},
		HasSession: func(n string) bool { return n == "mytask" },
		AgentState: func(s string) string { stateReads++; return "running" },
	}
	rows := BuildRows(config.Config{}, []string{root}, d)
	byRepoTask := map[string]Row{}
	for _, r := range rows {
		byRepoTask[r.Repo+"/"+r.Task] = r
	}
	be, fe := byRepoTask["repoBE/mytask"], byRepoTask["repoFE/mytask"]
	if !be.Live || !fe.Live {
		t.Fatalf("both siblings must be live: %+v %+v", be, fe)
	}
	if be.Session != "mytask" || fe.Session != "mytask" {
		t.Errorf("sessions differ: %q %q", be.Session, fe.Session)
	}
	if be.Agent != "running" || fe.Agent != "running" {
		t.Errorf("agents differ: %q %q", be.Agent, fe.Agent)
	}
	if stateReads != 1 {
		t.Errorf("agent state read %d times, want once per task", stateReads)
	}
	if other := byRepoTask["repoBE/other"]; other.Live {
		t.Errorf("task without session must be idle: %+v", other)
	}
}

func TestCwdMatchBeatsNameMatch(t *testing.T) {
	root := fixtureRoots(t)
	beWT := filepath.Join(root, "repoBE/mytask")
	d := Deps{
		Panes:      []mux.Pane{{Session: "weird-name", Target: "weird-name:0.0", CWD: beWT, Cmd: "vim"}},
		HasSession: func(n string) bool { return true }, // name-match would also succeed
	}
	rows := BuildRows(config.Config{}, []string{root}, d)
	for _, r := range rows {
		if r.Task == "mytask" && r.Session != "weird-name" {
			t.Errorf("cwd match should win: got session %q", r.Session)
		}
	}
}

func TestSortByAccessDescNeverLast(t *testing.T) {
	root := fixtureRoots(t)
	now := time.Now()
	access := map[string]time.Time{
		"repoBE/other":  now,
		"repoBE/mytask": now.Add(-time.Hour),
		// repoFE/mytask never used → zero time
	}
	d := Deps{AccessTime: func(repo, task string) time.Time { return access[repo+"/"+task] }}
	rows := BuildRows(config.Config{}, []string{root}, d)
	if rows[0].Task != "other" {
		t.Errorf("most recent first, got %s/%s", rows[0].Repo, rows[0].Task)
	}
	last := rows[len(rows)-1]
	if last.Repo != "repoFE" {
		t.Errorf("never-used last, got %s/%s", last.Repo, last.Task)
	}
}

func TestPairRowsSortAdjacent(t *testing.T) {
	cfg := config.Config{Groups: []config.Group{{Name: "g", Processes: []config.Process{
		{Label: "fe", Repo: "fe-repo"}, {Label: "be", Repo: "be-repo"},
	}}}}
	t0 := time.Now()
	times := map[string]time.Time{
		"fe-repo__tsk":  t0.Add(-3 * time.Hour), // fe old
		"other__mid":    t0.Add(-1 * time.Hour), // would split the pair without grouping
		"be-repo__tsk":  t0,                     // be fresh
	}
	root := t.TempDir()
	for _, p := range []string{"fe-repo/tsk", "be-repo/tsk", "other/mid"} {
		if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rows := BuildRows(cfg, []string{root}, Deps{
		AccessTime: func(repo, task string) time.Time { return times[repo+"__"+task] },
	})
	var order []string
	for _, r := range rows {
		order = append(order, r.Repo)
	}
	// Group siblings must sort adjacent (both members of "g" before the
	// unrelated "other" row); exact fe/be order within the tie is no
	// longer meaningful — groups are N-process, not fe/be-shaped.
	if len(order) != 3 || order[2] != "other" {
		t.Fatalf("order = %v, want group members adjacent with other last", order)
	}
	if !GroupSiblings(cfg, rows[0], rows[1]) || GroupSiblings(cfg, rows[1], rows[2]) {
		t.Error("GroupSiblings misdetects")
	}
}

func TestGroupLiveCount(t *testing.T) {
	cfg := config.Config{Groups: []config.Group{{
		Name: "mfe",
		Processes: []config.Process{
			{Label: "shell", Repo: "shell-repo"},
			{Label: "cart", Repo: "cart-repo"},
			{Label: "checkout", Repo: "checkout-repo"},
		},
	}}}
	root := t.TempDir()
	for _, p := range []string{"shell-repo/tsk", "solo-repo/tsk"} {
		if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	d := Deps{
		SessionWindows: func(session string) []string { return []string{"shell", "cart"} },
	}
	rows := BuildRows(cfg, []string{root}, d)
	byRepo := map[string]Row{}
	for _, r := range rows {
		byRepo[r.Repo] = r
	}
	shell := byRepo["shell-repo"]
	if shell.GroupSize != 3 || shell.GroupLive != 2 {
		t.Errorf("shell-repo row = GroupLive=%d GroupSize=%d, want 2/3", shell.GroupLive, shell.GroupSize)
	}
	solo := byRepo["solo-repo"]
	if solo.GroupSize != 0 {
		t.Errorf("solo-repo row GroupSize = %d, want 0", solo.GroupSize)
	}
}

func TestGroupSiblings(t *testing.T) {
	cfg := config.Config{Groups: []config.Group{{
		Name: "mfe",
		Processes: []config.Process{
			{Label: "shell", Repo: "shell-repo"},
			{Label: "cart", Repo: "cart-repo"},
			{Label: "checkout", Repo: "checkout-repo"},
		},
	}}}
	a := Row{Repo: "shell-repo", Task: "tsk", GroupName: "mfe"}
	b := Row{Repo: "cart-repo", Task: "tsk", GroupName: "mfe"}
	c := Row{Repo: "checkout-repo", Task: "tsk", GroupName: "mfe"}
	if !GroupSiblings(cfg, a, b) || !GroupSiblings(cfg, b, c) {
		t.Error("same group, same task should be siblings")
	}
	bOtherTask := Row{Repo: "cart-repo", Task: "other-tsk", GroupName: "mfe"}
	if GroupSiblings(cfg, a, bOtherTask) {
		t.Error("different tasks must not be siblings")
	}
	unrelated := Row{Repo: "unrelated-repo", Task: "tsk"}
	if GroupSiblings(cfg, a, unrelated) {
		t.Error("unrelated repo must not be a sibling")
	}
}

// TestResolveGroupPicksMoreMaterialized: cr-managament is a member of both
// "cr" and "credit-risk-mfe". Only credit-risk-mfe has 2 of its 2 member
// worktrees present for UND-329, cr has 0 of its 2 — so ResolveGroup must
// pick credit-risk-mfe, unambiguously.
func TestResolveGroupPicksMoreMaterialized(t *testing.T) {
	cfg := config.Config{Groups: []config.Group{
		{Name: "cr", Processes: []config.Process{
			{Label: "fe", Repo: "cr-frontend"}, {Label: "be", Repo: "cr-managament"},
		}},
		{Name: "credit-risk-mfe", Processes: []config.Process{
			{Label: "host", Repo: "credit-risk-host"}, {Label: "be", Repo: "cr-managament"},
		}},
	}}
	root := t.TempDir()
	for _, p := range []string{"credit-risk-host/UND-329", "cr-managament/UND-329"} {
		if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	g, ambiguous, ok := ResolveGroup(cfg, []string{root}, "cr-managament", "UND-329")
	if !ok || ambiguous != nil {
		t.Fatalf("expected unambiguous resolution, got g=%+v ambiguous=%+v ok=%v", g, ambiguous, ok)
	}
	if g.Name != "credit-risk-mfe" {
		t.Errorf("resolved to %q, want credit-risk-mfe", g.Name)
	}
}

// TestResolveGroupTiesAreAmbiguous: both candidate groups have exactly 1
// of 2 member worktrees present — a genuine tie, so ResolveGroup must
// refuse to guess and report both as ambiguous.
func TestResolveGroupTiesAreAmbiguous(t *testing.T) {
	cfg := config.Config{Groups: []config.Group{
		{Name: "a", Processes: []config.Process{
			{Label: "x", Repo: "shared"}, {Label: "y", Repo: "only-a"},
		}},
		{Name: "b", Processes: []config.Process{
			{Label: "x", Repo: "shared"}, {Label: "z", Repo: "only-b"},
		}},
	}}
	root := t.TempDir()
	// Neither only-a nor only-b has a worktree — both candidates tie at 1
	// present member (shared itself, found via its own worktree).
	if err := os.MkdirAll(filepath.Join(root, "shared/tsk"), 0o755); err != nil {
		t.Fatal(err)
	}
	g, ambiguous, ok := ResolveGroup(cfg, []string{root}, "shared", "tsk")
	if !ok {
		t.Fatal("shared is in groups, should resolve ok=true")
	}
	if len(ambiguous) != 2 {
		t.Fatalf("expected both tied candidates in ambiguous, got %+v", ambiguous)
	}
	if g.Name != ambiguous[0].Name {
		t.Errorf("returned group should be the first tied candidate")
	}
}

// TestResolveGroupNoGroups: a repo in no group must resolve ok=false.
func TestResolveGroupNoGroups(t *testing.T) {
	cfg := config.Config{}
	root := t.TempDir()
	_, ambiguous, ok := ResolveGroup(cfg, []string{root}, "nope", "tsk")
	if ok || ambiguous != nil {
		t.Errorf("repo in no group must resolve ok=false, ambiguous=nil; got ok=%v ambiguous=%+v", ok, ambiguous)
	}
}

// TestBuildRowsPopulatesGroupName verifies a row's GroupName is populated
// by BuildRows from the resolved group, without needing SessionWindows
// wired up.
func TestBuildRowsPopulatesGroupName(t *testing.T) {
	cfg := config.Config{Groups: []config.Group{{
		Name: "mfe",
		Processes: []config.Process{
			{Label: "shell", Repo: "shell-repo"},
			{Label: "cart", Repo: "cart-repo"},
		},
	}}}
	root := t.TempDir()
	for _, p := range []string{"shell-repo/tsk", "solo-repo/tsk"} {
		if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rows := BuildRows(cfg, []string{root}, Deps{})
	byRepo := map[string]Row{}
	for _, r := range rows {
		byRepo[r.Repo] = r
	}
	if byRepo["shell-repo"].GroupName != "mfe" {
		t.Errorf("shell-repo GroupName = %q, want mfe", byRepo["shell-repo"].GroupName)
	}
	if byRepo["solo-repo"].GroupName != "" {
		t.Errorf("solo-repo GroupName = %q, want empty", byRepo["solo-repo"].GroupName)
	}
}

// TestThreeMemberGroupSortsFullyAdjacent guards the picker's link bracket
// for N>2-process groups: an unrelated repo whose name falls
// alphabetically between two group members' names (here "other" sits
// between "checkout-repo" and "shell-repo") must never split the group's
// visual bracket — every member has to land in one contiguous run.
func TestThreeMemberGroupSortsFullyAdjacent(t *testing.T) {
	cfg := config.Config{Groups: []config.Group{{
		Name: "mfe",
		Processes: []config.Process{
			{Label: "shell", Repo: "shell-repo"},
			{Label: "cart", Repo: "cart-repo"},
			{Label: "checkout", Repo: "checkout-repo"},
		},
	}}}
	root := t.TempDir()
	for _, p := range []string{"shell-repo/UND-1", "cart-repo/UND-1", "checkout-repo/UND-1", "other/UND-1"} {
		if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rows := BuildRows(cfg, []string{root}, Deps{})
	links := 0
	for i := 0; i < len(rows)-1; i++ {
		if GroupSiblings(cfg, rows[i], rows[i+1]) {
			links++
		}
	}
	if links != 2 {
		var order []string
		for _, r := range rows {
			order = append(order, r.Repo)
		}
		t.Errorf("expected 2 adjacent sibling links (3 members in one contiguous run), got %d; order = %v", links, order)
	}
}

// TestSharedRepoRowsAgreeOnGroup guards against per-row-independent group
// resolution: cr-managament is a member of both "cr" and "credit-risk-mfe"
// (real config shape from this feature's design doc). When only cr and
// its sibling cr-frontend have worktrees for a task, BOTH rows must land
// on the SAME GroupName ("cr") and be reported as siblings — resolving
// each row's group independently let cr-frontend and cr-managament
// disagree (or a shared repo disagree with its OWN group's sibling)
// whenever a tie-break differed row to row, which is what silently broke
// the picker's pairing bracket/coloring for rows that were, in fact,
// supposed to move together.
func TestSharedRepoRowsAgreeOnGroup(t *testing.T) {
	cfg := config.Config{Groups: []config.Group{
		{Name: "cr", Processes: []config.Process{
			{Label: "fe", Repo: "cr-frontend"}, {Label: "be", Repo: "cr-managament"},
		}},
		{Name: "credit-risk-mfe", Processes: []config.Process{
			{Label: "host", Repo: "credit-risk-host"}, {Label: "be", Repo: "cr-managament"},
		}},
	}}
	root := t.TempDir()
	for _, p := range []string{"cr-frontend/UND-1", "cr-managament/UND-1"} {
		if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rows := BuildRows(cfg, []string{root}, Deps{})
	byRepo := map[string]Row{}
	for _, r := range rows {
		byRepo[r.Repo] = r
	}
	fe, be := byRepo["cr-frontend"], byRepo["cr-managament"]
	if fe.GroupName != "cr" || be.GroupName != "cr" {
		t.Fatalf("both rows must agree on group=cr, got fe=%q be=%q", fe.GroupName, be.GroupName)
	}
	if !GroupSiblings(cfg, fe, be) {
		t.Error("cr-frontend and cr-managament must be reported as siblings")
	}
}

// TestSharedRepoTaskLevelTieDeclinesRatherThanGuessing: when BOTH
// candidate groups sharing cr-managament are equally materialized for one
// task (2/2 present each), there's no principled way to pick a bracket to
// draw — resolveTaskGroup must decline for every row of that task
// (GroupName == "" everywhere) instead of some rows getting one group's
// name and others getting the other's.
func TestSharedRepoTaskLevelTieDeclinesRatherThanGuessing(t *testing.T) {
	cfg := config.Config{Groups: []config.Group{
		{Name: "cr", Processes: []config.Process{
			{Label: "fe", Repo: "cr-frontend"}, {Label: "be", Repo: "cr-managament"},
		}},
		{Name: "credit-risk-mfe", Processes: []config.Process{
			{Label: "host", Repo: "credit-risk-host"}, {Label: "be", Repo: "cr-managament"},
		}},
	}}
	root := t.TempDir()
	for _, p := range []string{"cr-frontend/UND-1", "credit-risk-host/UND-1", "cr-managament/UND-1"} {
		if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rows := BuildRows(cfg, []string{root}, Deps{})
	for _, r := range rows {
		if r.GroupName != "" {
			t.Errorf("expected no GroupName on a task-level tie, got %s=%q", r.Repo, r.GroupName)
		}
	}
}
