package worktree

import (
	"bufio"
	"bytes"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"orkestra/internal/config"
	"orkestra/internal/hooks"
	"orkestra/internal/mux"
)

// Log receives subprocess output (git, hooks). Defaults to stderr for CLI
// use; the TUI swaps in io.Discard — raw git output on stderr while
// bubbletea is drawing there shears the whole layout.
var Log io.Writer = os.Stderr

func git(dir string, args ...string) error {
	var buf bytes.Buffer
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	c.Stdout = io.MultiWriter(Log, &buf)
	c.Stderr = io.MultiWriter(Log, &buf)
	if err := c.Run(); err != nil {
		// Log may be io.Discard (TUI) — carry the output in the error so
		// failures still surface somewhere instead of vanishing.
		return fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(buf.String()))
	}
	return nil
}

func gitOut(dir string, args ...string) string {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Conflict describes a branch that is already checked out somewhere.
// Resolving it is destructive, so it is reported to the caller rather
// than silently forced.
type Conflict struct {
	Branch string
	Path   string // the checkout currently holding the branch
	IsMain bool   // Path is the primary checkout, not a linked worktree
}

// BranchCheckout reports where branch is checked out, or nil if it is
// free. Read-only — the caller decides whether to disturb anything.
//
// `git worktree list --porcelain` emits records separated by blank lines,
// each starting with a "worktree <path>" line; the FIRST record is always
// the primary checkout, which is how IsMain is determined without a
// separate rev-parse.
func BranchCheckout(repoRoot, branch string) *Conflict {
	out := gitOut(repoRoot, "worktree", "list", "--porcelain")
	if out == "" {
		return nil
	}
	want := "branch refs/heads/" + branch
	path, first := "", true
	isMain := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
			isMain = first
			first = false
		case line == want:
			return &Conflict{Branch: branch, Path: path, IsMain: isMain}
		}
	}
	return nil
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

// TaskNameFor maps a branch name to an ork task name. A task is both a
// directory name and a multiplexer session name, so slashes cannot
// survive; Row.Branch still shows the real branch, keeping the mapping
// visible in the picker.
func TaskNameFor(branch string) string {
	return strings.ReplaceAll(branch, "/", "-")
}

// feBEDirs finds the fe/be sibling worktrees for task — separate repos
// (ORK_FE_REPO/ORK_BE_REPO), not subdirs, that share task names. Row's own
// repo/path is reused directly when it matches one side, so pressing the
// key from either the fe or the be row works without a second filesystem
// lookup.
func feBEDirs(cfg config.Config, pair config.Pair, repo, task, wt string) (feDir, beDir string, err error) {
	switch repo {
	case pair.FERepo:
		feDir = wt
	default:
		feDir = FindWorktree(cfg.WorktreeRoots, pair.FERepo, task)
	}
	switch repo {
	case pair.BERepo:
		beDir = wt
	default:
		beDir = FindWorktree(cfg.WorktreeRoots, pair.BERepo, task)
	}
	if feDir == "" {
		return "", "", fmt.Errorf("no %s/%s worktree found", pair.FERepo, task)
	}
	if beDir == "" {
		return "", "", fmt.Errorf("no %s/%s worktree found", pair.BERepo, task)
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
	// 3000 is reserved for the login proxy (OAuth redirect whitelist) — a
	// task hashing to slot 0 takes slot 999 instead. Only that one slot
	// remaps, so every other task keeps its established port.
	if n == 0 {
		n = 999
	}
	return 3000 + n, 8000 + n
}

// patchFEEnvVar rewrites (or appends) VAR=http://localhost:<port> in
// feDir/.env.local — how the fe dev server learns which port the
// task-specific backend landed on, since fe/be run as separate processes
// with no shared env.
func patchFEEnvVar(feDir, varName, urlPath string, port int) error {
	return patchEnvVar(feDir, varName, fmt.Sprintf("http://localhost:%d%s", port, urlPath))
}

// patchEnvVar rewrites (or appends) VAR=value in dir/.env.local.
func patchEnvVar(dir, varName, value string) error {
	path := filepath.Join(dir, ".env.local")
	line := varName + "=" + value

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
	// Only rows belonging to a configured pair get fe/be windows —
	// an unrelated repo whose task name happens to exist in both sibling
	// repos must not spawn dev servers for them.
	pair, ok := cfg.PairFor(repo)
	if !ok {
		if len(cfg.Pairs) == 0 {
			return "", "", "", "", fmt.Errorf("no fe/be pairs configured (~/.ork.conf or ~/.config/ork/pairs.json)")
		}
		return "", "", "", "", fmt.Errorf("%s is not part of any configured fe/be pair", repo)
	}
	feDir, beDir, err = feBEDirs(cfg, pair, repo, task, wt)
	if err != nil {
		return "", "", "", "", err
	}
	fePort, bePort := TaskPorts(task)
	if pair.FEEnvVar != "" {
		if err := patchFEEnvVar(feDir, pair.FEEnvVar, pair.FEEnvPath, bePort); err != nil {
			return "", "", "", "", err
		}
	}
	// Task name exposed to the fe app so it can label itself (e.g. browser
	// tab title "[task] app") — otherwise every task's tab reads identically
	// and only the port distinguishes them. Best-effort: the fe may ignore it.
	if err := patchEnvVar(feDir, "NEXT_PUBLIC_ORK_TASK", task); err != nil {
		return "", "", "", "", err
	}
	// Apps that hardcode their own origin (NEXTAUTH_URL etc.) must learn
	// the task's real fe port, or auth redirects land on localhost:3000.
	for _, v := range pair.FEURLEnvVars {
		if err := patchEnvVar(feDir, v, fmt.Sprintf("http://localhost:%d", fePort)); err != nil {
			return "", "", "", "", err
		}
	}
	return feDir, beDir, subPort(pair.FECmd, fePort), subPort(pair.BECmd, bePort), nil
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
	if err := mux.EnsureSession(name, wt); err != nil {
		return err
	}
	if err := mux.EnsureWindow(name, "fe", feDir, fe); err != nil {
		return err
	}
	return mux.EnsureWindow(name, "be", beDir, be)
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
	finishWorktree(cfg, repoRoot, repo, task, wt)
	return wt, nil
}

// AddExisting creates <firstRoot>/<repo>/<TaskNameFor(branch)> as a
// worktree on an EXISTING branch.
//
// When the branch is already checked out somewhere, the conflict is
// RETURNED and nothing is mutated — resolving it disturbs another
// checkout, which is the user's call. The caller confirms, then calls
// again with force=true.
func AddExisting(cfg config.Config, repoRoot, branch string, force bool) (string, *Conflict, error) {
	repo := filepath.Base(repoRoot)
	task := TaskNameFor(branch)

	wt := filepath.Join(cfg.WorktreeRoots[0], repo, task)
	if _, err := os.Stat(wt); err == nil {
		return "", nil, fmt.Errorf("%s already exists", wt)
	}

	var resolved string // describes a force-resolved conflict, for error context below
	if c := BranchCheckout(repoRoot, branch); c != nil {
		if !force {
			return "", c, nil
		}
		if c.IsMain {
			// Leave the primary checkout on a real branch rather than
			// detached: later commits there would otherwise be orphan-prone.
			base := BaseBranch(repoRoot)
			if base == "" {
				return "", nil, fmt.Errorf("couldn't determine a base branch for %s", repoRoot)
			}
			if base == branch {
				return "", nil, fmt.Errorf("%s is %s's base branch — it must stay checked out there", branch, repo)
			}
			if err := git(c.Path, "checkout", base); err != nil {
				return "", nil, fmt.Errorf("couldn't switch %s to %s: %w", c.Path, base, err)
			}
			resolved = fmt.Sprintf("%s was already switched to %s", c.Path, base)
		} else if err := git(c.Path, "checkout", "--detach"); err != nil {
			return "", nil, fmt.Errorf("couldn't detach %s: %w", c.Path, err)
		} else {
			resolved = fmt.Sprintf("%s was already detached", c.Path)
		}
	}

	// Every mutating path from here on reaches this prune; no early return
	// does, so the non-forced path never mutates anything (git self-heals
	// worktree list for missing paths, so nothing depends on an early prune).
	git(repoRoot, "worktree", "prune")

	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		return "", nil, err
	}
	if err := git(repoRoot, "worktree", "add", wt, branch); err != nil {
		if resolved != "" {
			return "", nil, fmt.Errorf("couldn't create %s: %w; note: %s", wt, err, resolved)
		}
		return "", nil, fmt.Errorf("git worktree add failed for %s: %w", wt, err)
	}
	finishWorktree(cfg, repoRoot, repo, task, wt)
	return wt, nil, nil
}

