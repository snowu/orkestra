package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"orkestra/internal/config"
	"orkestra/internal/tmux"
)

func TestKillSessionForSiblingGuard(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"repoBE/mytask", "repoFE/mytask", "repoBE/solo"} {
		os.MkdirAll(filepath.Join(root, p), 0o755)
	}
	cfg := config.Config{WorktreeRoots: []string{root}}

	killed := []string{}
	ops := TmuxOps{
		Panes:       func() []tmux.Pane { return nil },
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
		Panes:       func() []tmux.Pane { return []tmux.Pane{{Session: "othername", CWD: wt}} },
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
	ops := TmuxOps{Panes: func() []tmux.Pane { return nil }, HasSession: func(string) bool { return false }, KillSession: func(string) {}}
	if err := EndTask(cfg, ops, []string{repoRoot}, filepath.Base(repoRoot), "feat-x"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Error("worktree still exists")
	}
	out, _ := exec.Command("git", "-C", repoRoot, "branch", "--list", "feat-x").Output()
	if len(out) != 0 {
		t.Errorf("branch still exists: %s", out)
	}
}
