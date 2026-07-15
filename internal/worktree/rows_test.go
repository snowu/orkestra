package worktree

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"orkestra/internal/config"
	"orkestra/internal/tmux"
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
		Panes:      []tmux.Pane{{Session: "mytask", Target: "mytask:0.0", CWD: beWT, Cmd: "node"}},
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
		Panes:      []tmux.Pane{{Session: "weird-name", Target: "weird-name:0.0", CWD: beWT, Cmd: "vim"}},
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
	cfg := config.Config{Pairs: []config.Pair{{FERepo: "fe-repo", BERepo: "be-repo"}}}
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
	want := []string{"fe-repo", "be-repo", "other"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
	if !PairSiblings(cfg, rows[0], rows[1]) || PairSiblings(cfg, rows[1], rows[2]) {
		t.Error("PairSiblings misdetects")
	}
}
