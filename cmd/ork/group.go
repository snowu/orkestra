package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"orkestra/internal/config"
	"orkestra/internal/worktree"
)

const usage = `usage: ork [new-task <name> | end-task [name] | scan | group [task] [--group NAME] [--dry-run] | config check | login-proxy [port] | --version]
ORK_CONFIG selects a config file (default ~/.ork.conf).
group starts the task's process-group windows (same as ctrl-g); --dry-run
previews worktrees, ports, commands and file updates without changing anything.`

// runConfigCheck validates the configuration and lists the effective
// groups. Unlike normal startup, which reports problems and carries on,
// any problem makes this exit non-zero.
func runConfigCheck() {
	cfg := loadConfigQuiet()
	fmt.Fprintf(os.Stderr, "%s, %d worktree root(s), %d group(s) from %s\n",
		cfg.Multiplexer, len(cfg.WorktreeRoots), len(cfg.Groups), cfg.GroupsConfig)
	for _, g := range cfg.Groups {
		var procs []string
		for _, p := range g.Processes {
			procs = append(procs, p.Label+"="+p.Repo)
		}
		fmt.Fprintf(os.Stderr, "  %s: %s\n", g.Name, strings.Join(procs, ", "))
	}
	if len(cfg.Problems) > 0 {
		for _, p := range cfg.Problems {
			fmt.Fprintln(os.Stderr, "problem: "+p)
		}
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "Configuration OK")
}

func runGroup(args []string) {
	task, group, dry := "", "", false
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--dry-run":
			dry = true
		case arg == "--group" && i+1 < len(args):
			i++
			group = args[i]
		case task == "" && !strings.HasPrefix(arg, "-"):
			task = arg
		default:
			fatal("usage: ork group [task] [--group NAME] [--dry-run]")
		}
	}
	cfg := loadConfig()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		fatal("group: run from a repo checkout or task worktree")
	}
	repo, task, wt := taskContext(cfg, trimNL(string(out)), task)
	if task == "" {
		fatal("group: supply a task name, or run inside its worktree")
	}
	if err := worktree.ValidateTaskName(task); err != nil {
		fatal("group: " + err.Error())
	}
	if wt == "" {
		wt = worktree.FindWorktree(cfg.WorktreeRoots, repo, task)
	}
	if wt == "" {
		fatal("group: no " + repo + "/" + task + " worktree")
	}
	g, err := worktree.ResolveGroupFor(cfg, repo, task, group)
	var amb *worktree.AmbiguousGroupError
	if errors.As(err, &amb) {
		fatal(err.Error() + " — pick one with --group NAME")
	}
	if err != nil {
		fatal(err.Error())
	}
	plan, err := worktree.PlanGroup(cfg, g, repo, task, wt)
	if err != nil {
		fatal(err.Error())
	}
	printPlan(plan)
	if dry {
		return
	}
	requireTools(cfg)
	ensureLoginProxy(cfg)
	_, notes, err := worktree.ApplyGroupPlan(plan, wt)
	for _, n := range notes {
		fmt.Fprintln(os.Stderr, "ork: "+n)
	}
	if err != nil {
		fatal(err.Error())
	}
	fmt.Fprintln(os.Stderr, "Group "+plan.Group+" ready in session "+plan.Session)
}

func printPlan(plan worktree.GroupPlan) {
	fmt.Fprintf(os.Stderr, "group %s → session %s (fe port %d, be port %d)\n", plan.Group, plan.Session, plan.FEPort, plan.BEPort)
	for _, p := range plan.Procs {
		if p.Skip != "" {
			fmt.Fprintf(os.Stderr, "%s (%s): skipped — %s\n", p.Label, p.Repo, p.Skip)
			continue
		}
		fmt.Fprintf(os.Stderr, "%s (%s): %s\n  $ %s\n", p.Label, p.Repo, p.Dir, p.Cmd)
		keys := make([]string, 0, len(p.Env))
		for k := range p.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(os.Stderr, "  %s: %s=%q\n", p.EnvFile, k, p.Env[k])
		}
		if p.PatchFile != "" {
			fmt.Fprintf(os.Stderr, "  %s: %s → localhost:%d\n", p.PatchFile, p.PatchKey, plan.BEPort)
		}
	}
}

// taskContext maps a checkout path to repo/task. Inside <root>/<repo>/<task>
// (at any depth below it) both come from the layout; elsewhere repo is the
// checkout's folder name and task stays as given. Roots and checkout are
// compared with symlinks resolved — git reports the resolved toplevel, so
// a symlinked root would otherwise never match.
func taskContext(cfg config.Config, checkout, task string) (repo, resolvedTask, wt string) {
	repo = filepath.Base(checkout)
	real := resolve(checkout)
	for _, root := range cfg.WorktreeRoots {
		rel, err := filepath.Rel(resolve(root), real)
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
				wt = filepath.Join(root, parts[0], parts[1])
			}
			break
		}
	}
	return repo, task, wt
}

func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
