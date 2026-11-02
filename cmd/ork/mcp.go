package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"orkestra/internal/agentstate"
	"orkestra/internal/config"
	"orkestra/internal/hooks"
	"orkestra/internal/mux"
	"orkestra/internal/worktree"
)

// runMCP serves ork's operations over MCP on stdio, so coding-agent CLIs
// can create worktrees, manage groups and start them. stdout is the
// JSON-RPC channel here, so os.Stdout is pointed at stderr for the whole
// run: a stray print anywhere would otherwise corrupt the protocol stream.
func runMCP() {
	out := os.Stdout
	os.Stdout = os.Stderr
	srv := newMCPServer()
	if err := srv.Run(context.Background(), &mcp.IOTransport{Reader: os.Stdin, Writer: out}); err != nil {
		fatal("mcp: " + err.Error())
	}
}

const mcpInstructions = `ork manages per-task git worktrees laid out as <root>/<repo>/<task>, each with a terminal-multiplexer (tmux/herdr) session named after the task.
A "process group" (groups.json) is a set of sibling repos that share task names, e.g. a frontend + backend; starting a group opens one window per process in the task's session, each running its command in its own repo's worktree with stable per-task ports.
Typical flow: list_repos → create_worktree (repo, or group to create every member's worktree at once) → start_group. Use plan_group to preview a start, get_config to see groups/hooks, set_group/delete_group/set_repo_hook to edit configuration.
Repos are referred to by folder name (basename), not path.`

// mcpMu serialises tool calls: worktree.Log and hooks.Out are package
// globals swapped per call to capture git/hook output.
var mcpMu sync.Mutex

// callEnv is what every tool handler gets: freshly loaded config (the user
// may edit it between calls) and the per-call log of git/hook output.
type callEnv struct {
	cfg config.Config
}

func newMCPServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "ork", Version: version}, &mcp.ServerOptions{Instructions: mcpInstructions})

	addTool(s, &mcp.Tool{Name: "get_config",
		Description: "Show ork's effective configuration: multiplexer, worktree roots, favorite repos, process groups, per-repo setup hooks, and any configuration problems.",
		Annotations: readOnly("Show ork configuration")}, mcpGetConfig)
	addTool(s, &mcp.Tool{Name: "list_repos",
		Description: "List the git repos ork knows about (found by scanning $HOME), favorites first, with the groups each belongs to.",
		Annotations: readOnly("List repos")}, mcpListRepos)
	addTool(s, &mcp.Tool{Name: "list_worktrees",
		Description: "List task worktrees: repo, task, branch, path, whether a session is live, the agent state in it (running/waiting/input) and the resolved process group.",
		Annotations: readOnly("List worktrees")}, mcpListWorktrees)
	addTool(s, &mcp.Tool{Name: "list_branches",
		Description: "List a repo's local branches that have no worktree yet, newest first — candidates for create_worktree's branch argument.",
		Annotations: readOnly("List branches")}, mcpListBranches)
	addTool(s, &mcp.Tool{Name: "create_worktree",
		Description: "Create a task worktree in a repo — or, given a group, in every member repo of the group — on a new branch off the default branch, or on an existing branch. Copies .env.local and runs the repo's setup hook. Does not start anything; use start_group for that.",
		Annotations: &mcp.ToolAnnotations{Title: "Create worktree", DestructiveHint: ptr(false), OpenWorldHint: ptr(false)}}, mcpCreateWorktree)
	addTool(s, &mcp.Tool{Name: "plan_group",
		Description: "Dry run of start_group: shows the resolved group, session, ports, commands per process, and the env/template file updates — writes and starts nothing.",
		Annotations: readOnly("Preview group start")}, mcpPlanGroup)
	addTool(s, &mcp.Tool{Name: "start_group",
		Description: "Start a task's process group: writes each process's env/template updates and opens one window per process in the task's session (existing windows are left alone). Also starts the login proxy on :3000 if it isn't running.",
		Annotations: &mcp.ToolAnnotations{Title: "Start group", DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(false)}}, mcpStartGroup)
	addTool(s, &mcp.Tool{Name: "kill_session",
		Description: "Kill the multiplexer session(s) of a task worktree, stopping everything running in them. Leaves the worktree and branch alone. A task-named session shared with another repo's worktree of the same task is not killed.",
		Annotations: &mcp.ToolAnnotations{Title: "Kill session", DestructiveHint: ptr(true), IdempotentHint: true, OpenWorldHint: ptr(false)}}, mcpKillSession)
	addTool(s, &mcp.Tool{Name: "end_task",
		Description: "End a task in one repo: removes the worktree (uncommitted changes are lost), deletes its branch locally AND on origin, and kills its session. Irreversible — confirm with the user first. If you are running inside that session, it kills you too.",
		Annotations: &mcp.ToolAnnotations{Title: "End task", DestructiveHint: ptr(true), OpenWorldHint: ptr(true)}}, mcpEndTask)
	addTool(s, &mcp.Tool{Name: "set_group",
		Description: "Add a process group to groups.json, or replace the group with the same name. Validated before writing; the previous file is kept as groups.json.bak.",
		Annotations: &mcp.ToolAnnotations{Title: "Add or replace group", DestructiveHint: ptr(true), IdempotentHint: true, OpenWorldHint: ptr(false)}}, mcpSetGroup)
	addTool(s, &mcp.Tool{Name: "delete_group",
		Description: "Remove a process group from groups.json (the previous file is kept as groups.json.bak). Worktrees and sessions are not touched.",
		Annotations: &mcp.ToolAnnotations{Title: "Delete group", DestructiveHint: ptr(true), IdempotentHint: true, OpenWorldHint: ptr(false)}}, mcpDeleteGroup)
	addTool(s, &mcp.Tool{Name: "set_repo_hook",
		Description: "Set the shell command run (cwd = the new worktree) after a worktree is created for a repo, e.g. copying env files and installing dependencies. An empty command removes the hook. Stored in hooks.json; the previous file is kept as hooks.json.bak.",
		Annotations: &mcp.ToolAnnotations{Title: "Set repo setup hook", DestructiveHint: ptr(true), IdempotentHint: true, OpenWorldHint: ptr(false)}}, mcpSetRepoHook)
	return s
}