// finishWorktree is the shared post-creation tail of NewTask and
// AddExisting: seed config, run the repo's setup hook, mark the profile,
// and count the worktree as used (a fresh task that sorted to the bottom
// of the picker was the bug this last line prevents).
func finishWorktree(cfg config.Config, repoRoot, repo, task, wt string) {
	if data, err := os.ReadFile(filepath.Join(repoRoot, ".env.local")); err == nil {
		os.WriteFile(filepath.Join(wt, ".env.local"), data, 0o600)
		fmt.Fprintln(Log, "Copied .env.local")
	}
	hooks.RunRepoHook(cfg.HooksConfig, repo, wt)
	WriteClaudeProfile(cfg.ClaudePersonalDirs, repoRoot, wt)
	TouchAccess(repo, task)
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
	Panes       func() []mux.Pane
	HasSession  func(string) bool
	KillSession func(string)
}

func LiveTmuxOps() TmuxOps {
	return TmuxOps{Panes: mux.ListPanes, HasSession: mux.HasSession, KillSession: mux.KillSession}
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
// EndTask is best-effort by design (a branch that was never pushed makes
// `push origin --delete` fail — that must not abort the rest), so instead
// of an error it returns a summary of what each step actually did, for the
// TUI's status line / CLI output.
func EndTask(cfg config.Config, t TmuxOps, repos []string, repo, task string) string {
	wt := WorktreeOrDefault(cfg.WorktreeRoots, repo, task)
	var steps []string
	step := func(label string, err error) {
		if err != nil {
			steps = append(steps, label+" FAILED")
		} else {
			steps = append(steps, label)
		}
	}

	repoRoot := FindRepoRoot(repos, repo)
	if repoRoot == "" {
		home, _ := os.UserHomeDir()
		repoRoot = filepath.Join(home, "code", repo)
	}
	if _, err := os.Stat(repoRoot); err == nil {
		step("worktree removed", git(repoRoot, "worktree", "remove", wt, "--force"))
		git(repoRoot, "worktree", "prune")
		step("branch deleted", git(repoRoot, "branch", "-D", task))
		step("origin branch deleted", git(repoRoot, "push", "origin", "--delete", task))
	} else {
		steps = append(steps, "repo root not found ("+repoRoot+") — git cleanup skipped")
	}
	if _, err := os.Stat(wt); err == nil {
		step("folder removed", os.RemoveAll(wt))
	}
	os.Remove(AccessFile(repo, task))

	KillSessionFor(cfg, t, repo, task)
	steps = append(steps, "session killed")
	return repo + "/" + task + ": " + strings.Join(steps, " · ")
}
