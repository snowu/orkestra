package worktree

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	// Identical writers let os/exec serialize both streams through one
	// copy goroutine; separate MultiWriters race on the shared buffer.
	c.Stderr = c.Stdout
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

// PairPlan is the resolved setup used by both the CLI preview and launcher.
// Building a plan never writes files or starts processes.
type PairPlan struct {
	FERepo, BERepo, FEDir, BEDir, FECmd, BECmd string
	FEPort, BEPort                             int
	EnvFile                                    string
	Env                                        map[string]string
	PatchFile, PatchKey                        string
	patchData                                  []byte
	patchMode                                  os.FileMode
}

func PlanPair(cfg config.Config, repo, task, wt string) (PairPlan, error) {
	pair, ok := cfg.PairFor(repo)
	if !ok {
		return PairPlan{}, fmt.Errorf("%s has no configured pair; set ORK_PAIRS_CONFIG (see ork config check)", repo)
	}
	if err := pair.Validate(); err != nil {
		return PairPlan{}, err
	}
	feDir, beDir, err := feBEDirs(cfg, pair, repo, task, wt)
	if err != nil {
		return PairPlan{}, err
	}
	for _, dir := range []string{feDir, beDir} {
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			return PairPlan{}, fmt.Errorf("worktree directory unavailable: %s", dir)
		}
	}
	fePort, bePort := TaskPorts(task)
	replace := func(cmd string, port int) string {
		return strings.NewReplacer("{port}", strconv.Itoa(port), "{fe_port}", strconv.Itoa(fePort), "{be_port}", strconv.Itoa(bePort)).Replace(cmd)
	}
	plan := PairPlan{FERepo: pair.FERepo, BERepo: pair.BERepo, FEDir: feDir, BEDir: beDir,
		FECmd: replace(pair.FECmd, fePort), BECmd: replace(pair.BECmd, bePort),
		FEPort: fePort, BEPort: bePort, EnvFile: pair.EnvFile(), Env: map[string]string{}}
	if pair.FEEnvVar != "" {
		plan.Env[pair.FEEnvVar] = fmt.Sprintf("http://localhost:%d%s", bePort, pair.FEEnvPath)
	}
	if key := pair.TaskKey(); key != "" {
		plan.Env[key] = task
	}
	for _, key := range pair.FEURLEnvVars {
		plan.Env[key] = fmt.Sprintf("http://localhost:%d", fePort)
	}
	if pair.FEPatchFile != "" {
		plan.PatchFile, plan.PatchKey = pair.FEPatchFile, pair.FEPatchKey
		path, mode, err := managedFile(feDir, pair.FEPatchFile)
		if err != nil {
			return PairPlan{}, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return PairPlan{}, err
		}
		re := regexp.MustCompile(`(?m)^(\s*` + regexp.QuoteMeta(pair.FEPatchKey) + `\s*[:=]\s*")http://localhost:\d+([^"]*")`)
		if !re.Match(data) {
			return PairPlan{}, fmt.Errorf("no %q localhost URL found in %s", pair.FEPatchKey, path)
		}
		plan.patchData = re.ReplaceAll(data, []byte(`${1}http://localhost:`+strconv.Itoa(bePort)+`${2}`))
		plan.patchMode = mode
	}
	return plan, nil
}

func prepFEBE(cfg config.Config, repo, task, wt string) (feDir, beDir, feCmd, beCmd string, err error) {
	plan, err := PlanPair(cfg, repo, task, wt)
	if err != nil {
		return "", "", "", "", err
	}
	if err := patchEnvFile(plan.FEDir, plan.EnvFile, plan.Env); err != nil {
		return "", "", "", "", err
	}
	if plan.PatchFile != "" {
		if err := atomicWrite(filepath.Join(plan.FEDir, plan.PatchFile), plan.patchData, plan.patchMode); err != nil {
			return "", "", "", "", err
		}
	}
	return plan.FEDir, plan.BEDir, plan.FECmd, plan.BECmd, nil
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