func ptr[T any](v T) *T { return &v }

func readOnly(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, OpenWorldHint: ptr(false)}
}

// addTool registers fn with the call plumbing every tool shares: one call at
// a time, config reloaded, git/hook output captured and returned alongside
// the result (or the error) as JSON text.
func addTool[In any](s *mcp.Server, t *mcp.Tool, fn func(*callEnv, In) (map[string]any, error)) {
	mcp.AddTool(s, t, func(_ context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		mcpMu.Lock()
		defer mcpMu.Unlock()

		// A temp file, not a buffer: a setup hook that leaves a background
		// process holding a pipe would block the call until it exits.
		logf, err := os.CreateTemp("", "ork-mcp-*.log")
		if err != nil {
			return nil, nil, err
		}
		defer os.Remove(logf.Name())
		defer logf.Close()
		prevLog, prevHooks := worktree.Log, hooks.Out
		worktree.Log, hooks.Out = logf, logf
		defer func() { worktree.Log, hooks.Out = prevLog, prevHooks }()

		var res map[string]any
		cfg, err := config.Load(configPath())
		if err == nil {
			res, err = fn(&callEnv{cfg: cfg}, in)
		}
		logf.Seek(0, io.SeekStart)
		logData, _ := io.ReadAll(logf)
		log := strings.TrimSpace(string(logData))

		if err != nil {
			text := err.Error()
			if log != "" {
				text += "\n\noutput:\n" + log
			}
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
		}
		if log != "" {
			res["output"] = log
		}
		data, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil, nil
	})
}

// selectMux activates the configured multiplexer backend, as an error
// instead of requireTools' exit.
func (e *callEnv) selectMux() error {
	return mux.Select(e.cfg.Multiplexer)
}

// repoDirs is the $HOME repo scan (cached for 60s on disk, like the TUI's);
// fresh forces a rescan.
func (e *callEnv) repoDirs(fresh bool) []string {
	ttl := 60 * time.Second
	if fresh {
		ttl = 0
	}
	return worktree.AllRepoDirs(homeDirMust(), e.cfg.ScanMaxDepth, repoCache(), ttl)
}

