package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"orkestra/internal/config"
	"orkestra/internal/mux"
)

func TestRepoScopedSessionsStayIndependent(t *testing.T) {
	root := fixtureRoots(t)
	cfg := config.Config{ScopeSessionsToRepo: true}
	rows := BuildRows(cfg, []string{root}, Deps{HasSession: func(name string) bool { return name == "repoFE__mytask" }, AgentState: func(string) string { return "waiting" }})
	for _, row := range rows {
		if row.Repo == "repoBE" && row.Live {
			t.Fatalf("BE borrowed FE session: %+v", row)
		}
		if row.Repo == "repoFE" && (!row.Live || row.Session != "repoFE__mytask") {
			t.Fatalf("missing scoped session: %+v", row)
		}
	}
}

func TestSharedSessionFindsLaterSiblingSubdir(t *testing.T) {
	root := fixtureRoots(t)
	rows := BuildRows(config.Config{}, []string{root}, Deps{Panes: []mux.Pane{{Session: "custom", CWD: filepath.Join(root, "repoFE/mytask/src"), Cmd: "claude"}}})
	for _, row := range rows {
		if row.Task == "mytask" && row.Session != "custom" {
			t.Fatalf("sibling missed session: %+v", row)
		}
	}
}

func TestKillSharedCwdSessionPreservesSibling(t *testing.T) {
	root := fixtureRoots(t)
	var killed []string
	ops := TmuxOps{Panes: func() []mux.Pane {
		return []mux.Pane{{Session: "mytask", CWD: filepath.Join(root, "repoBE/mytask/src")}}
	}, HasSession: func(string) bool { return true }, KillSession: func(name string) { killed = append(killed, name) }}
	KillSessionFor(config.Config{WorktreeRoots: []string{root}}, ops, "repoBE", "mytask")
	for _, name := range killed {
		if name == "mytask" {
			t.Fatal("killed shared session via cwd")
		}
	}
}

func TestMultiplePairsWithEqualTimesStayAdjacent(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{Pairs: []config.Pair{{FERepo: "a-web", BERepo: "c-api"}, {FERepo: "b-web", BERepo: "d-api"}}}
	for _, repo := range []string{"a-web", "b-web", "c-api", "d-api"} {
		os.MkdirAll(filepath.Join(root, repo, "task"), 0o755)
	}
	rows := BuildRows(cfg, []string{root}, Deps{})
	if !PairSiblings(cfg, rows[0], rows[1]) || !PairSiblings(cfg, rows[2], rows[3]) {
		t.Fatalf("pairs split: %+v", rows)
	}
}

func TestRepoAtExactScanDepth(t *testing.T) {
	home := mk(t, "a/b/repo/.git")
	dirs := AllRepoDirs(home, 3, filepath.Join(t.TempDir(), "cache"), 0)
	if len(dirs) != 1 || dirs[0] != filepath.Join(home, "a/b/repo") {
		t.Fatalf("repo at max depth omitted: %v", dirs)
	}
}

func TestLockedWorktreeCleanupPreservesEverything(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "repo")
	os.MkdirAll(repo, 0o755)
	gitCmd := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", repo}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	gitCmd("init", "-b", "main")
	gitCmd("commit", "--allow-empty", "-m", "init")
	root := filepath.Join(home, "worktrees")
	wt := filepath.Join(root, "repo", "task")
	gitCmd("worktree", "add", wt, "-b", "task")
	gitCmd("worktree", "lock", wt, "--reason", "do not remove")
	os.WriteFile(filepath.Join(wt, "untracked"), []byte("keep"), 0o600)
	killed := false
	ops := TmuxOps{Panes: func() []mux.Pane { return nil }, HasSession: func(string) bool { return true }, KillSession: func(string) { killed = true }}
	cfg := config.Config{WorktreeRoots: []string{root}}
	summary := EndTask(cfg, ops, []string{repo}, "repo", "task")
	if !strings.Contains(summary, "removal FAILED") || killed {
		t.Fatalf("cleanup: %s, killed=%v", summary, killed)
	}
	if data, err := os.ReadFile(filepath.Join(wt, "untracked")); err != nil || string(data) != "keep" {
		t.Fatal("locked worktree data lost")
	}
	gitCmd("show-ref", "--verify", "refs/heads/task")
	for _, task := range []string{"../outside", "..", "bad name", "-bad"} {
		if _, err := NewTask(cfg, repo, task); err == nil {
			t.Fatalf("invalid task accepted %q", task)
		}
	}
	if summary := EndTask(cfg, ops, nil, "repo", "task"); !strings.Contains(summary, "cleanup skipped") || killed {
		t.Fatal("missing repo did not stop cleanup")
	}
	hooksPath := filepath.Join(home, "hooks.json")
	os.WriteFile(hooksPath, []byte(`{"repo":"exit 9"}`), 0o600)
	cfg.HooksConfig = hooksPath
	if _, err := NewTask(cfg, repo, "hook-failure"); err == nil || !strings.Contains(err.Error(), "setup hook failed") {
		t.Fatalf("hook failure hidden: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "repo/hook-failure")); err != nil {
		t.Fatal("failed hook's worktree should remain for repair")
	}
}
