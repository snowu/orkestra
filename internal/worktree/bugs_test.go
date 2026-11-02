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

func TestMultipleGroupsWithEqualTimesStayAdjacent(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{Groups: []config.Group{
		{Name: "g1", Processes: []config.Process{{Label: "fe", Repo: "a-web"}, {Label: "be", Repo: "c-api"}}},
		{Name: "g2", Processes: []config.Process{{Label: "fe", Repo: "b-web"}, {Label: "be", Repo: "d-api"}}},
	}}
	for _, repo := range []string{"a-web", "b-web", "c-api", "d-api"} {
		os.MkdirAll(filepath.Join(root, repo, "task"), 0o755)
	}
	rows := BuildRows(cfg, []string{root}, Deps{})
	if !GroupSiblings(cfg, rows[0], rows[1]) || !GroupSiblings(cfg, rows[2], rows[3]) {
		t.Fatalf("groups split: %+v", rows)
	}
	// within a cluster, the group's declared process order
	if rows[0].Repo != "a-web" || rows[1].Repo != "c-api" {
		t.Fatalf("cluster order = %s, %s", rows[0].Repo, rows[1].Repo)
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
	// A failing setup hook is reported but the worktree is finished:
	// profile written, path returned, so the user can repair it.
	hooksPath := filepath.Join(home, "hooks.json")
	os.WriteFile(hooksPath, []byte(`{"repo":"exit 9"}`), 0o600)
	cfg.HooksConfig = hooksPath
	wt2, err := NewTask(cfg, repo, "hook-failure")
	if err != nil || wt2 != filepath.Join(root, "repo/hook-failure") {
		t.Fatalf("hook failure must not fail NewTask: %q %v", wt2, err)
	}
	if _, err := os.Stat(filepath.Join(wt2, ".claude-profile")); err != nil {
		t.Fatal("profile must still be written after a hook failure")
	}
}

func TestEndTaskUntrackedFolderAndReusedBranch(t *testing.T) {
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
	cfg := config.Config{WorktreeRoots: []string{root}}
	ops := TmuxOps{Panes: func() []mux.Pane { return nil }, HasSession: func(string) bool { return false }, KillSession: func(string) {}}

	// A folder git doesn't track: removed directly instead of stuck forever.
	stray := filepath.Join(root, "repo", "stray")
	os.MkdirAll(stray, 0o755)
	if s := EndTask(cfg, ops, []string{repo}, "repo", "stray"); !strings.Contains(s, "untracked folder removed") {
		t.Fatalf("summary = %s", s)
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatal("untracked folder left behind")
	}

	// A reused feat/login branch lives in task dir feat-login: EndTask must
	// delete feat/login, not a (nonexistent) feat-login branch.
	if _, err := NewTaskOnBranch(cfg, repo, "feat-login", "feat/login"); err != nil {
		t.Fatal(err)
	}
	if s := EndTask(cfg, ops, []string{repo}, "repo", "feat-login"); !strings.Contains(s, "branch feat/login deleted") || strings.Contains(s, "branch feat/login deleted FAILED") {
		t.Fatalf("summary = %s", s)
	}
	if HasBranch(repo, "feat/login") {
		t.Fatal("reused branch not deleted")
	}
}

func TestSessionsForMatchesKill(t *testing.T) {
	root := fixtureRoots(t)
	panes := []mux.Pane{{Session: "main", CWD: filepath.Join(root, "repoBE/mytask/src")}}
	var killed []string
	ops := TmuxOps{Panes: func() []mux.Pane { return panes },
		HasSession: func(string) bool { return false }, KillSession: func(n string) { killed = append(killed, n) }}
	cfg := config.Config{WorktreeRoots: []string{root}}
	doomed := SessionsFor(cfg, ops, "repoBE", "mytask")
	KillSessionFor(cfg, ops, "repoBE", "mytask")
	if strings.Join(doomed, ",") != "main" || strings.Join(killed, ",") != "main" {
		t.Fatalf("doomed=%v killed=%v", doomed, killed)
	}
}
