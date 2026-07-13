package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"orkestra/internal/config"
	"orkestra/internal/tmux"
)

type Row struct {
	Repo, Task, Branch  string
	Session, Cmd, Agent string
	Live                bool
	FELive, BELive      bool      // <session>_fe / <session>_be running (ctrl-g)
	LastUsed            time.Time // zero = never used via ork
	Path                string
}

// Deps injects the impure lookups so BuildRows is testable without a tmux
// server or real git repos.
type Deps struct {
	Panes      []tmux.Pane
	HasSession func(name string) bool
	AgentState func(session string) string
	Branch     func(wt string) string
	AccessTime func(repo, task string) time.Time
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
		if d.HasSession != nil {
			name := SessionName(cfg, repo, task)
			r.FELive = d.HasSession(name + "_fe")
			r.BELive = d.HasSession(name + "_be")
		}
		rows = append(rows, r)
	}
	// Most recently used first; never-used (zero time) sort last.
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].LastUsed.After(rows[j].LastUsed)
	})
	return rows
}

// LiveDeps builds Deps against the real system.
func LiveDeps(agentStateDir string, staleAfter time.Duration, readState func(dir, session string, staleAfter time.Duration) string) Deps {
	return Deps{
		Panes:      tmux.ListPanes(),
		HasSession: tmux.HasSession,
		AgentState: func(s string) string { return readState(agentStateDir, s, staleAfter) },
		Branch:     GitBranch,
		AccessTime: func(repo, task string) time.Time {
			st, err := os.Stat(AccessFile(repo, task))
			if err != nil {
				return time.Time{}
			}
			return st.ModTime()
		},
	}
}
