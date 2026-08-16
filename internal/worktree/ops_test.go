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

func TestKillSessionForSiblingGuard(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"repoBE/mytask", "repoFE/mytask", "repoBE/solo"} {
		os.MkdirAll(filepath.Join(root, p), 0o755)
	}
	cfg := config.Config{WorktreeRoots: []string{root}}

	killed := []string{}
	ops := TmuxOps{
		Panes:       func() []mux.Pane { return nil },
		HasSession:  func(n string) bool { return n == "mytask" || n == "solo" },
		KillSession: func(n string) { killed = append(killed, n) },
	}

	// sibling exists (repoFE/mytask) → shared task session must survive
	KillSessionFor(cfg, ops, "repoBE", "mytask")
	for _, k := range killed {
		if k == "mytask" {
			t.Error("killed shared task session while sibling exists")
		}
	}

	// no sibling → task session dies
	killed = nil
	KillSessionFor(cfg, ops, "repoBE", "solo")
	found := false
	for _, k := range killed {
		if k == "solo" {
			found = true
		}
	}
	if !found {
		t.Errorf("solo session not killed: %v", killed)
	}
}

func TestKillSessionForCwdMatch(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "repoBE/mytask")
	os.MkdirAll(wt, 0o755)
	cfg := config.Config{WorktreeRoots: []string{root}}

	killed := []string{}
	ops := TmuxOps{
		Panes:       func() []mux.Pane { return []mux.Pane{{Session: "othername", CWD: wt}} },
		HasSession:  func(n string) bool { return false },
		KillSession: func(n string) { killed = append(killed, n) },
	}
	KillSessionFor(cfg, ops, "repoBE", "mytask")
	if len(killed) != 1 || killed[0] != "othername" {
		t.Errorf("cwd-matched session not killed: %v", killed)
	}
}

func TestNewTaskRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repoRoot := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", repoRoot}, args...)...)
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	os.WriteFile(filepath.Join(repoRoot, "f.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(repoRoot, ".env.local"), []byte("SECRET=1"), 0o644)
	run("add", "f.txt")
	run("commit", "-m", "init")

	wtRoot := t.TempDir()
	cfg := config.Config{
		WorktreeRoots:      []string{wtRoot},
		HooksConfig:        filepath.Join(t.TempDir(), "none.json"),
		ClaudePersonalDirs: []string{filepath.Dir(repoRoot)},
	}
	wt, err := NewTask(cfg, repoRoot, "feat-x")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(wtRoot, filepath.Base(repoRoot), "feat-x")
	if wt != want {
		t.Errorf("wt = %s, want %s", wt, want)
	}
	if GitBranch(wt) != "feat-x" {
		t.Errorf("branch = %q", GitBranch(wt))
	}
	if data, _ := os.ReadFile(filepath.Join(wt, ".env.local")); string(data) != "SECRET=1" {
		t.Error(".env.local not copied")
	}
	if data, _ := os.ReadFile(filepath.Join(wt, ".claude-profile")); string(data) != "personal" {
		t.Errorf(".claude-profile = %q", data)
	}

	// EndTask removes worktree + branch
	ops := TmuxOps{Panes: func() []mux.Pane { return nil }, HasSession: func(string) bool { return false }, KillSession: func(string) {}}
	summary := EndTask(cfg, ops, []string{repoRoot}, filepath.Base(repoRoot), "feat-x")
	if !strings.Contains(summary, "worktree removed") || !strings.Contains(summary, "branch deleted") {
		t.Errorf("summary missing expected steps: %q", summary)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Error("worktree still exists")
	}
	out, _ := exec.Command("git", "-C", repoRoot, "branch", "--list", "feat-x").Output()
	if len(out) != 0 {
		t.Errorf("branch still exists: %s", out)
	}
}

func TestTaskNameFor(t *testing.T) {
	cases := map[string]string{
		"feat/x":         "feat-x",
		"plain":          "plain",
		"a/b/c":          "a-b-c",
		"UND-329-rating": "UND-329-rating",
	}
	for in, want := range cases {
		if got := TaskNameFor(in); got != want {
			t.Errorf("TaskNameFor(%q) = %q, want %q", in, got, want)
		}
	}
}

// gitRepo builds a temp repo with one commit on main and returns its path
// plus a runner for further git commands in it.
func gitRepo(t *testing.T) (string, func(args ...string)) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", root}, args...)...)
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o644)
	run("add", "f.txt")
	run("commit", "-m", "init")
	return root, run
}

