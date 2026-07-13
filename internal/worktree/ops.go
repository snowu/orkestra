package worktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"orkestra/internal/config"
	"orkestra/internal/hooks"
	"orkestra/internal/tmux"
)

func git(dir string, args ...string) error {
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	c.Stdout = os.Stderr
	c.Stderr = os.Stderr
	return c.Run()
}

func gitOut(dir string, args ...string) string {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// BaseBranch: origin/HEAD's symbolic ref is git's own record of the
// remote's default branch (a hardcoded "master" broke on main-default
// repos); falls back to the currently checked-out branch for local-only
// repos.
func BaseBranch(repoRoot string) string {
	b := gitOut(repoRoot, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	b = strings.TrimPrefix(b, "origin/")
	if b == "" {
		b = gitOut(repoRoot, "branch", "--show-current")
	}
	return b
}

// SessionName: task-named by default — deliberately shared across repos so
// one agent can span a BE+FE pair under the same task; repo-scoped when
// configured.
func SessionName(cfg config.Config, repo, task string) string {
	if cfg.ScopeSessionsToRepo {
		return repo + "__" + task
	}
	return task
}

// feBEDirs finds the fe/be sibling worktrees for task — separate repos
// (ORK_FE_REPO/ORK_BE_REPO), not subdirs, that share task names. Row's own
// repo/path is reused directly when it matches one side, so pressing the
// key from either the fe or the be row works without a second filesystem
// lookup.
func feBEDirs(cfg config.Config, repo, task, wt string) (feDir, beDir string, err error) {
	if cfg.FERepo == "" || cfg.BERepo == "" {
		return "", "", fmt.Errorf("ORK_FE_REPO / ORK_BE_REPO not configured in ~/.ork.conf")
	}
	switch repo {
	case cfg.FERepo:
		feDir = wt
	default:
		feDir = FindWorktree(cfg.WorktreeRoots, cfg.FERepo, task)
	}
	switch repo {
	case cfg.BERepo:
		beDir = wt
	default:
		beDir = FindWorktree(cfg.WorktreeRoots, cfg.BERepo, task)
	}
	if feDir == "" {
		return "", "", fmt.Errorf("no %s/%s worktree found", cfg.FERepo, task)
	}
	if beDir == "" {
		return "", "", fmt.Errorf("no %s/%s worktree found", cfg.BERepo, task)
	}
	return feDir, beDir, nil
}

// SpawnFEBE starts (if not already running) detached tmux sessions
// <task>_fe and <task>_be running the configured fe/be commands, rooted at
// their respective sibling worktrees. Used for background dev servers that
// don't need to be watched (hot reload does the rest).
func SpawnFEBE(cfg config.Config, repo, task, wt string) error {
	feDir, beDir, err := feBEDirs(cfg, repo, task, wt)
	if err != nil {
		return err
	}
	name := SessionName(cfg, repo, task)
	if err := tmux.SpawnDetached(name+"_fe", feDir, cfg.FECmd); err != nil {
		return err
	}
	return tmux.SpawnDetached(name+"_be", beDir, cfg.BECmd)
}

// EnsureFEBEWindows makes sure the base session for repo/task has fe/be
// windows running the configured commands, creating them if missing.
func EnsureFEBEWindows(cfg config.Config, repo, task, wt string) error {
	feDir, beDir, err := feBEDirs(cfg, repo, task, wt)
	if err != nil {
		return err
	}
	name := SessionName(cfg, repo, task)
	if err := tmux.EnsureWindow(name, "fe", feDir, cfg.FECmd); err != nil {
		return err
	}
	return tmux.EnsureWindow(name, "be", beDir, cfg.BECmd)
}

// NewTask creates <firstRoot>/<repo>/<task> as a worktree on a new branch
// off the repo's default branch, copies .env.local, runs the repo hook,
// writes .claude-profile, and touches the access marker (a fresh task
// counts as used — otherwise it'd sort to the bottom next run).
func NewTask(cfg config.Config, repoRoot, task string) (string, error) {
	repo := filepath.Base(repoRoot)
	git(repoRoot, "worktree", "prune")

	base := BaseBranch(repoRoot)
	if base == "" {
		return "", fmt.Errorf("couldn't determine a base branch (no origin/HEAD, not on a branch)")
	}
	wt := filepath.Join(cfg.WorktreeRoots[0], repo, task)
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		return "", err
	}
	if err := git(repoRoot, "worktree", "add", wt, "-b", task, base); err != nil {
		return "", fmt.Errorf("git worktree add failed for %s", wt)
	}

	if data, err := os.ReadFile(filepath.Join(repoRoot, ".env.local")); err == nil {
		os.WriteFile(filepath.Join(wt, ".env.local"), data, 0o600)
		fmt.Fprintln(os.Stderr, "Copied .env.local")
	}

	hooks.RunRepoHook(cfg.HooksConfig, repo, wt)
	WriteClaudeProfile(cfg.ClaudePersonalDirs, repoRoot, wt)
	TouchAccess(repo, task)
	return wt, nil
}

// WriteClaudeProfile marks the worktree "personal" or "work" by whether
// the repo root falls under a CLAUDE_PERSONAL_DIRS prefix — worktrees live
// outside those dirs even for personal repos, so the shell's claude()
// wrapper can't prefix-match them directly; this marker is what it reads.
func WriteClaudeProfile(personalDirs []string, repoRoot, wt string) {
	profile := "work"
	for _, d := range personalDirs {
		if d == "" {
			continue
		}
		if repoRoot == d || strings.HasPrefix(repoRoot, d+"/") {
			profile = "personal"
			break
		}
	}
	os.WriteFile(filepath.Join(wt, ".claude-profile"), []byte(profile), 0o644)
}

// TmuxOps abstracts the session-killing surface for tests.
type TmuxOps struct {
	Panes       func() []tmux.Pane
	HasSession  func(string) bool
	KillSession func(string)
}

func LiveTmuxOps() TmuxOps {
	return TmuxOps{Panes: tmux.ListPanes, HasSession: tmux.HasSession, KillSession: tmux.KillSession}
}

// KillSessionFor kills whatever session(s) belong to a worktree without
// touching the worktree/branch: any session with a pane cwd'd there, the
// repo-scoped name, and the plain task-named session — but that last one
// only when no OTHER repo's worktree under the same task name still
// exists, since task-named sessions are shared across repos by design and
// killing it would yank it from a still-active sibling.
func KillSessionFor(cfg config.Config, t TmuxOps, repo, task string) {
	wt := WorktreeOrDefault(cfg.WorktreeRoots, repo, task)

	for _, p := range t.Panes() {
		if p.CWD == wt {
			t.KillSession(p.Session)
			break
		}
	}

	if repoSess := repo + "__" + task; t.HasSession(repoSess) {
		t.KillSession(repoSess)
	}

	if t.HasSession(task) {
		hasSibling := false
		for _, root := range cfg.WorktreeRoots {
			repos, _ := os.ReadDir(root)
			for _, r := range repos {
				d := filepath.Join(root, r.Name(), task)
				if d == wt {
					continue
				}
				if st, err := os.Stat(d); err == nil && st.IsDir() {
					hasSibling = true
				}
			}
		}
		if !hasSibling {
			t.KillSession(task)
		}
	}
}

// EndTask removes the worktree, deletes the branch locally and on origin,
// clears the access marker, and kills the session LAST: when ork itself
// runs inside the session being ended, the kill also kills this process —
// with the kill first, nothing after it ever ran (branch+folder left
// behind). Killing last means cleanup is already done if we die here.
func EndTask(cfg config.Config, t TmuxOps, repos []string, repo, task string) error {
	wt := WorktreeOrDefault(cfg.WorktreeRoots, repo, task)

	repoRoot := FindRepoRoot(repos, repo)
	if repoRoot == "" {
		home, _ := os.UserHomeDir()
		repoRoot = filepath.Join(home, "code", repo)
	}
	if _, err := os.Stat(repoRoot); err == nil {
		git(repoRoot, "worktree", "remove", wt, "--force")
		git(repoRoot, "worktree", "prune")
		git(repoRoot, "branch", "-D", task)
		git(repoRoot, "push", "origin", "--delete", task)
	}
	if _, err := os.Stat(wt); err == nil {
		os.RemoveAll(wt)
	}
	os.Remove(AccessFile(repo, task))

	KillSessionFor(cfg, t, repo, task)
	return nil
}
