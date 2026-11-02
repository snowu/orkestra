package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"orkestra/internal/config"
	"orkestra/internal/mux"
)

type Row struct {
	Repo, Task, Branch  string
	Session, Cmd, Agent string
	Live                bool
	FELive, BELive      bool      // "fe"/"be" windows present in the base session (ctrl-g/ctrl-a)
	GroupLive           int       // count of this row's group's process labels with a live window
	GroupSize           int       // total processes in this row's group; 0 if repo is in no group
	GroupName           string    // name of the resolved group for (Repo, Task); "" if none/ambiguous
	LastUsed            time.Time // zero = never used via ork
	Path                string
}

// Deps injects the impure lookups so BuildRows is testable without a tmux
// server or real git repos.
type Deps struct {
	Panes          []mux.Pane
	HasSession     func(name string) bool
	SessionWindows func(session string) []string
	AgentState     func(session string) string
	Branch         func(wt string) string
	AccessTime     func(repo, task string) time.Time
}

// AccessDir returns ~/.cache/ork/access.
func AccessDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache/ork/access")
}

// AccessFile is the marker whose mtime is "last used via ork".
func AccessFile(repo, task string) string {
	return filepath.Join(AccessDir(), repo+"__"+task)
}

// TouchAccess records "actually landed here via ork" — folder mtime is not
// a substitute (it moves on file edits, not cd/attach).
func TouchAccess(repo, task string) {
	os.MkdirAll(AccessDir(), 0o755)
	p := AccessFile(repo, task)
	now := time.Now()
	if err := os.Chtimes(p, now, now); err != nil {
		os.WriteFile(p, nil, 0o644)
	}
}

