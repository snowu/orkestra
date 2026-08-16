package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"orkestra/internal/config"
	"orkestra/internal/mux"
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

// requireTools verifies git and selects the configured multiplexer
// backend (ORK_MULTIPLEXER) — a missing binary or unknown value is fatal,
// never a fallback to the other backend.
func requireTools(cfg config.Config) {
	if _, err := exec.LookPath("git"); err != nil {
		fatal("git not installed — required")
	}
	if err := mux.Select(cfg.Multiplexer); err != nil {
		fatal(err.Error())
	}
}

func runTUI() {
	cfg := loadConfig()
	requireTools(cfg)
	ensureLoginProxy(cfg)

	res, err := ui.Run(cfg)
	if err != nil {
		fatal(err.Error())
	}
	dispatch(cfg, res)
}

// runScan: `ork scan` — the TUI opened straight on the scan screen. The
// result is dispatched through the same switch as the TUI's own, so a
// branch picked here behaves exactly as one picked with ctrl+f.
func runScan() {
	cfg := loadConfig()
	requireTools(cfg)
	res, err := ui.RunScan(cfg)
	if err != nil {
		fatal(err.Error())
	}
	dispatch(cfg, res)
}

func dispatch(cfg config.Config, res ui.Result) {
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
	case ui.ActionUseBranch:
		wt, conflict, err := worktree.AddExisting(cfg, res.RepoRoot, res.Branch, res.Force)
		if err != nil {
			fatal("worktree for " + res.Branch + " failed: " + err.Error())
		}
		if conflict != nil {
			// The TUI already resolved conflicts before returning; reaching
			// here means the checkout changed in between.
			fatal(res.Branch + " is now checked out in " + conflict.Path + " — try again")
		}
		// The sibling repo may not have the same branch; fall back to a new
		// one, matching how ActionNewTask treats asymmetric pairs.
		if res.RepoRoot2 != "" {
			if worktree.HasBranch(res.RepoRoot2, res.Branch) {
				// res.Force is the user's answer to a prompt that named the
				// PRIMARY repo's checkout — it never showed them the
				// sibling's state, so it carries no consent to disturb the
				// sibling. Always pass false here; a sibling whose branch is
				// checked out elsewhere is reported below, not forced.
				if _, conflict2, err := worktree.AddExisting(cfg, res.RepoRoot2, res.Branch, false); err != nil {
					fmt.Fprintln(os.Stderr, "ork: sibling worktree failed: "+err.Error())
				} else if conflict2 != nil {
					fmt.Fprintln(os.Stderr, "ork: sibling worktree skipped: "+res.Branch+" is checked out in "+conflict2.Path)
				}
			} else if _, err := worktree.NewTask(cfg, res.RepoRoot2, res.Task); err != nil {
				fmt.Fprintln(os.Stderr, "ork: sibling worktree failed: "+err.Error())
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
	if mux.HasSession("ork-login-proxy") {
		mux.KillSession("ork-login-proxy")
	}
	// A failed spawn will never bind the port, so waiting on it is pure
	// startup latency — the full 3s below on every single launch (the
	// multiplexer server being down hits this every time).
	if err := mux.NewDetached("ork-login-proxy", "exec ork login-proxy"); err != nil {
		fmt.Fprintln(os.Stderr, "ork: login proxy not started: "+err.Error())
		return
	}

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
	if err := mux.NewOrAttach(name, wt); err != nil {
		fatal(mux.Binary() + " attach failed: " + err.Error())
	}
}

// runNewTask: CLI subcommand used by the worktree-tasks.sh shim — creates
// the worktree for the repo at cwd and prints its path on stdout (the shim
// cd's there).
func runNewTask(task string) {
	cfg := loadConfig()
	requireTools(cfg)
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
	cfg := loadConfig()
	requireTools(cfg)
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
	cfg := loadConfig()
	requireTools(cfg)
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