// repoRoot resolves a repo folder name to its checkout, rescanning once on
// a miss so a freshly cloned repo is found without waiting out the cache.
func (e *callEnv) repoRoot(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("repo must be a repo folder name, not a path (see list_repos)")
	}
	if r := worktree.FindRepoRoot(e.repoDirs(false), name); r != "" {
		return r, nil
	}
	if r := worktree.FindRepoRoot(e.repoDirs(true), name); r != "" {
		return r, nil
	}
	return "", fmt.Errorf("repo %q not found under $HOME (scan depth %d; see list_repos)", name, e.cfg.ScanMaxDepth)
}

// --- read-only tools ---

type noArgs struct{}

func mcpGetConfig(e *callEnv, _ noArgs) (map[string]any, error) {
	h, err := config.ReadHooks(e.cfg.HooksConfig)
	problems := e.cfg.Problems
	if err != nil {
		problems = append(problems, err.Error())
	}
	naming := "task (shared across repos)"
	if e.cfg.ScopeSessionsToRepo {
		naming = "repo__task"
	}
	return map[string]any{
		"config_file":    configPath(),
		"multiplexer":    e.cfg.Multiplexer,
		"worktree_roots": e.cfg.WorktreeRoots,
		"favorites":      e.cfg.Favorites,
		"session_naming": naming,
		"groups_file":    e.cfg.GroupsConfig,
		"groups":         toGroupArgs(e.cfg.Groups),
		"hooks_file":     e.cfg.HooksConfig,
		"hooks":          h,
		"problems":       problems,
	}, nil
}

type listReposArgs struct {
	Query   string `json:"query,omitempty" jsonschema:"only repos whose name contains this (case-insensitive)"`
	Refresh bool   `json:"refresh,omitempty" jsonschema:"rescan $HOME instead of using the 60s cache"`
}

type repoInfo struct {
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	Favorite bool     `json:"favorite,omitempty"`
	Groups   []string `json:"groups,omitempty"`
}

func mcpListRepos(e *callEnv, in listReposArgs) (map[string]any, error) {
	fav := map[string]bool{}
	for _, f := range e.cfg.Favorites {
		fav[f] = true
	}
	seen := map[string]bool{}
	var repos []repoInfo
	for _, d := range e.repoDirs(in.Refresh) {
		name := filepath.Base(d)
		if seen[name] || (in.Query != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(in.Query))) {
			continue
		}
		seen[name] = true
		r := repoInfo{Name: name, Path: d, Favorite: fav[name]}
		for _, g := range e.cfg.GroupsForRepo(name) {
			r.Groups = append(r.Groups, g.Name)
		}
		repos = append(repos, r)
	}
	sort.SliceStable(repos, func(i, j int) bool {
		if repos[i].Favorite != repos[j].Favorite {
			return repos[i].Favorite
		}
		return repos[i].Name < repos[j].Name
	})
	return map[string]any{"repos": repos}, nil
}

type listWorktreesArgs struct {
	Repo string `json:"repo,omitempty" jsonschema:"only this repo"`
	Task string `json:"task,omitempty" jsonschema:"only this task"`
}

type worktreeInfo struct {
	Repo      string `json:"repo"`
	Task      string `json:"task"`
	Branch    string `json:"branch,omitempty"`
	Path      string `json:"path"`
	Session   string `json:"session,omitempty"`
	Live      bool   `json:"live"`
	Agent     string `json:"agent,omitempty"`
	Group     string `json:"group,omitempty"`
	GroupLive string `json:"group_windows_live,omitempty"`
	LastUsed  string `json:"last_used,omitempty"`
}

