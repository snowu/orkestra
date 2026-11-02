package worktree

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"orkestra/internal/config"
	"orkestra/internal/hooks"
	"orkestra/internal/mux"
)

// Log receives subprocess output (git, hooks). Defaults to stderr for CLI
// use; the TUI swaps in io.Discard — raw git output on stderr while
// bubbletea is drawing there shears the whole layout.
var Log io.Writer = os.Stderr

// Window creation is indirected so tests can capture spawns without a live
// multiplexer server, the same way TmuxOps does for session killing.
var (
	ensureSession = mux.EnsureSession
	ensureWindow  = mux.EnsureWindow
	windowNames   = mux.SessionWindowNames
)

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

// HasBranch reports whether repoRoot has a local branch by that name.
func HasBranch(repoRoot, branch string) bool {
	// Quiet: a query, not an operation — its output (the tip hash) is noise
	// in the operation log.
	return exec.Command("git", "-C", repoRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil
}

// Conflict describes a branch that is already checked out somewhere.
// Resolving it is destructive, so it is reported to the caller rather
// than silently forced.
type Conflict struct {
	Branch string
	Path   string // the checkout currently holding the branch
	IsMain bool   // Path is the primary checkout, not a linked worktree
}

// wtRecord is one record of `git worktree list --porcelain`.
type wtRecord struct {
	path, branch string // branch is the short name; "" when detached
	isMain       bool   // the FIRST record is always the primary checkout
	prunable     bool   // its directory is gone; `git worktree prune` drops it
}

// listWorktrees parses `git worktree list --porcelain`: records separated
// by blank lines, each starting with "worktree <path>".
func listWorktrees(repoRoot string) []wtRecord {
	var recs []wtRecord
	for _, line := range strings.Split(gitOut(repoRoot, "worktree", "list", "--porcelain"), "\n") {
		line = strings.TrimSpace(line)
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			recs = append(recs, wtRecord{path: p, isMain: len(recs) == 0})
			continue
		}
		if len(recs) == 0 {
			continue
		}
		r := &recs[len(recs)-1]
		if b, ok := strings.CutPrefix(line, "branch refs/heads/"); ok {
			r.branch = b
		} else if line == "prunable" || strings.HasPrefix(line, "prunable ") {
			r.prunable = true
		}
	}
	return recs
}

// BranchCheckout reports where branch is checked out, or nil if it is
// free. Read-only — the caller decides whether to disturb anything. A
// prunable record (directory deleted by hand) does not count: git still
// lists it until pruned, but nothing real holds the branch.
func BranchCheckout(repoRoot, branch string) *Conflict {
	for _, r := range listWorktrees(repoRoot) {
		if r.branch == branch && !r.prunable {
			return &Conflict{Branch: branch, Path: r.path, IsMain: r.isMain}
		}
	}
	return nil
}

