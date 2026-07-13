package worktree

import (
	"bufio"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	// Only rows belonging to the configured pair get fe/be windows —
	// an unrelated repo whose task name happens to exist in both sibling
	// repos must not spawn dev servers for them.
	if repo != cfg.FERepo && repo != cfg.BERepo {
		return "", "", fmt.Errorf("%s is not the fe/be pair (%s / %s)", repo, cfg.FERepo, cfg.BERepo)
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

// TaskPorts derives stable ports per task name — FE in 3000-3999, BE in
// 8000-8999 — so concurrent tasks' dev servers don't collide, and the FE
// port is predictable instead of whatever `next dev` auto-increments to.
// Same task always gets the same pair across runs, no coordination needed.
func TaskPorts(task string) (fe, be int) {
	h := fnv.New32a()
	h.Write([]byte(task))
	n := int(h.Sum32() % 1000)
	return 3000 + n, 8000 + n
}

// patchFEEnvVar rewrites (or appends) VAR=http://localhost:<port> in
// feDir/.env.local — how the fe dev server learns which port the
// task-specific backend landed on, since fe/be run as separate processes
// with no shared env.
func patchFEEnvVar(feDir, varName string, port int) error {
	path := filepath.Join(feDir, ".env.local")
	line := fmt.Sprintf("%s=http://localhost:%d", varName, port)

	f, err := os.Open(path)
	var lines []string
	found := false
	if err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			l := sc.Text()
			if strings.HasPrefix(l, varName+"=") {
				lines = append(lines, line)
				found = true
			} else {
				lines = append(lines, l)
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if !found {
		lines = append(lines, line)
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

// subPort substitutes a {port} placeholder in cmd, if present.
func subPort(cmd string, port int) string {
	return strings.ReplaceAll(cmd, "{port}", strconv.Itoa(port))
}

// prepFEBE resolves fe/be dirs, derives the task's ports ({port} in FECmd
// gets the fe port, in BECmd the be port), and patches the fe env var (if
// configured) to point at the be port — the setup shared before actually
// starting either process.
func prepFEBE(cfg config.Config, repo, task, wt string) (feDir, beDir, feCmd, beCmd string, err error) {
	feDir, beDir, err = feBEDirs(cfg, repo, task, wt)
	if err != nil {
		return "", "", "", "", err
	}
	fePort, bePort := TaskPorts(task)
	if cfg.FEEnvVar != "" {
		if err := patchFEEnvVar(feDir, cfg.FEEnvVar, bePort); err != nil {
			return "", "", "", "", err
		}
	}
	return feDir, beDir, subPort(cfg.FECmd, fePort), subPort(cfg.BECmd, bePort), nil
}

// EnsureFEBEWindows makes sure the base session for repo/task exists and has
// fe/be windows running the configured commands, creating whatever's
// missing. Used both for ctrl-g (spawn in background, no attach after) and
// ctrl-a (attach once this returns) — fe/be always live as windows in the
// SAME session as the base one, never separate sessions, so switching
// windows (ctrl-b 1/2/3) or attaching shows all three together and killing
// the base session takes fe/be down with it.
func EnsureFEBEWindows(cfg config.Config, repo, task, wt string) error {
	feDir, beDir, fe, be, err := prepFEBE(cfg, repo, task, wt)
	if err != nil {
		return err
	}
	name := SessionName(cfg, repo, task)
	if err := tmux.EnsureSession(name, wt); err != nil {
		return err
	}
	if err := tmux.EnsureWindow(name, "fe", feDir, fe); err != nil {
		return err
	}
	return tmux.EnsureWindow(name, "be", beDir, be)
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