func mcpListWorktrees(e *callEnv, in listWorktreesArgs) (map[string]any, error) {
	var deps worktree.Deps
	var note string
	if err := e.selectMux(); err != nil {
		// Still useful without session info: the worktrees are on disk.
		note = "no session info: " + err.Error()
		deps = worktree.Deps{Branch: worktree.GitBranch}
	} else {
		deps = worktree.LiveDeps(agentstate.Dir(), agentstate.StaleAfter, agentstate.Read)
	}
	var out []worktreeInfo
	for _, r := range worktree.BuildRows(e.cfg, e.cfg.WorktreeRoots, deps) {
		if (in.Repo != "" && r.Repo != in.Repo) || (in.Task != "" && r.Task != in.Task) {
			continue
		}
		w := worktreeInfo{Repo: r.Repo, Task: r.Task, Branch: r.Branch, Path: r.Path, Session: r.Session, Live: r.Live, Agent: r.Agent, Group: r.GroupName}
		if r.GroupSize > 0 {
			w.GroupLive = fmt.Sprintf("%d/%d", r.GroupLive, r.GroupSize)
		}
		if !r.LastUsed.IsZero() {
			w.LastUsed = r.LastUsed.Format(time.RFC3339)
		}
		out = append(out, w)
	}
	res := map[string]any{"worktrees": out}
	if note != "" {
		res["note"] = note
	}
	return res, nil
}

type listBranchesArgs struct {
	Repo        string `json:"repo" jsonschema:"repo folder name"`
	MaxAgeHours int    `json:"max_age_hours,omitempty" jsonschema:"only branches with a commit this recent; 0 = all"`
}

func mcpListBranches(e *callEnv, in listBranchesArgs) (map[string]any, error) {
	root, err := e.repoRoot(in.Repo)
	if err != nil {
		return nil, err
	}
	type branch struct {
		Name           string `json:"name"`
		Task           string `json:"task"`
		LastCommit     string `json:"last_commit"`
		InMainCheckout bool   `json:"checked_out_in_main_checkout,omitempty"`
	}
	var out []branch
	for _, b := range worktree.BranchCandidates(root, time.Duration(in.MaxAgeHours)*time.Hour) {
		out = append(out, branch{Name: b.Name, Task: worktree.TaskNameFor(b.Name), LastCommit: b.Tip.Format(time.RFC3339), InMainCheckout: b.InMain})
	}
	return map[string]any{"repo": in.Repo, "base_branch": worktree.BaseBranch(root), "branches": out}, nil
}

// --- worktree lifecycle ---

type createWorktreeArgs struct {
	Repo   string `json:"repo,omitempty" jsonschema:"repo folder name; give this or group"`
	Group  string `json:"group,omitempty" jsonschema:"process group name: creates the worktree in every member repo; give this or repo"`
	Task   string `json:"task,omitempty" jsonschema:"task name (folder + session name; a valid branch name without slashes). Defaults to the branch name with / replaced by -"`
	Branch string `json:"branch,omitempty" jsonschema:"branch to use: an existing local branch is checked out as-is, otherwise a new branch with this name is created off the default branch. Defaults to the task name"`
	Force  bool   `json:"force,omitempty" jsonschema:"if the existing branch is checked out elsewhere, move it: the main checkout is switched to its base branch, another worktree is detached. Only after the user agreed"`
}

type createdWorktree struct {
	Repo string `json:"repo"`
	Path string `json:"path"`
}

