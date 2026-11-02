// Package config parses ~/.ork.conf. The file is bash syntax (it's still
// sourced by the shell shims), but ork only ever needs the handful of
// KEY=value / KEY=(a b c) assignments below (optionally `export`ed, with
// trailing comments) — this is a line parser for exactly those forms, not a
// bash evaluator. Unknown keys are ignored so users can keep arbitrary
// shell in the file for their own wrappers.
package config

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

	// Problems are non-fatal configuration issues (an invalid group that
	// was dropped, an unparseable value that kept its default, legacy pair
	// keys). Startup reports them on stderr and carries on — a config
	// mistake must not lock the user out of their worktrees — while
	// `ork config check` lists them and fails.
	Problems []string
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
	// PatchFile/PatchKey: for apps whose backend url lives in some other
	// text file entirely (e.g. a Pkl config template rendered into
	// public/config.json on every dev-server start — patching the
	// generated file gets clobbered immediately). PatchFile is relative to
	// this process's worktree; ork rewrites lines matching
	// `<key> = "http://localhost:<anything>"` (or `<key>: "..."`) to
	// http://localhost:<bePort>, key/value quoting preserved.
	PatchFile string `json:"patch_file"`
	PatchKey  string `json:"patch_key"`
	// EnvFile is the dotenv file env_var/url_env_vars/the task label are
	// written to, relative to the worktree; default .env.local.
	EnvFile string `json:"env_file"`
	// TaskEnvVar names the key that receives the task name (an app can
	// show it, e.g. in its tab title). Defaults to NEXT_PUBLIC_ORK_TASK;
	// "" disables it.
	TaskEnvVar *string `json:"task_env_var,omitempty"`
}

// EnvFileOrDefault is EnvFile, defaulting to .env.local.
func (p Process) EnvFileOrDefault() string {
	if p.EnvFile != "" {
		return p.EnvFile
	}
	return ".env.local"
}

// TaskKey is the dotenv key that receives the task name, "" for none. The
// default only applies to processes that already manage a dotenv file
// (fe port range, env_var or url_env_vars) — writing a label into a
// backend or non-JS worktree just leaves a stray untracked file.
func (p Process) TaskKey() string {
	if p.TaskEnvVar != nil {
		return *p.TaskEnvVar
	}
	if p.PortRange == "fe" || p.EnvVar != "" || len(p.URLEnvVars) > 0 {
		return "NEXT_PUBLIC_ORK_TASK"
	}
	return ""
}

var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// localPath: relative, inside the worktree, single line.
func localPath(f string) bool {
	return filepath.IsLocal(f) && !strings.ContainsAny(f, "\r\n")
}

// Validate checks values later used as paths, dotenv assignments and
// window names, before anything is written.
func (p Process) Validate() error {
	if p.Label == "" {
		return fmt.Errorf("a process has no label")
	}
	if r := p.Repo; r == "" || r == "." || r == ".." || strings.ContainsAny(r, `/\`) {
		return fmt.Errorf("%s: repo must be a repo folder name", p.Label)
	}
	switch p.PortRange {
	case "", "fe", "be":
	default:
		return fmt.Errorf("%s: port_range must be \"fe\", \"be\" or omitted", p.Label)
	}
	if p.PortRange == "" && strings.Contains(p.Cmd, "{port}") {
		return fmt.Errorf("%s: cmd uses {port} but no port_range is set", p.Label)
	}
	if (p.PatchFile == "") != (p.PatchKey == "") {
		return fmt.Errorf("%s: patch_file and patch_key must be set together", p.Label)
	}
	if p.PatchFile != "" && !localPath(p.PatchFile) {
		return fmt.Errorf("%s: patch_file must be a relative path inside the worktree", p.Label)
	}
	if p.EnvFile != "" && !localPath(p.EnvFile) {
		return fmt.Errorf("%s: env_file must be a relative path inside the worktree", p.Label)
	}
	if p.PatchFile != "" && filepath.Clean(p.PatchFile) == filepath.Clean(p.EnvFileOrDefault()) {
		return fmt.Errorf("%s: patch_file and env_file must be different files", p.Label)
	}
	seen := map[string]bool{}
	for _, key := range append([]string{p.EnvVar, p.TaskKey()}, p.URLEnvVars...) {
		if key == "" {
			continue
		}
		if !envKey.MatchString(key) {
			return fmt.Errorf("%s: invalid environment key %q", p.Label, key)
		}
		if seen[key] {
			return fmt.Errorf("%s: duplicate environment key %q", p.Label, key)
		}
		seen[key] = true
	}
	if strings.ContainsAny(p.EnvPath, "\r\n\"'#$`\\") || (p.EnvPath != "" && !strings.HasPrefix(p.EnvPath, "/")) {
		return fmt.Errorf("%s: env_path must be a URL path starting with /", p.Label)
	}
	return nil
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
		if err := p.Validate(); err != nil {
			return fmt.Errorf("group %q: %w", g.Name, err)
		}
		if seen[p.Label] {
			return fmt.Errorf("group %q has duplicate label %q", g.Name, p.Label)
		}
		seen[p.Label] = true
	}
	return nil
}

