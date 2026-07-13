package main

import (
	"fmt"
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
		attach(cfg, res.Repo, res.Task, wt)
	}
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
	if err := worktree.EndTask(cfg, worktree.LiveTmuxOps(), repos, repo, task); err != nil {
		fatal(err.Error())
	}
	fmt.Fprintln(os.Stderr, "Worktree '"+task+"' cleaned up")
	if main := worktree.FindRepoRoot(repos, repo); main != "" {
		fmt.Println(main)
	}
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