func TestBranchCheckout(t *testing.T) {
	repoRoot, run := gitRepo(t)

	// main is checked out in the primary checkout
	c := BranchCheckout(repoRoot, "main")
	if c == nil {
		t.Fatal("main should be reported as checked out")
	}
	if !c.IsMain {
		t.Error("primary checkout must set IsMain")
	}
	if c.Path != repoRoot || c.Branch != "main" {
		t.Errorf("conflict = %+v, want path %s", c, repoRoot)
	}

	// a branch with no checkout is free
	run("branch", "free-branch")
	if c := BranchCheckout(repoRoot, "free-branch"); c != nil {
		t.Errorf("free-branch should be free, got %+v", c)
	}

	// a branch held by a linked worktree is a non-main conflict
	other := filepath.Join(t.TempDir(), "linked")
	run("worktree", "add", other, "-b", "held")
	c = BranchCheckout(repoRoot, "held")
	if c == nil {
		t.Fatal("held should be reported as checked out")
	}
	if c.IsMain {
		t.Error("linked worktree must not set IsMain")
	}
	if c.Path != other {
		t.Errorf("conflict path = %s, want %s", c.Path, other)
	}

	// unknown branch is free, not an error
	if c := BranchCheckout(repoRoot, "no-such-branch"); c != nil {
		t.Errorf("unknown branch should be free, got %+v", c)
	}
}

func addExistingCfg(t *testing.T, repoRoot string) (config.Config, string) {
	t.Helper()
	wtRoot := t.TempDir()
	return config.Config{
		WorktreeRoots:      []string{wtRoot},
		HooksConfig:        filepath.Join(t.TempDir(), "none.json"),
		ClaudePersonalDirs: []string{filepath.Dir(repoRoot)},
	}, wtRoot
}

func TestAddExistingFreeBranch(t *testing.T) {
	repoRoot, run := gitRepo(t)
	os.WriteFile(filepath.Join(repoRoot, ".env.local"), []byte("SECRET=1"), 0o644)
	run("branch", "feat/x")
	cfg, wtRoot := addExistingCfg(t, repoRoot)

	wt, c, err := AddExisting(cfg, repoRoot, "feat/x", false)
	if err != nil || c != nil {
		t.Fatalf("err=%v conflict=%+v", err, c)
	}
	want := filepath.Join(wtRoot, filepath.Base(repoRoot), "feat-x")
	if wt != want {
		t.Errorf("wt = %s, want %s", wt, want)
	}
	if GitBranch(wt) != "feat/x" {
		t.Errorf("branch = %q, want feat/x", GitBranch(wt))
	}
	// the shared tail ran
	if data, _ := os.ReadFile(filepath.Join(wt, ".env.local")); string(data) != "SECRET=1" {
		t.Error(".env.local not copied")
	}
	if _, err := os.Stat(filepath.Join(wt, ".claude-profile")); err != nil {
		t.Error(".claude-profile not written")
	}
}

func TestAddExistingConflictIsReportedNotForced(t *testing.T) {
	repoRoot, run := gitRepo(t)
	cfg, _ := addExistingCfg(t, repoRoot)

	// Stage a STALE worktree entry: add a linked worktree, then delete its
	// directory from disk without telling git. `git worktree list` still
	// reports it until something runs `worktree prune`. This makes the
	// before/after snapshot below an actual regression guard — if prune
	// ever ran on the non-forced path, this entry would vanish and the
	// assertion would catch it; without a stale entry, prune is a silent
	// no-op and proves nothing.
	stale := filepath.Join(t.TempDir(), "stale")
	run("worktree", "add", stale, "-b", "stale-branch")
	os.RemoveAll(stale)

	before := gitOut(repoRoot, "worktree", "list")

	// "main" is held by the primary checkout
	wt, c, err := AddExisting(cfg, repoRoot, "main", false)
	if err != nil {
		t.Fatalf("conflict must not be an error: %v", err)
	}
	if wt != "" {
		t.Errorf("no worktree should be created, got %s", wt)
	}
	if c == nil || !c.IsMain || c.Path != repoRoot {
		t.Fatalf("conflict = %+v", c)
	}
	// repo untouched
	if GitBranch(repoRoot) != "main" {
		t.Errorf("main checkout was disturbed: on %q", GitBranch(repoRoot))
	}
	if after := gitOut(repoRoot, "worktree", "list"); after != before {
		t.Errorf("worktree list changed: before=%q after=%q", before, after)
	}
}