// checkedOutBranches returns the local branch short names checked out
// anywhere in repoRoot, split by WHERE: linked holds branches held by a
// linked worktree (offering one as a candidate would be nonsense — it
// already has a worktree); main holds the branch (if any) held by the
// primary checkout (no worktree yet — a legitimate candidate, picking it
// just means the main checkout gets switched to its base branch first).
// Prunable records are skipped, as in BranchCheckout.
func checkedOutBranches(repoRoot string) (linked map[string]bool, main map[string]bool) {
	linked, main = map[string]bool{}, map[string]bool{}
	for _, r := range listWorktrees(repoRoot) {
		switch {
		case r.branch == "" || r.prunable:
		case r.isMain:
			main[r.branch] = true
		default:
			linked[r.branch] = true
		}
	}
	return linked, main
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
// survive, and tmux silently rewrites '.' and ':' in session names (so a
// later lookup by the original name would miss). Row.Branch still shows
// the real branch, keeping the mapping visible in the picker.
func TaskNameFor(branch string) string {
	return strings.NewReplacer("/", "-", ".", "-", ":", "-").Replace(branch)
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

// patchFileData rewrites every `key = "http://localhost:PORT..."` (or
// `key: "..."`) line in data to port, keeping any URL suffix — for backend
// urls that live in a config template (e.g. a Pkl file rendered into a
// generated config.json on every dev-server start, where patching the
// generated file gets immediately clobbered) rather than a dotenv file.
func patchFileData(data []byte, key string, port int) ([]byte, error) {
	re := regexp.MustCompile(`(?m)^(\s*` + regexp.QuoteMeta(key) + `\s*[:=]\s*")http://localhost:\d+([^"]*")`)
	if !re.Match(data) {
		return nil, fmt.Errorf("no %q localhost URL found", key)
	}
	return re.ReplaceAll(data, []byte(`${1}http://localhost:`+strconv.Itoa(port)+`${2}`)), nil
}

// patchFilePort applies patchFileData to dir/relPath in place.
func patchFilePort(dir, relPath, key string, port int) error {
	path, mode, err := managedFile(dir, relPath)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out, err := patchFileData(data, key, port)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return atomicWrite(path, out, mode)
}

// subPorts substitutes {port} (the process's own port) plus {fe_port} and
// {be_port}, so a command can reference the other side's port too.
func subPorts(cmd string, port, fePort, bePort int) string {
	return strings.NewReplacer(
		"{port}", strconv.Itoa(port),
		"{fe_port}", strconv.Itoa(fePort),
		"{be_port}", strconv.Itoa(bePort),
	).Replace(cmd)
}

// ProcPlan is one process of a GroupPlan, fully resolved. Skip, when set,
// says why the process will not be started (no worktree for the task, a
// fixed port already taken) — a note, not an error.
type ProcPlan struct {
	Label, Repo, Dir, Cmd string
	Port                  int
	EnvFile               string
	Env                   map[string]string
	PatchFile, PatchKey   string
	Skip                  string
	patchData             []byte
	patchMode             os.FileMode
}

// GroupPlan is everything spawning a group for a task would do. Building
// one writes nothing and starts nothing, so the CLI's --dry-run and the
// real launch share it: what is previewed is exactly what runs.
type GroupPlan struct {
	Group, Session string
	FEPort, BEPort int
	Procs          []ProcPlan
}

// PlanGroup resolves g for repo/task (wt is repo's own worktree). It fails
// only on configuration that cannot work (a patch key that isn't in its
// file, an env file outside the worktree); per-process gaps become Skip.
func PlanGroup(cfg config.Config, g config.Group, repo, task, wt string) (GroupPlan, error) {
	if err := g.Validate(); err != nil {
		return GroupPlan{}, err
	}
	fePort, bePort := TaskPorts(task)
	plan := GroupPlan{Group: g.Name, Session: SessionName(cfg, repo, task), FEPort: fePort, BEPort: bePort}
	existing := map[string]bool{}
	for _, w := range windowNames(plan.Session) {
		existing[w] = true
	}
	for _, p := range g.Processes {
		pp := ProcPlan{Label: p.Label, Repo: p.Repo, EnvFile: p.EnvFileOrDefault(), Env: map[string]string{}}
		pp.Dir = wt
		if p.Repo != repo {
			pp.Dir = FindWorktree(cfg.WorktreeRoots, p.Repo, task)
		}
		switch {
		case pp.Dir == "":
			pp.Skip = "no " + p.Repo + "/" + task + " worktree"
		// A fixed-port process cannot be isolated per task, so a port
		// already in use means another task's copy is running. Report it
		// instead of letting the bind fail confusingly (or worse, letting
		// a federation host load the wrong task's remote). This task's own
		// window already running is not a conflict — it's a re-run.
		case p.FixedPort != 0 && !existing[p.Label] && portInUse(p.FixedPort):
			pp.Skip = fmt.Sprintf("port %d already in use", p.FixedPort)
		}
		if pp.Skip != "" {
			plan.Procs = append(plan.Procs, pp)
			continue
		}
		switch p.PortRange {
		case "fe":
			pp.Port = fePort
		case "be":
			pp.Port = bePort
		}
		pp.Cmd = subPorts(p.Cmd, pp.Port, fePort, bePort)
		if p.EnvVar != "" {
			pp.Env[p.EnvVar] = fmt.Sprintf("http://localhost:%d%s", bePort, p.EnvPath)
		}
		if key := p.TaskKey(); key != "" {
			pp.Env[key] = task
		}
		// Apps that hardcode their own origin (NEXTAUTH_URL etc.) must
		// learn the task's real fe port, or auth redirects land on :3000.
		for _, key := range p.URLEnvVars {
			pp.Env[key] = fmt.Sprintf("http://localhost:%d", fePort)
		}
		if len(pp.Env) > 0 {
			if _, _, err := managedFile(pp.Dir, pp.EnvFile); err != nil {
				return GroupPlan{}, fmt.Errorf("%s: %w", p.Label, err)
			}
		}
		if p.PatchFile != "" {
			pp.PatchFile, pp.PatchKey = p.PatchFile, p.PatchKey
			path, mode, err := managedFile(pp.Dir, p.PatchFile)
			if err != nil {
				return GroupPlan{}, fmt.Errorf("%s: %w", p.Label, err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return GroupPlan{}, fmt.Errorf("%s: %w", p.Label, err)
			}
			if pp.patchData, err = patchFileData(data, p.PatchKey, bePort); err != nil {
				return GroupPlan{}, fmt.Errorf("%s: %s: %w", p.Label, path, err)
			}
			pp.patchMode = mode
		}
		plan.Procs = append(plan.Procs, pp)
	}
	return plan, nil
}

// ResolveGroupFor picks the group to spawn for repo/task: the named one if
// name is set, otherwise ResolveGroup's choice (an *AmbiguousGroupError
// when that is a tie).
func ResolveGroupFor(cfg config.Config, repo, task, name string) (config.Group, error) {
	if name != "" {
		g, ok := cfg.GroupByName(name)
		if !ok {
			return config.Group{}, fmt.Errorf("no group named %q", name)
		}
		return g, nil
	}
	g, ambiguous, ok := ResolveGroup(cfg, cfg.WorktreeRoots, repo, task)
	if !ok {
		if len(cfg.Groups) == 0 {
			return config.Group{}, fmt.Errorf("no process groups configured (%s)", cfg.GroupsConfig)
		}
		return config.Group{}, fmt.Errorf("%s is not part of any group", repo)
	}
	if ambiguous != nil {
		return config.Group{}, &AmbiguousGroupError{Repo: repo, Task: task, Candidates: ambiguous}
	}
	return g, nil
}

// AmbiguousGroupError is returned by EnsureGroupWindows when repo/task
// resolves to two or more groups with an equal claim (same count of
// present member worktrees) — this is the highest-stakes call site for
// group resolution (it launches processes), so it must never guess.
// Candidates carries every tied group so the caller can name them in a
// message, or eventually offer a picker.
type AmbiguousGroupError struct {
	Repo, Task string
	Candidates []config.Group
}

func (e *AmbiguousGroupError) Error() string {
	names := make([]string, len(e.Candidates))
	for i, g := range e.Candidates {
		names[i] = g.Name
	}
	return fmt.Sprintf("%s/%s matches groups %s — ambiguous", e.Repo, e.Task, strings.Join(names, ", "))
}

// EnsureGroupWindows makes sure the base session for repo/task exists and
// has one window per process in the resolved group, each running its
// command in its OWN repo's worktree for that task.
//
// Returns the labels actually spawned and human-readable notes about what
// was skipped and why. Skips are deliberately NOT errors: a group may span
// repos that a given task does not touch, and one absent worktree must not
// stop the rest of the group from starting. When repo/task resolves
// ambiguously (see ResolveGroup), err is an *AmbiguousGroupError instead of
// guessing which group to launch.
func EnsureGroupWindows(cfg config.Config, repo, task, wt string) (spawned []string, notes []string, err error) {
	g, err := ResolveGroupFor(cfg, repo, task, "")
	if err != nil {
		return nil, nil, err
	}
	return EnsureGroupWindowsFor(cfg, g, repo, task, wt)
}

// EnsureGroupWindowsFor is EnsureGroupWindows without the resolution step —
// the caller (e.g. the ambiguity picker, once the user has chosen among
// AmbiguousGroupError's Candidates) already knows exactly which group to
// spawn.
func EnsureGroupWindowsFor(cfg config.Config, g config.Group, repo, task, wt string) (spawned []string, notes []string, err error) {
	plan, err := PlanGroup(cfg, g, repo, task, wt)
	if err != nil {
		return nil, nil, err
	}
	return ApplyGroupPlan(plan, wt)
}

// ApplyGroupPlan writes the plan's env/template updates and starts each
// non-skipped process's window in plan.Session (created at sessionDir if
// missing).
func ApplyGroupPlan(plan GroupPlan, sessionDir string) (spawned []string, notes []string, err error) {
	if err := ensureSession(plan.Session, sessionDir); err != nil {
		return nil, nil, err
	}
	for _, p := range plan.Procs {
		if p.Skip != "" {
			notes = append(notes, p.Label+": "+p.Skip+" — skipped")
			continue
		}
		if err := patchEnvFile(p.Dir, p.EnvFile, p.Env); err != nil {
			return spawned, notes, fmt.Errorf("%s: %w", p.Label, err)
		}
		if p.PatchFile != "" {
			path, _, err := managedFile(p.Dir, p.PatchFile)
			if err == nil {
				err = atomicWrite(path, p.patchData, p.patchMode)
			}
			if err != nil {
				return spawned, notes, fmt.Errorf("%s: %w", p.Label, err)
			}
		}
		if err := ensureWindow(plan.Session, p.Label, p.Dir, p.Cmd); err != nil {
			return spawned, notes, err
		}
		spawned = append(spawned, p.Label)
	}
	return spawned, notes, nil
}

// portInUse reports whether something is already listening locally.
func portInUse(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 150*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// NewTask creates <firstRoot>/<repo>/<task> as a worktree on a new branch
// off the repo's default branch, copies .env.local, runs the repo hook,
// writes .claude-profile, and touches the access marker (a fresh task
// counts as used — otherwise it'd sort to the bottom next run).
func NewTask(cfg config.Config, repoRoot, task string) (string, error) {
	return NewTaskOnBranch(cfg, repoRoot, task, task)
}

// NewTaskOnBranch is NewTask with a branch name that differs from the task
// (directory/session) name — used when a group sibling has to get a fresh
// branch matching another member's existing one, e.g. feat/login while the
// task dir is feat-login, so both repos' branches line up.
func NewTaskOnBranch(cfg config.Config, repoRoot, task, branch string) (string, error) {
	if err := ValidateTaskName(task); err != nil {
		return "", err
	}
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
	if err := git(repoRoot, "worktree", "add", wt, "-b", branch, base); err != nil {
		return "", fmt.Errorf("git worktree add failed for %s: %w", wt, err)
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
	// does, so the non-forced path never mutates anything. It is required,
	// not cosmetic: a worktree whose directory was deleted by hand still
	// holds its branch in git's bookkeeping (BranchCheckout ignores such
	// prunable records), and `git worktree add` refuses the branch until
	// it is pruned.
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
//
// The worktree already exists by now, so a failing copy or hook is reported
// (with the path, so it can be repaired by hand) but never aborts: the
// remaining steps still run and the caller still gets a usable worktree —
// failing here would leave a half-set-up worktree whose branch blocks a
// retry.
func finishWorktree(cfg config.Config, repoRoot, repo, task, wt string) {
	if data, err := os.ReadFile(filepath.Join(repoRoot, ".env.local")); err == nil {
		if err := os.WriteFile(filepath.Join(wt, ".env.local"), data, 0o600); err != nil {
			fmt.Fprintf(Log, "ork: %s: copying .env.local failed: %v\n", wt, err)
		} else {
			fmt.Fprintln(Log, "Copied .env.local")
		}
	}
	if err := hooks.RunRepoHook(cfg.HooksConfig, repo, wt); err != nil {
		fmt.Fprintf(Log, "ork: %s: setup hook failed: %v — fix it up by hand\n", wt, err)
	}
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

// SessionsFor lists the sessions that belong to a worktree: any session
// with a pane anywhere inside it (cwd in a subdirectory counts), the
// repo-scoped name, and the plain task-named session — but that last one
// only when no OTHER repo's worktree under the same task name still exists,
// since task-named sessions are shared across repos by design and killing
// it would yank it from a still-active sibling. KillSessionFor kills
// exactly these, and the TUI checks its own session against the same list
// before a kill, so the two can never disagree.
func SessionsFor(cfg config.Config, t TmuxOps, repo, task string) []string {
	wt := WorktreeOrDefault(cfg.WorktreeRoots, repo, task)
	hasSibling := false
	for _, root := range cfg.WorktreeRoots {
		repos, _ := os.ReadDir(root)
		for _, r := range repos {
			dir := filepath.Join(root, r.Name(), task)
			if dir == wt {
				continue
			}
			if st, err := os.Stat(dir); err == nil && st.IsDir() {
				hasSibling = true
			}
		}
	}
	var out []string
	seen := map[string]bool{}
	add := func(name string) {
		if name == "" || seen[name] || (name == task && hasSibling) {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, pane := range t.Panes() {
		if PathWithin(wt, pane.CWD) {
			add(pane.Session)
		}
	}
	if name := repo + "__" + task; t.HasSession(name) {
		add(name)
	}
	if t.HasSession(task) {
		add(task)
	}
	return out
}

// KillSessionFor kills SessionsFor(repo, task) without touching the
// worktree or branch.
func KillSessionFor(cfg config.Config, t TmuxOps, repo, task string) {
	for _, name := range SessionsFor(cfg, t, repo, task) {
		t.KillSession(name)
	}
}

// EndTask removes the worktree, deletes the branch locally and on origin,
// clears the access marker, and kills the session LAST: when ork itself
// runs inside the session being ended, the kill also kills this process —
// with the kill first, nothing after it ever ran (branch+folder left
// behind). Killing last means cleanup is already done if we die here.
//
// If git refuses to remove a worktree it still tracks (locked, say),
// cleanup stops there and preserves the folder, branch and session. A
// folder git does NOT track (registration pruned, main checkout re-cloned)
// can't be removed by git at all, so it is deleted directly instead —
// otherwise it could never be cleaned up from ork. The remaining steps are
// best-effort (a branch that was never pushed makes `push origin --delete`
// fail — that must not abort the rest), so instead of an error EndTask
// returns a summary of what each step actually did, for the TUI's status
// line / CLI output.
func EndTask(cfg config.Config, t TmuxOps, repos []string, repo, task string) string {
	if len(cfg.WorktreeRoots) == 0 || repo == "" || repo == "." || repo == ".." || filepath.Base(repo) != repo {
		return "cleanup refused: invalid repo or worktree roots"
	}
	if task == "" || task == "." || task == ".." || filepath.Base(task) != task {
		return "cleanup refused: task must be a single folder name"
	}
	wt := WorktreeOrDefault(cfg.WorktreeRoots, repo, task)
	var steps []string
	step := func(label string, err error) {
		if err != nil {
			steps = append(steps, label+" FAILED")
		} else {
			steps = append(steps, label)
		}
	}

	// The branch can differ from the task: a reused feat/login lives in
	// task dir feat-login. Read it before the worktree is gone.
	branch := GitBranch(wt)
	if branch == "" {
		branch = task
	}

	repoRoot := FindRepoRoot(repos, repo)
	if repoRoot == "" {
		return repo + "/" + task + ": repo root not found — cleanup skipped, nothing removed"
	}
	switch _, err := os.Stat(wt); {
	case os.IsNotExist(err):
		steps = append(steps, "folder already gone")
	case !isRegisteredWorktree(repoRoot, wt):
		step("untracked folder removed", os.RemoveAll(wt))
	default:
		if err := git(repoRoot, "worktree", "remove", wt, "--force"); err != nil {
			return repo + "/" + task + ": worktree removal FAILED; folder, branch and session preserved: " + err.Error()
		}
		steps = append(steps, "worktree removed")
	}
	git(repoRoot, "worktree", "prune")
	step("branch "+branch+" deleted", git(repoRoot, "branch", "-D", branch))
	step("origin branch deleted", git(repoRoot, "push", "origin", "--delete", branch))
	os.Remove(AccessFile(repo, task))

	KillSessionFor(cfg, t, repo, task)
	steps = append(steps, "session cleanup complete")
	return repo + "/" + task + ": " + strings.Join(steps, " · ")
}

// isRegisteredWorktree reports whether git tracks wt as a worktree of
// repoRoot (paths compared after resolving symlinks).
func isRegisteredWorktree(repoRoot, wt string) bool {
	want := evalPath(wt)
	for _, r := range listWorktrees(repoRoot) {
		if evalPath(r.path) == want {
			return true
		}
	}
	return false
}

func evalPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// ValidateTaskName: a task is a git branch name that also fits the
// discovery layout's single folder level (<root>/<repo>/<task>).
func ValidateTaskName(task string) error {
	if task == "" || task == "." || task == ".." || strings.ContainsAny(task, `/\`) || strings.Contains(task, "@{") {
		return fmt.Errorf("task must be a single folder name")
	}
	if err := exec.Command("git", "check-ref-format", "--branch", task).Run(); err != nil {
		return fmt.Errorf("invalid task name %q (must be a valid git branch name)", task)
	}
	return nil
}