// GitBranch returns the current branch of a worktree ("" on error).
func GitBranch(wt string) string {
	out, err := exec.Command("git", "-C", wt, "branch", "--show-current").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// BuildRows assembles the picker rows.
//
// Session resolution runs ONCE PER TASK from a single pane snapshot —
// per-row resolution let two sibling worktrees sharing a task (BE+FE under
// one task name) resolve to different sessions, the exact "waiting shows
// in one folder but not the other" bug. Agent state is read in the same
// per-task pass so siblings can't observe different values within one
// build either.
func BuildRows(cfg config.Config, roots []string, d Deps) []Row {
	dirs := AllWorktreeDirs(roots)

	type sess struct{ name, cmd, agent string }
	taskSess := map[string]*sess{}
	for _, wt := range dirs {
		t := filepath.Base(wt)
		if _, done := taskSess[t]; done {
			continue
		}
		var s *sess
		for _, p := range d.Panes {
			if p.CWD == wt {
				s = &sess{name: p.Session, cmd: p.Cmd}
				break
			}
		}
		if s == nil && d.HasSession != nil && d.HasSession(t) {
			cmd := ""
			for _, p := range d.Panes {
				if p.Session == t {
					cmd = p.Cmd
					break
				}
			}
			s = &sess{name: t, cmd: cmd}
		}
		if s != nil && d.AgentState != nil {
			s.agent = d.AgentState(s.name)
		}
		taskSess[t] = s // nil means "no session", memoized too
	}

	// Group resolution also runs ONCE PER TASK, over every repo present
	// for that task — not once per row/repo independently. Per-row
	// resolution let two sibling repos of what should be one group
	// disagree about which group they belonged to whenever a shared repo
	// (e.g. a backend in both a 2-process pair-shaped group and a bigger
	// federated group) had its tie broken differently depending on which
	// OTHER repo happened to be present for that repo's own resolution —
	// the exact bug this task is fixing, just recurring at the picker
	// layer: rows that should visually pair up wouldn't, because they'd
	// silently land on different GroupNames. taskGroup below is resolved
	// from the task's full repo set so every member of the winning group
	// gets the SAME name.
	taskGroup := map[string]*resolvedTaskGroup{}
	for _, wt := range dirs {
		t := filepath.Base(wt)
		if _, done := taskGroup[t]; done {
			continue
		}
		taskGroup[t] = resolveTaskGroup(cfg, roots, dirs, t)
	}

	rows := make([]Row, 0, len(dirs))
	for _, wt := range dirs {
		task := filepath.Base(wt)
		repo := filepath.Base(filepath.Dir(wt))
		r := Row{Repo: repo, Task: task, Path: wt}
		if d.Branch != nil {
			r.Branch = d.Branch(wt)
		}
		if d.AccessTime != nil {
			r.LastUsed = d.AccessTime(repo, task)
		}
		if s := taskSess[task]; s != nil {
			r.Session, r.Cmd, r.Agent, r.Live = s.name, s.cmd, s.agent, true
		}
		var windows []string
		if d.SessionWindows != nil {
			name := SessionName(cfg, repo, task)
			windows = d.SessionWindows(name)
			for _, w := range windows {
				switch w {
				case "fe":
					r.FELive = true
				case "be":
					r.BELive = true
				}
			}
		}
		// Group membership (and GroupName in particular) must not depend
		// on SessionWindows being wired up — it's a config/worktree fact,
		// not a liveness fact, and other code (sort adjacency, sibling
		// detection, the picker) needs it regardless of whether live-window
		// info is available.
		if tg := taskGroup[task]; tg != nil && tg.memberRepos[repo] {
			r.GroupName = tg.group.Name
			r.GroupSize = len(tg.group.Processes)
			live := map[string]bool{}
			for _, w := range windows {
				live[w] = true
			}
			for _, p := range tg.group.Processes {
				if live[p.Label] {
					r.GroupLive++
				}
			}
		}
		rows = append(rows, r)
	}
	// Most recently used first; never-used (zero time) sort last. Rows
	// that are members of a configured process group under one task share
	// the newest sibling's time so they always sort adjacent — the picker
	// draws them linked by a bracket.
	group := pairGroups(cfg, rows)
	effTime := func(r Row) time.Time {
		if g, ok := group[r.Repo+"/"+r.Task]; ok {
			return g
		}
		return r.LastUsed
	}
	// clusterKey: rows sharing this string are one linked cluster and must
	// end up CONTIGUOUS in the sorted output, not just tied on effTime.
	// Two group members under the same task share the exact same key
	// (GroupName+"/"+Task); every other row gets a key unique to itself
	// (Repo+"/"+Task). Without this, an all-zero-time (never-used) group
	// of 3+ members sorted purely by effTime ties broke on the ORIGINAL
	// (alphabetical directory-scan) order — an unrelated repo whose name
	// falls alphabetically between two group members' names would land
	// in the middle of the cluster instead of outside it, splitting the
	// bracket the picker draws.
	clusterKey := func(r Row) string {
		if r.GroupName != "" {
			return r.GroupName + "/" + r.Task
		}
		return r.Repo + "/" + r.Task
	}
	sort.SliceStable(rows, func(i, j int) bool {
		ti, tj := effTime(rows[i]), effTime(rows[j])
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return clusterKey(rows[i]) < clusterKey(rows[j])
	})
	return rows
}

// resolvedTaskGroup is the outcome of resolving ALL repos present for one
// task down to a single winning group (or none), for display/sibling
// purposes — see resolveTaskGroup.
type resolvedTaskGroup struct {
	group       config.Group
	memberRepos map[string]bool // group.Processes' repos, for O(1) row lookup
}

// resolveTaskGroup picks ONE group to represent task's whole row cluster
// in the picker, considering every repo that actually has a worktree for
// task — not just one repo's candidate list in isolation. Candidates are
// every group touched by ANY present repo for this task; each is scored
// by how many of ITS OWN member repos have a present worktree for task
// (same "most materialized" rule as ResolveGroup). The highest-scoring
// group wins; on a genuine tie, resolveTaskGroup deliberately declines
// rather than picking arbitrarily per-row — nil is returned, so no row
// for this task gets a GroupName, and the picker shows no (potentially
// wrong) bracket instead of a partial/inconsistent one. This is stricter
// than ResolveGroup's ambiguous-but-still-returns-a-value contract,
// because ResolveGroup's ambiguity is surfaced to a human deciding what
// to launch, while this feeds a passive cosmetic pairing indicator with no
// place to show "ambiguous" per row.
func resolveTaskGroup(cfg config.Config, roots []string, allDirs []string, task string) *resolvedTaskGroup {
	presentRepos := map[string]bool{}
	for _, wt := range allDirs {
		if filepath.Base(wt) == task {
			presentRepos[filepath.Base(filepath.Dir(wt))] = true
		}
	}

	seen := map[string]bool{}
	var candidates []config.Group
	for repo := range presentRepos {
		for _, g := range cfg.GroupsForRepo(repo) {
			if !seen[g.Name] {
				seen[g.Name] = true
				candidates = append(candidates, g)
			}
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	best := -1
	var bestGroups []config.Group
	for _, cand := range candidates {
		present := 0
		for _, p := range cand.Processes {
			if presentRepos[p.Repo] {
				present++
			}
		}
		switch {
		case present > best:
			best = present
			bestGroups = []config.Group{cand}
		case present == best:
			bestGroups = append(bestGroups, cand)
		}
	}
	if len(bestGroups) != 1 {
		return nil // tie: decline rather than guess which one to draw
	}

	members := map[string]bool{}
	for _, p := range bestGroups[0].Processes {
		members[p.Repo] = true
	}
	return &resolvedTaskGroup{group: bestGroups[0], memberRepos: members}
}

// ResolveGroup picks the group to act on for (repo, task) when repo is a
// member of multiple configured groups (e.g. a shared backend in both a
// classic pair-shaped group and a larger federated one). A group is really
// a set of repos sharing a TASK, so the right group is whichever one is
// actually materialised on disk for this task — the candidate whose member
// repos have the most EXISTING worktrees for task. If exactly one
// candidate has that maximum, it's returned with ambiguous == nil. If two
// or more tie (including the "no worktrees exist yet for any candidate"
// case, where every candidate ties at 0), the first tied candidate is
// returned alongside ALL tied candidates in ambiguous, so the caller can
// refuse to guess and prompt instead.
func ResolveGroup(cfg config.Config, roots []string, repo, task string) (g config.Group, ambiguous []config.Group, ok bool) {
	candidates := cfg.GroupsForRepo(repo)
	if len(candidates) == 0 {
		return config.Group{}, nil, false
	}
	if len(candidates) == 1 {
		return candidates[0], nil, true
	}

	best := -1
	var bestGroups []config.Group
	for _, cand := range candidates {
		present := 0
		for _, p := range cand.Processes {
			if FindWorktree(roots, p.Repo, task) != "" {
				present++
			}
		}
		switch {
		case present > best:
			best = present
			bestGroups = []config.Group{cand}
		case present == best:
			bestGroups = append(bestGroups, cand)
		}
	}

	if len(bestGroups) == 1 {
		return bestGroups[0], nil, true
	}
	return bestGroups[0], bestGroups, true
}

// pairGroups maps "repo/task" -> shared sort time for rows where two or
// more members of a configured group have a worktree under the same task
// name, so all N siblings sort adjacent.
func pairGroups(cfg config.Config, rows []Row) map[string]time.Time {
	// task -> group name -> rows present for that group under that task.
	byTaskGroup := map[string]map[string][]Row{}
	for _, r := range rows {
		if r.GroupName == "" {
			continue
		}
		g := config.Group{Name: r.GroupName}
		m := byTaskGroup[r.Task]
		if m == nil {
			m = map[string][]Row{}
			byTaskGroup[r.Task] = m
		}
		m[g.Name] = append(m[g.Name], r)
	}
	out := map[string]time.Time{}
	for _, groups := range byTaskGroup {
		for _, sibs := range groups {
			if len(sibs) < 2 {
				continue
			}
			t := sibs[0].LastUsed
			for _, r := range sibs[1:] {
				if r.LastUsed.After(t) {
					t = r.LastUsed
				}
			}
			for _, r := range sibs {
				out[r.Repo+"/"+r.Task] = t
			}
		}
	}
	return out
}

// GroupSiblings reports whether rows a and b belong to the same resolved
// process group under the same task — the picker's cue to draw them
// linked, generalizing PairSiblings to N-process groups. Uses each row's
// already-resolved GroupName rather than re-resolving, so it agrees with
// whatever BuildRows decided.
func GroupSiblings(cfg config.Config, a, b Row) bool {
	if a.Task != b.Task || a.Repo == b.Repo {
		return false
	}
	if a.GroupName == "" || b.GroupName == "" {
		return false
	}
	return a.GroupName == b.GroupName
}

// LiveDeps builds Deps against the real system.
func LiveDeps(agentStateDir string, staleAfter time.Duration, readState func(dir, session string, staleAfter time.Duration) string) Deps {
	return Deps{
		Panes:          mux.ListPanes(),
		HasSession:     mux.HasSession,
		SessionWindows: mux.SessionWindowNames,
		AgentState:     func(s string) string { return readState(agentStateDir, s, staleAfter) },
		Branch:         GitBranch,
		AccessTime: func(repo, task string) time.Time {
			st, err := os.Stat(AccessFile(repo, task))
			if err != nil {
				return time.Time{}
			}
			return st.ModTime()
		},
	}
}
