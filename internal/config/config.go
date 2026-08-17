// Package config parses ~/.ork.conf. The file is bash syntax (it's still
// sourced by the shell shims), but ork only ever needs the handful of
// KEY=value / KEY=(a b c) assignments below — this is a line parser for
// exactly those forms, not a bash evaluator. Unknown keys are ignored so
// users can keep arbitrary shell in the file for their own wrappers.
package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	WorktreeRoots       []string
	Favorites           []string
	ScanMaxDepth        int
	ScopeSessionsToRepo bool
	HooksConfig         string
	ClaudePersonalDirs  []string

	// Multiplexer is the session backend ork drives: "tmux" or "herdr".
	// Chosen at install time and forced — ork never falls back to the
	// other one even when both are installed.
	Multiplexer string

	// GroupsConfig is the JSON file declaring N-process groups (same
	// no-code-execution rationale as HooksConfig). Optional.
	GroupsConfig string
	Groups       []Group
}

// Process is one command in a Group: a repo, what to run in its worktree,
// and how it gets a port.
//
// PortRange is OPTIONAL ("fe" = 3000-3999, "be" = 8000-8999, "" = none).
// A process whose port is hardcoded in its own repo — a federated MFE
// remote pinned in ebury-web.config.json, say — leaves it empty and must
// not use {port} in Cmd. FixedPort is that hardcoded port, declared here
// only so ork can warn when it is already in use; ork never parses the
// repo's own config to find it.
type Process struct {
	Label      string   `json:"label"`
	Repo       string   `json:"repo"`
	Cmd        string   `json:"cmd"`
	PortRange  string   `json:"port_range"`
	FixedPort  int      `json:"fixed_port"`
	EnvVar     string   `json:"env_var"`
	EnvPath    string   `json:"env_path"`
	URLEnvVars []string `json:"url_env_vars"`
}

// Group is N processes spawned together for one task.
type Group struct {
	Name      string    `json:"name"`
	Processes []Process `json:"processes"`
}

// Validate rejects groups that would produce broken windows: labels name
// the mux windows, so they must exist and be unique within the group.
func (g Group) Validate() error {
	if len(g.Processes) == 0 {
		return fmt.Errorf("group %q has no processes", g.Name)
	}
	seen := map[string]bool{}
	for _, p := range g.Processes {
		if p.Label == "" {
			return fmt.Errorf("group %q has a process with no label", g.Name)
		}
		if seen[p.Label] {
			return fmt.Errorf("group %q has duplicate label %q", g.Name, p.Label)
		}
		seen[p.Label] = true
	}
	return nil
}

// loadGroups reads GroupsConfig. Invalid groups are dropped with a note on
// stderr rather than failing startup — a broken groups file must not stop
// the user reaching their worktrees.
func (c *Config) loadGroups() {
	data, err := os.ReadFile(c.GroupsConfig)
	if err != nil {
		return
	}
	var fromFile []Group
	if json.Unmarshal(data, &fromFile) != nil {
		fmt.Fprintln(os.Stderr, "ork: bad "+c.GroupsConfig+" — ignoring")
		return
	}
	for _, g := range fromFile {
		if err := g.Validate(); err != nil {
			fmt.Fprintln(os.Stderr, "ork: "+err.Error()+" — ignoring")
			continue
		}
		c.Groups = append(c.Groups, g)
	}
}

// GroupsForRepo returns every group repo belongs to, in file order. A repo
// can be a member of more than one group with different settings (e.g. a
// backend shared between a classic 2-process group and a larger federated
// group) — callers that need to pick ONE group must disambiguate using
// which groups actually have worktrees for the task at hand (see
// worktree.ResolveGroup); picking blindly here would silently make one of
// the groups unreachable.
func (c Config) GroupsForRepo(repo string) []Group {
	var out []Group
	for _, g := range c.Groups {
		for _, proc := range g.Processes {
			if proc.Repo == repo {
				out = append(out, g)
				break
			}
		}
	}
	return out
}

func defaults() Config {
	home, _ := os.UserHomeDir()
	return Config{
		WorktreeRoots: []string{filepath.Join(home, "worktrees")},
		ScanMaxDepth:  3,
		HooksConfig:   filepath.Join(home, ".config/ork/hooks.json"),
		Multiplexer:   "tmux",
		GroupsConfig:  filepath.Join(home, ".config/ork/groups.json"),
	}
}

// Load reads path if it exists; a missing file just yields defaults.
func Load(path string) (Config, error) {
	cfg := defaults()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "ORK_WORKTREES_ROOTS":
			if v := parseArray(val); len(v) > 0 {
				cfg.WorktreeRoots = v
			}
		case "ORK_FAVORITES":
			cfg.Favorites = parseArray(val)
		case "CLAUDE_PERSONAL_DIRS":
			cfg.ClaudePersonalDirs = parseArray(val)
		case "ORK_SCAN_MAXDEPTH":
			if n, err := strconv.Atoi(unquote(val)); err == nil && n > 0 {
				cfg.ScanMaxDepth = n
			}
		case "ORK_SCOPE_SESSIONS_TO_REPO":
			cfg.ScopeSessionsToRepo = unquote(val) == "1"
		case "ORK_MULTIPLEXER":
			if v := unquote(val); v != "" {
				cfg.Multiplexer = v
			}
		case "ORK_HOOKS_CONFIG":
			if v := expand(unquote(val)); v != "" {
				cfg.HooksConfig = v
			}
		case "ORK_GROUPS_CONFIG":
			if v := expand(unquote(val)); v != "" {
				cfg.GroupsConfig = v
			}
		}
	}
	if err := sc.Err(); err != nil {
		return cfg, err
	}
	cfg.loadGroups()
	return cfg, nil
}

// parseArray handles bash `(elem "elem" 'elem')` values.
func parseArray(val string) []string {
	val = strings.TrimSpace(val)
	if !strings.HasPrefix(val, "(") || !strings.HasSuffix(val, ")") {
		// scalar assigned where an array is expected — treat as 1 element
		if v := expand(unquote(val)); v != "" {
			return []string{v}
		}
		return nil
	}
	inner := strings.TrimSpace(val[1 : len(val)-1])
	if inner == "" {
		return nil
	}
	var out []string
	for _, tok := range splitTokens(inner) {
		if v := expand(unquote(tok)); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// splitTokens splits on whitespace, respecting single/double quotes.
func splitTokens(s string) []string {
	var toks []string
	var cur strings.Builder
	var quote byte
	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			cur.WriteByte(c)
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
			cur.WriteByte(c)
		case c == ' ' || c == '\t':
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return toks
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// expand resolves $HOME/${HOME} and a leading ~ — the only expansions
// real-world ork.conf files use.
func expand(s string) string {
	home, _ := os.UserHomeDir()
	s = strings.ReplaceAll(s, "${HOME}", home)
	s = strings.ReplaceAll(s, "$HOME", home)
	if s == "~" || strings.HasPrefix(s, "~/") {
		s = home + s[1:]
	}
	return s
}