func mcpCreateWorktree(e *callEnv, in createWorktreeArgs) (map[string]any, error) {
	if (in.Repo == "") == (in.Group == "") {
		return nil, fmt.Errorf("give exactly one of repo or group")
	}
	if in.Task == "" && in.Branch == "" {
		return nil, fmt.Errorf("give a task name and/or a branch")
	}
	primary, extras := in.Repo, []string(nil)
	if in.Group != "" {
		g, ok := e.cfg.GroupByName(in.Group)
		if !ok {
			return nil, fmt.Errorf("no group named %q", in.Group)
		}
		primary = g.Processes[0].Repo
		seen := map[string]bool{primary: true}
		for _, p := range g.Processes {
			if !seen[p.Repo] {
				seen[p.Repo] = true
				extras = append(extras, p.Repo)
			}
		}
	}
	root, err := e.repoRoot(primary)
	if err != nil {
		return nil, err
	}

	branch, task := in.Branch, in.Task
	if branch == "" {
		branch = task
	}
	if task == "" {
		task = worktree.TaskNameFor(branch)
	}
	if err := worktree.ValidateTaskName(task); err != nil {
		return nil, err
	}
	existing := worktree.HasBranch(root, branch)
	if existing && task != worktree.TaskNameFor(branch) {
		return nil, fmt.Errorf("existing branch %s always maps to task %s — omit task or pass task=%s", branch, worktree.TaskNameFor(branch), worktree.TaskNameFor(branch))
	}

	var wt string
	if existing {
		var c *worktree.Conflict
		wt, c, err = worktree.AddExisting(e.cfg, root, branch, in.Force)
		if err != nil {
			return nil, err
		}
		if c != nil {
			what := "detached"
			if c.IsMain {
				what = "switched to " + worktree.BaseBranch(root)
			}
			return map[string]any{
				"created":  false,
				"conflict": fmt.Sprintf("branch %s is checked out in %s", branch, c.Path),
				"resolve":  fmt.Sprintf("ask the user, then call again with force=true: %s would be %s", c.Path, what),
			}, nil
		}
	} else if wt, err = worktree.NewTaskOnBranch(e.cfg, root, task, branch); err != nil {
		return nil, err
	}

	created := []createdWorktree{{Repo: primary, Path: wt}}
	var notes []string
	for _, repo := range extras {
		r, err := e.repoRoot(repo)
		if err != nil {
			notes = append(notes, repo+": "+err.Error())
			continue
		}
		var path string
		if worktree.HasBranch(r, branch) {
			// Never forced: the user's consent (if any) covered the
			// primary repo's checkout, not this one.
			var c *worktree.Conflict
			path, c, err = worktree.AddExisting(e.cfg, r, branch, false)
			if err == nil && c != nil {
				err = fmt.Errorf("%s is already checked out in %s — skipped", branch, c.Path)
			}
		} else {
			path, err = worktree.NewTaskOnBranch(e.cfg, r, task, branch)
		}
		if err != nil {
			notes = append(notes, repo+": "+err.Error())
			continue
		}
		created = append(created, createdWorktree{Repo: repo, Path: path})
	}
	res := map[string]any{
		"created":   true,
		"task":      task,
		"branch":    branch,
		"session":   worktree.SessionName(e.cfg, primary, task),
		"worktrees": created,
	}
	if len(notes) > 0 {
		res["notes"] = notes
	}
	if len(e.cfg.GroupsForRepo(primary)) > 0 {
		res["next"] = "start_group with task=" + task + " to start the group's processes"
	}
	return res, nil
}

type taskArgs struct {
	Repo string `json:"repo" jsonschema:"repo folder name"`
	Task string `json:"task" jsonschema:"task name"`
}

func mcpKillSession(e *callEnv, in taskArgs) (map[string]any, error) {
	if err := e.selectMux(); err != nil {
		return nil, err
	}
	ops := worktree.LiveTmuxOps()
	sessions := worktree.SessionsFor(e.cfg, ops, in.Repo, in.Task)
	if len(sessions) == 0 {
		return map[string]any{"killed": []string{}, "note": "no session for " + in.Repo + "/" + in.Task}, nil
	}
	worktree.KillSessionFor(e.cfg, ops, in.Repo, in.Task)
	return map[string]any{"killed": sessions}, nil
}

func mcpEndTask(e *callEnv, in taskArgs) (map[string]any, error) {
	if err := e.selectMux(); err != nil {
		return nil, err
	}
	if worktree.FindWorktree(e.cfg.WorktreeRoots, in.Repo, in.Task) == "" {
		return nil, fmt.Errorf("no %s/%s worktree (see list_worktrees)", in.Repo, in.Task)
	}
	repos := e.repoDirs(false)
	if worktree.FindRepoRoot(repos, in.Repo) == "" {
		repos = e.repoDirs(true)
	}
	return map[string]any{"summary": worktree.EndTask(e.cfg, worktree.LiveTmuxOps(), repos, in.Repo, in.Task)}, nil
}

// --- groups ---

type groupTaskArgs struct {
	Task  string `json:"task" jsonschema:"task name"`
	Repo  string `json:"repo,omitempty" jsonschema:"a member repo that has a worktree for the task; defaults to the first member of group that does"`
	Group string `json:"group,omitempty" jsonschema:"group name; required when the repo belongs to several groups and the task's worktrees don't decide between them"`
}