func TestAddExistingForceMovesMainToBase(t *testing.T) {
	repoRoot, run := gitRepo(t)
	run("checkout", "-b", "feature")
	cfg, _ := addExistingCfg(t, repoRoot)

	// BaseBranch falls back to the current branch for local-only repos, so
	// point origin/HEAD at main explicitly — the real-world shape.
	run("update-ref", "refs/remotes/origin/main", "refs/heads/main")
	run("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

	wt, c, err := AddExisting(cfg, repoRoot, "feature", true)
	if err != nil || c != nil {
		t.Fatalf("err=%v conflict=%+v", err, c)
	}
	if GitBranch(wt) != "feature" {
		t.Errorf("worktree branch = %q", GitBranch(wt))
	}
	if got := GitBranch(repoRoot); got != "main" {
		t.Errorf("main checkout = %q, want main", got)
	}
}

func TestAddExistingForceRefusesDirtyMain(t *testing.T) {
	repoRoot, run := gitRepo(t)
	run("update-ref", "refs/remotes/origin/main", "refs/heads/main")
	run("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	run("checkout", "-b", "feature")
	// diverge the branches on the same file, then dirty it — switching away
	// would have to overwrite the change, so git must refuse
	os.WriteFile(filepath.Join(repoRoot, "f.txt"), []byte("on-feature"), 0o644)
	run("commit", "-am", "feature change")
	os.WriteFile(filepath.Join(repoRoot, "f.txt"), []byte("uncommitted"), 0o644)

	cfg, _ := addExistingCfg(t, repoRoot)
	wt, _, err := AddExisting(cfg, repoRoot, "feature", true)
	if err == nil {
		t.Fatal("dirty conflicting tree must abort")
	}
	if wt != "" {
		t.Errorf("no worktree on failure, got %s", wt)
	}
	if got := GitBranch(repoRoot); got != "feature" {
		t.Errorf("repo must stay on feature, got %q", got)
	}
}

func TestAddExistingForceDetachesLinkedWorktree(t *testing.T) {
	repoRoot, run := gitRepo(t)
	other := filepath.Join(t.TempDir(), "linked")
	run("worktree", "add", other, "-b", "held")
	cfg, _ := addExistingCfg(t, repoRoot)

	wt, c, err := AddExisting(cfg, repoRoot, "held", true)
	if err != nil || c != nil {
		t.Fatalf("err=%v conflict=%+v", err, c)
	}
	if GitBranch(wt) != "held" {
		t.Errorf("new worktree branch = %q", GitBranch(wt))
	}
	// detached HEAD reports an empty branch name
	if got := GitBranch(other); got != "" {
		t.Errorf("old worktree should be detached, on %q", got)
	}
}

func TestAddExistingRefusesExistingPath(t *testing.T) {
	repoRoot, run := gitRepo(t)
	run("branch", "dup")
	cfg, wtRoot := addExistingCfg(t, repoRoot)
	existing := filepath.Join(wtRoot, filepath.Base(repoRoot), "dup")
	os.MkdirAll(existing, 0o755)

	before := gitOut(repoRoot, "worktree", "list")

	_, _, err := AddExisting(cfg, repoRoot, "dup", false)
	if err == nil {
		t.Fatal("existing target path must error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("err = %q, want it to contain %q", err.Error(), "already exists")
	}
	if after := gitOut(repoRoot, "worktree", "list"); after != before {
		t.Errorf("worktree list changed: before=%q after=%q", before, after)
	}
}