// loadGroups reads GroupsConfig. Invalid groups are dropped and recorded in
// Problems rather than failing startup — a broken groups file must not stop
// the user reaching their worktrees. A missing file is fine (groups are
// optional); an unreadable one is a problem.
func (c *Config) loadGroups() {
	data, err := os.ReadFile(c.GroupsConfig)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		c.problem("%s: %v — ignoring", c.GroupsConfig, err)
		return
	}
	var fromFile []Group
	if err := json.Unmarshal(data, &fromFile); err != nil {
		c.problem("%s: %v — ignoring", c.GroupsConfig, err)
		return
	}
	// Unknown keys are almost always typos ("port-range", "env_vars") whose
	// setting then silently does nothing — say so, but still load.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&[]Group{}); err != nil {
		c.problem("%s: %v", c.GroupsConfig, err)
	}
	names := map[string]bool{}
	for _, g := range fromFile {
		if err := g.Validate(); err != nil {
			c.problem("%v — group ignored", err)
			continue
		}
		if names[g.Name] {
			c.problem("duplicate group name %q — later one ignored", g.Name)
			continue
		}
		names[g.Name] = true
		c.Groups = append(c.Groups, g)
	}
}

func (c *Config) problem(format string, args ...any) {
	c.Problems = append(c.Problems, fmt.Sprintf(format, args...))
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

// GroupByName returns the group called name, or false (always false for "").
func (c Config) GroupByName(name string) (Group, bool) {
	for _, g := range c.Groups {
		if name != "" && g.Name == name {
			return g, true
		}
	}
	return Group{}, false
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

// Load reads path if it exists; a missing file just yields defaults. The
// only hard error is one ork cannot run with at all (unreadable file,
// unknown multiplexer); everything else lands in Problems.
func Load(path string) (Config, error) {
	cfg := defaults()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// groups.json is its own file — no ~/.ork.conf must not hide it.
			cfg.loadGroups()
			cfg.warnLegacyPairs(nil)
			return cfg, nil
		}
		return cfg, err
	}
	defer f.Close()

	var legacyPairKeys []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(stripComment(sc.Text()))
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
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
			} else {
				cfg.problem("ORK_WORKTREES_ROOTS is empty — using %s", cfg.WorktreeRoots[0])
			}
		case "ORK_FAVORITES":
			cfg.Favorites = parseArray(val)
		case "CLAUDE_PERSONAL_DIRS":
			cfg.ClaudePersonalDirs = parseArray(val)
		case "ORK_SCAN_MAXDEPTH":
			if n, err := strconv.Atoi(unquote(val)); err == nil && n > 0 {
				cfg.ScanMaxDepth = n
			} else {
				cfg.problem("ORK_SCAN_MAXDEPTH=%s is not a positive integer — using %d", val, cfg.ScanMaxDepth)
			}
		case "ORK_SCOPE_SESSIONS_TO_REPO":
			switch strings.ToLower(unquote(val)) {
			case "1", "true", "yes", "on":
				cfg.ScopeSessionsToRepo = true
			case "", "0", "false", "no", "off":
				cfg.ScopeSessionsToRepo = false
			default:
				cfg.problem("ORK_SCOPE_SESSIONS_TO_REPO=%s is not 0 or 1 — leaving it off", val)
			}
		case "ORK_MULTIPLEXER":
			if v := unquote(val); v != "" {
				cfg.Multiplexer = v
			}
		case "ORK_HOOKS_CONFIG":
			if v := expand(unquote(val)); v != "" {
				cfg.HooksConfig = v
			}
		case "ORK_FE_REPO", "ORK_BE_REPO", "ORK_FE_CMD", "ORK_BE_CMD", "ORK_FE_ENV_VAR", "ORK_PAIRS_CONFIG":
			legacyPairKeys = append(legacyPairKeys, key)
		case "ORK_GROUPS_CONFIG":
			if v := expand(unquote(val)); v != "" {
				cfg.GroupsConfig = v
			}
		}
	}
	if err := sc.Err(); err != nil {
		return cfg, err
	}
	if cfg.Multiplexer != "tmux" && cfg.Multiplexer != "herdr" {
		return cfg, fmt.Errorf("ORK_MULTIPLEXER must be tmux or herdr, got %q", cfg.Multiplexer)
	}
	cfg.loadGroups()
	cfg.warnLegacyPairs(legacyPairKeys)
	return cfg, nil
}

// stripComment drops a trailing bash comment: a # at line start or after
// whitespace, outside quotes ("echo '# keep'" stays intact).
func stripComment(s string) string {
	var quote byte
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && quote != '\'' {
			i++
			continue
		}
		if quote != 0 {
			if s[i] == quote {
				quote = 0
			}
			continue
		}
		if s[i] == '\'' || s[i] == '"' {
			quote = s[i]
			continue
		}
		if s[i] == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t') {
			return s[:i]
		}
	}
	return s
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

// warnLegacyPairs tells users upgrading from the fe/be-pair config that
// it is no longer read — otherwise their pairs just silently vanish.
func (c *Config) warnLegacyPairs(keys []string) {
	if len(keys) > 0 {
		c.problem("%s no longer supported — move the pair to %s (see ork.conf.example)", strings.Join(keys, ", "), c.GroupsConfig)
	}
	pairs := filepath.Join(filepath.Dir(c.GroupsConfig), "pairs.json")
	if _, err := os.Stat(pairs); err == nil {
		c.problem("%s is no longer read — move its pairs to %s", pairs, c.GroupsConfig)
	}
}