// resolvePlan finds the group and anchor worktree for a group start and
// builds its plan.
func (e *callEnv) resolvePlan(in groupTaskArgs) (worktree.GroupPlan, string, error) {
	if err := worktree.ValidateTaskName(in.Task); err != nil {
		return worktree.GroupPlan{}, "", err
	}
	repo := in.Repo
	if repo == "" {
		g, ok := e.cfg.GroupByName(in.Group)
		if !ok {
			return worktree.GroupPlan{}, "", fmt.Errorf("give repo, or a valid group name")
		}
		for _, p := range g.Processes {
			if worktree.FindWorktree(e.cfg.WorktreeRoots, p.Repo, in.Task) != "" {
				repo = p.Repo
				break
			}
		}
		if repo == "" {
			return worktree.GroupPlan{}, "", fmt.Errorf("no member of group %s has a %s worktree — create_worktree first", in.Group, in.Task)
		}
	}
	wt := worktree.FindWorktree(e.cfg.WorktreeRoots, repo, in.Task)
	if wt == "" {
		return worktree.GroupPlan{}, "", fmt.Errorf("no %s/%s worktree — create_worktree first", repo, in.Task)
	}
	g, err := worktree.ResolveGroupFor(e.cfg, repo, in.Task, in.Group)
	var amb *worktree.AmbiguousGroupError
	if errors.As(err, &amb) {
		return worktree.GroupPlan{}, "", fmt.Errorf("%v — pass group", err)
	}
	if err != nil {
		return worktree.GroupPlan{}, "", err
	}
	plan, err := worktree.PlanGroup(e.cfg, g, repo, in.Task, wt)
	return plan, wt, err
}

type procPlanOut struct {
	Label     string            `json:"label"`
	Repo      string            `json:"repo"`
	Dir       string            `json:"dir,omitempty"`
	Cmd       string            `json:"cmd,omitempty"`
	Port      int               `json:"port,omitempty"`
	EnvFile   string            `json:"env_file,omitempty"`
	Env       map[string]string `json:"env_updates,omitempty"`
	PatchFile string            `json:"patch_file,omitempty"`
	PatchKey  string            `json:"patch_key,omitempty"`
	Skip      string            `json:"skipped,omitempty"`
}

func planOut(p worktree.GroupPlan) map[string]any {
	var procs []procPlanOut
	for _, pp := range p.Procs {
		o := procPlanOut{Label: pp.Label, Repo: pp.Repo, Skip: pp.Skip}
		if pp.Skip == "" {
			o.Dir, o.Cmd, o.Port, o.PatchFile, o.PatchKey = pp.Dir, pp.Cmd, pp.Port, pp.PatchFile, pp.PatchKey
			if len(pp.Env) > 0 {
				o.EnvFile, o.Env = pp.EnvFile, pp.Env
			}
		}
		procs = append(procs, o)
	}
	return map[string]any{"group": p.Group, "session": p.Session, "fe_port": p.FEPort, "be_port": p.BEPort, "processes": procs}
}

func mcpPlanGroup(e *callEnv, in groupTaskArgs) (map[string]any, error) {
	// Best-effort: with a backend, already-running windows are recognised.
	e.selectMux()
	plan, _, err := e.resolvePlan(in)
	if err != nil {
		return nil, err
	}
	return planOut(plan), nil
}

func mcpStartGroup(e *callEnv, in groupTaskArgs) (map[string]any, error) {
	if err := e.selectMux(); err != nil {
		return nil, err
	}
	plan, wt, err := e.resolvePlan(in)
	if err != nil {
		return nil, err
	}
	ensureLoginProxy(e.cfg)
	spawned, notes, err := worktree.ApplyGroupPlan(plan, wt)
	if err != nil {
		return nil, fmt.Errorf("%w (started so far: %v)", err, spawned)
	}
	res := planOut(plan)
	res["started"] = spawned
	if len(notes) > 0 {
		res["notes"] = notes
	}
	if e.cfg.Multiplexer == "tmux" {
		res["attach"] = "tmux attach -t " + plan.Session
	}
	return res, nil
}

