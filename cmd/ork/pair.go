package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"orkestra/internal/worktree"
)

const usage = `usage: ork [new-task <name> | end-task [name] | pair [task] [--dry-run] | config check | login-proxy [port] | --version]
ORK_CONFIG selects a config file (default ~/.ork.conf).
pair starts both configured repos' dev windows; --dry-run previews without changes.`

func runConfigCheck() {
	cfg := loadConfig()
	fmt.Fprintf(os.Stderr, "Configuration OK: %s, %d worktree root(s), %d pair(s)\n", cfg.Multiplexer, len(cfg.WorktreeRoots), len(cfg.Pairs))
	for _, p := range cfg.Pairs {
		fmt.Fprintf(os.Stderr, "  %s + %s: env %s, task key %q\n", p.FERepo, p.BERepo, p.EnvFile(), p.TaskKey())
	}
}

func runPair(args []string) {
	task, dry := "", false
	for _, arg := range args {
		if arg == "--dry-run" {
			dry = true
			continue
		}
		if task != "" || strings.HasPrefix(arg, "-") {
			fatal("usage: ork pair [task] [--dry-run]")
		}
		task = arg
	}
	cfg := loadConfig()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		fatal("pair: run from a repo checkout or task worktree")
	}
	checkout := trimNL(string(out))
	repo, wt := filepath.Base(checkout), ""
	// Worktree layout is <root>/<repo>/<task>; use its repo and task when
	// invoked from any subdirectory, rather than treating task as a repo.
	for _, root := range cfg.WorktreeRoots {
		rel, err := filepath.Rel(root, checkout)
		if err != nil || !filepath.IsLocal(rel) {
			continue
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) == 2 {
			repo = parts[0]
			if task == "" {
				task = parts[1]
			}
			if task == parts[1] {
				wt = checkout
			}
			break
		}
	}
	if task == "" {
		fatal("pair: supply a task name, or run inside its worktree")
	}
	if task == "." || task == ".." || strings.ContainsAny(task, `/\`) {
		fatal("pair: task must be a single folder name")
	}
	if wt == "" {
		wt = worktree.FindWorktree(cfg.WorktreeRoots, repo, task)
	}
	plan, err := worktree.PlanPair(cfg, repo, task, wt)
	if err != nil {
		fatal(err.Error())
	}
	fmt.Fprintf(os.Stderr, "%s (%d): %s\n  %s\n%s (%d): %s\n  %s\n", plan.FERepo, plan.FEPort, plan.FEDir, plan.FECmd, plan.BERepo, plan.BEPort, plan.BEDir, plan.BECmd)
	var keys []string
	for key := range plan.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(os.Stderr, "  %s: %s=%q\n", plan.EnvFile, key, plan.Env[key])
	}
	if dry {
		return
	}
	requireTools(cfg)
	ensureLoginProxy(cfg)
	if err := worktree.EnsureFEBEWindows(cfg, repo, task, wt); err != nil {
		fatal(err.Error())
	}
	fmt.Fprintln(os.Stderr, "Pair ready in session "+worktree.SessionName(cfg, repo, task))
}
