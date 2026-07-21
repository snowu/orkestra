package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"orkestra/internal/config"
	"orkestra/internal/tmux"
	"orkestra/internal/ui"
	"orkestra/internal/worktree"
)

func loadConfig() config.Config {
	home, _ := os.UserHomeDir()
	cfg, err := config.Load(filepath.Join(home, ".ork.conf"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "ork: bad ~/.ork.conf: "+err.Error())
	}
	return cfg
}

func requireTools() {
	for _, tool := range []string{"tmux", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			fatal(tool + " not installed — required")
		}
	}
}

func runTUI() {
	requireTools()
	cfg := loadConfig()
	ensureLoginProxy(cfg)

	res, err := ui.Run(cfg)
	if err != nil {
		fatal(err.Error())
	}

	switch res.Action {
	case ui.ActionQuit:
		return
	case ui.ActionCD:
		// The ONLY stdout write in the whole program: the cd target for
		// ork.sh's wrapper.
		fmt.Println(res.WtPath)
	case ui.ActionAttach:
		attach(cfg, res.Repo, res.Task, res.WtPath)
	case ui.ActionNewTask:
		wt, err := worktree.NewTask(cfg, res.RepoRoot, res.Task)
		if err != nil {
			fatal("new-task failed for " + res.Repo + "/" + res.Task + ": " + err.Error())
		}
		// Pair entry: create the sibling's worktree too, then attach to the
		// first side — sessions are task-named, so both share one session.
		if res.Repo2 != "" && res.RepoRoot2 != "" {
			if _, err := worktree.NewTask(cfg, res.RepoRoot2, res.Task); err != nil {
				fatal("new-task failed for " + res.Repo2 + "/" + res.Task + ": " + err.Error())
			}
		}
		attach(cfg, res.Repo, res.Task, wt)
	case ui.ActionOpenAll:
		if err := worktree.EnsureFEBEWindows(cfg, res.Repo, res.Task, res.WtPath); err != nil {
			fatal("ensure fe/be windows failed: " + err.Error())
		}
		attach(cfg, res.Repo, res.Task, res.WtPath)
	}
}

// ensureLoginProxy keeps `ork login-proxy` alive in a detached tmux
// session whenever fe/be pairs are configured — so the auth flow works out
// of the box, no manual step. Skipped when port 3000 is already taken
// (someone running a real dev server there deliberately) or the session
// already exists. Best-effort by design: the TUI must come up regardless.
func ensureLoginProxy(cfg config.Config) {
	if len(cfg.Pairs) == 0 {
		return
	}
	if portListening("127.0.0.1:3000") {
		return // something (our proxy, presumably) already answers — leave it
	}
	// Port's dead. If a stale ork-login-proxy tmux session is still around
	// (the process inside exited or was killed but the window/shell lingers,
	// e.g. after a crash or manual ctrl-c), HasSession alone would wrongly
	// treat that as "already running" and never respawn — this is the case
	// that left the proxy silently down indefinitely. Clear it so the fresh
	// session below can bind the port.
	if tmux.HasSession("ork-login-proxy") {
		tmux.KillSession("ork-login-proxy")
	}
	tmux.NewDetached("ork-login-proxy", "exec ork login-proxy")

	// Session creation -> process exec -> http.ListenAndServe takes real
	// wall-clock time. Block until it's actually accepting connections (or
	// give up after a bounded wait; best-effort by design still holds) so a
	// login attempted right after launch doesn't hit a not-yet-bound :3000.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if portListening("127.0.0.1:3000") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func portListening(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 150*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func attach(cfg config.Config, repo, task, wt string) {
	name := worktree.SessionName(cfg, repo, task)
	if err := tmux.NewOrAttach(name, wt); err != nil {
		fatal("tmux attach failed: " + err.Error())
	}
}

// runNewTask: CLI subcommand used by the worktree-tasks.sh shim — creates
// the worktree for the repo at cwd and prints its path on stdout (the shim
// cd's there).
func runNewTask(task string) {
	requireTools()
	cfg := loadConfig()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		fatal("not inside a git repository")
	}
	repoRoot := trimNL(string(out))
	wt, err := worktree.NewTask(cfg, repoRoot, task)
	if err != nil {
		fatal(err.Error())
	}
	fmt.Fprintln(os.Stderr, "Worktree ready at "+wt)
	fmt.Println(wt)
}

// runEndTask: CLI subcommand — task defaults to the current dir's basename
// when run from inside a worktree.
func runEndTask(task string) {
	requireTools()
	cfg := loadConfig()
	cwd, _ := os.Getwd()
	if task == "" {
		for _, root := range cfg.WorktreeRoots {
			if rel, err := filepath.Rel(root, cwd); err == nil && rel != "." && filepath.IsLocal(rel) {
				task = filepath.Base(cwd)
				break
			}
		}
		if task == "" {
			fatal("usage: ork end-task <task-name> (or run from inside a worktree)")
		}
	}
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		fatal("not inside a git repository")
	}
	repo := filepath.Base(trimNL(string(out)))

	// If we're inside the worktree being removed, land the caller in the
	// main checkout afterwards (printed for the shim to cd).
	repos := worktree.AllRepoDirs(homeDirMust(), cfg.ScanMaxDepth, repoCache(), 60*time.Second)
	summary := worktree.EndTask(cfg, worktree.LiveTmuxOps(), repos, repo, task)
	fmt.Fprintln(os.Stderr, summary)
	if main := worktree.FindRepoRoot(repos, repo); main != "" {
		fmt.Println(main)
	}
}

// runEndTaskDirect: temp-session cleanup — repo/task given explicitly (no
// cwd derivation: the session's cwd must not sit inside the dir being
// deleted). Output goes to the session's tty so the TUI can tail it.
func runEndTaskDirect(repo, task string) {
	requireTools()
	cfg := loadConfig()
	repos := worktree.AllRepoDirs(homeDirMust(), cfg.ScanMaxDepth, repoCache(), 60*time.Second)
	summary := worktree.EndTask(cfg, worktree.LiveTmuxOps(), repos, repo, task)
	fmt.Fprintln(os.Stderr, summary)
}

func trimNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

func homeDirMust() string {
	h, _ := os.UserHomeDir()
	return h
}

func repoCache() string {
	return filepath.Join(homeDirMust(), ".cache/ork/repo-scan")
}