// processArgs mirrors config.Process with schema descriptions and every
// optional key optional — config.Process has neither.
type processArgs struct {
	Label      string   `json:"label" jsonschema:"window name, unique within the group (e.g. fe, be)"`
	Repo       string   `json:"repo" jsonschema:"repo folder name"`
	Cmd        string   `json:"cmd" jsonschema:"command run in the repo's task worktree; {port} is this process's port, {fe_port}/{be_port} either side's"`
	PortRange  string   `json:"port_range,omitempty" jsonschema:"fe (3000-3999) or be (8000-8999): gives the process a stable per-task port. Omit when the port is hardcoded in the repo"`
	FixedPort  int      `json:"fixed_port,omitempty" jsonschema:"the hardcoded port of a process without port_range, so ork can warn when it's taken"`
	EnvVar     string   `json:"env_var,omitempty" jsonschema:"dotenv key rewritten to http://localhost:<be port><env_path>"`
	EnvPath    string   `json:"env_path,omitempty" jsonschema:"URL path appended to env_var's value, starting with /"`
	URLEnvVars []string `json:"url_env_vars,omitempty" jsonschema:"dotenv keys rewritten to the task's own fe origin (e.g. NEXTAUTH_URL)"`
	PatchFile  string   `json:"patch_file,omitempty" jsonschema:"file (relative to the worktree) whose 'patch_key = \"http://localhost:N...\"' line is rewritten to the be port — use the source template, not generated output"`
	PatchKey   string   `json:"patch_key,omitempty" jsonschema:"key to rewrite in patch_file"`
	EnvFile    string   `json:"env_file,omitempty" jsonschema:"dotenv file for env updates, relative to the worktree; default .env.local"`
	TaskEnvVar *string  `json:"task_env_var,omitempty" jsonschema:"dotenv key that receives the task name; defaults to NEXT_PUBLIC_ORK_TASK for processes with env updates; empty string disables"`
}

type groupArgs struct {
	Name      string        `json:"name" jsonschema:"group name"`
	Processes []processArgs `json:"processes" jsonschema:"processes started together for a task, one window each"`
}

func toGroupArgs(gs []config.Group) []groupArgs {
	out := []groupArgs{}
	for _, g := range gs {
		a := groupArgs{Name: g.Name}
		for _, p := range g.Processes {
			a.Processes = append(a.Processes, processArgs(p))
		}
		out = append(out, a)
	}
	return out
}

type setGroupArgs struct {
	Group groupArgs `json:"group" jsonschema:"the group; replaces any existing group with the same name"`
}

func mcpSetGroup(e *callEnv, in setGroupArgs) (map[string]any, error) {
	g := config.Group{Name: in.Group.Name}
	for _, p := range in.Group.Processes {
		g.Processes = append(g.Processes, config.Process(p))
	}
	replaced, err := config.UpsertGroup(e.cfg.GroupsConfig, g)
	if err != nil {
		return nil, err
	}
	return e.afterConfigWrite(map[string]any{"saved": g.Name, "replaced": replaced, "file": e.cfg.GroupsConfig})
}

type deleteGroupArgs struct {
	Name string `json:"name" jsonschema:"group name"`
}

func mcpDeleteGroup(e *callEnv, in deleteGroupArgs) (map[string]any, error) {
	if err := config.DeleteGroup(e.cfg.GroupsConfig, in.Name); err != nil {
		return nil, err
	}
	return e.afterConfigWrite(map[string]any{"deleted": in.Name, "file": e.cfg.GroupsConfig})
}

type setRepoHookArgs struct {
	Repo    string `json:"repo" jsonschema:"repo folder name"`
	Command string `json:"command" jsonschema:"bash command run in each new worktree of the repo; empty removes the hook. If you copy a direnv .envrc, also run 'direnv allow .'"`
}

func mcpSetRepoHook(e *callEnv, in setRepoHookArgs) (map[string]any, error) {
	if err := config.SetRepoHook(e.cfg.HooksConfig, in.Repo, in.Command); err != nil {
		return nil, err
	}
	return e.afterConfigWrite(map[string]any{"repo": in.Repo, "command": in.Command, "file": e.cfg.HooksConfig})
}

// afterConfigWrite reloads the config so the caller sees any problem the
// edit introduced (or left in place) right away.
func (e *callEnv) afterConfigWrite(res map[string]any) (map[string]any, error) {
	cfg, err := config.Load(configPath())
	if err != nil {
		return nil, err
	}
	if len(cfg.Problems) > 0 {
		res["problems"] = cfg.Problems
	}
	return res, nil
}
