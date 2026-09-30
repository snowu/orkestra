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
	"io"
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

	// FE/BE pairing: separate sibling repos (not subdirs) that share task
	// names — e.g. ORK_FE_REPO=cr-frontend, ORK_BE_REPO=cr-managament. From
	// any row, the paired worktree is <root>/<FERepo|BERepo>/<sameTask>.
	// These legacy single-pair keys still work; they become Pairs[0].
	FERepo, BERepo string
	// FECmd/BECmd run in each sibling worktree. BECmd may contain a {port}
	// placeholder — ork substitutes a port derived from the task name so
	// concurrent tasks' backends don't collide. FEEnvVar, if set, is the
	// .env.local key ork rewrites in the fe worktree to point at that same
	// port before running FECmd (e.g. NEXT_PUBLIC_CREDIT_RISK_SERVICE_ENDPOINT).
	FECmd, BECmd string
	FEEnvVar     string

	// PairsConfig is the JSON file declaring additional FE/BE pairs (same
	// no-code-execution rationale as HooksConfig). Pairs is the merged
	// result: the legacy ORK_FE_REPO/ORK_BE_REPO pair first (if set),
	// then every pair from PairsConfig (repositories must be unique).
	PairsConfig string
	Pairs       []Pair
}

// Pair is one FE/BE sibling-repo pairing. JSON tags match the keys in
// pairs.json. FEEnvPath, if set, is appended after the port in the URL
// written to the fe env var (e.g. "/public/operations" →
// http://localhost:8123/public/operations).
type Pair struct {
	FERepo    string `json:"fe"`
	BERepo    string `json:"be"`
	FECmd     string `json:"fe_cmd"`
	BECmd     string `json:"be_cmd"`
	FEEnvVar  string `json:"fe_env_var"`
	FEEnvPath string `json:"fe_env_path"`
	// FEURLEnvVars are .env.local keys rewritten to the task's OWN fe url
	// (http://localhost:<fePort>) — for apps that hardcode their public
	// origin (e.g. NEXTAUTH_URL=http://localhost:3000) and would otherwise
	// redirect auth flows to whatever task hashes to port 3000.
	FEURLEnvVars []string `json:"fe_url_env_vars"`
	// FEEnvFile selects the frontend dotenv file; default .env.local.
	FEEnvFile string `json:"fe_env_file"`
	// TaskEnvVar defaults to NEXT_PUBLIC_ORK_TASK; an empty string disables it.
	TaskEnvVar *string `json:"task_env_var,omitempty"`
}

// PairFor returns the pair repo belongs to (either side), or false.
func (c Config) PairFor(repo string) (Pair, bool) {
	for _, p := range c.Pairs {
		if repo == p.FERepo || repo == p.BERepo {
			return p, true
		}
	}
	return Pair{}, false
}

func defaults() Config {
	home, _ := os.UserHomeDir()
	return Config{
		WorktreeRoots: []string{filepath.Join(home, "worktrees")},
		ScanMaxDepth:  3,
		HooksConfig:   filepath.Join(home, ".config/ork/hooks.json"),
		Multiplexer:   "tmux",
		PairsConfig:   filepath.Join(home, ".config/ork/pairs.json"),
		FECmd:         "rund",
		BECmd:         "bund", // override with a {port}-templated command to avoid port collisions across tasks
	}
}

// Load reads path if it exists; a missing file just yields defaults.
func Load(path string) (Config, error) {
	cfg := defaults()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			if err := cfg.Validate(); err != nil {
				return cfg, err
			}
			cfg.Pairs, err = mergePairs(cfg)
			return cfg, err
		}
		return cfg, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(stripComment(sc.Text()))
		line = strings.TrimPrefix(line, "export ")
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
			cfg.WorktreeRoots = parseArray(val)
		case "ORK_FAVORITES":
			cfg.Favorites = parseArray(val)
		case "CLAUDE_PERSONAL_DIRS":
			cfg.ClaudePersonalDirs = parseArray(val)
		case "ORK_SCAN_MAXDEPTH":
			n, err := strconv.Atoi(unquote(val))
			if err != nil || n <= 0 {
				return cfg, fmt.Errorf("ORK_SCAN_MAXDEPTH must be a positive integer")
			}
			cfg.ScanMaxDepth = n
		case "ORK_SCOPE_SESSIONS_TO_REPO":
			v := unquote(val)
			if v != "0" && v != "1" {
				return cfg, fmt.Errorf("ORK_SCOPE_SESSIONS_TO_REPO must be 0 or 1")
			}
			cfg.ScopeSessionsToRepo = v == "1"
		case "ORK_MULTIPLEXER":
			if v := unquote(val); v != "" {
				cfg.Multiplexer = v
			}
		case "ORK_HOOKS_CONFIG":
			if v := expand(unquote(val)); v != "" {
				cfg.HooksConfig = v
			}
		case "ORK_FE_REPO":
			cfg.FERepo = unquote(val)
		case "ORK_BE_REPO":
			cfg.BERepo = unquote(val)
		case "ORK_FE_CMD":
			if v := unquote(val); v != "" {
				cfg.FECmd = v
			}
		case "ORK_BE_CMD":
			if v := unquote(val); v != "" {
				cfg.BECmd = v
			}
		case "ORK_FE_ENV_VAR":
			cfg.FEEnvVar = unquote(val)
		case "ORK_PAIRS_CONFIG":
			if v := expand(unquote(val)); v != "" {
				cfg.PairsConfig = v
			}
		}
	}
	if err := sc.Err(); err != nil {
		return cfg, err
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	cfg.Pairs, err = mergePairs(cfg)
	return cfg, err
}

// mergePairs preserves legacy priority while rejecting ambiguous definitions.
// A missing file is optional; malformed or unreadable configuration is an error.
func mergePairs(cfg Config) ([]Pair, error) {
	var pairs []Pair
	if cfg.FERepo != "" || cfg.BERepo != "" {
		pairs = append(pairs, Pair{FERepo: cfg.FERepo, BERepo: cfg.BERepo,
			FECmd: cfg.FECmd, BECmd: cfg.BECmd, FEEnvVar: cfg.FEEnvVar})
	}
	f, err := os.Open(cfg.PairsConfig)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("pairs %s: %w", cfg.PairsConfig, err)
	}
	if err == nil {
		defer f.Close()
		var fromFile []Pair
		dec := json.NewDecoder(f)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&fromFile); err != nil {
			return nil, fmt.Errorf("pairs %s: %w", cfg.PairsConfig, err)
		}
		if fromFile == nil {
			return nil, fmt.Errorf("pairs %s: expected a JSON array, use [] for no pairs", cfg.PairsConfig)
		}
		var extra any
		if err := dec.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("pairs %s: expected one JSON array", cfg.PairsConfig)
		}
		pairs = append(pairs, fromFile...)
	}
	seen := map[string]int{}
	for i := range pairs {
		p := &pairs[i]
		if err := p.Validate(); err != nil {
			return nil, fmt.Errorf("pair %d (%s + %s): %w", i+1, p.FERepo, p.BERepo, err)
		}
		for _, repo := range []string{p.FERepo, p.BERepo} {
			if prev, ok := seen[repo]; ok {
				return nil, fmt.Errorf("pair %d: repo %q already belongs to pair %d", i+1, repo, prev)
			}
			seen[repo] = i + 1
		}
		if p.FECmd == "" {
			p.FECmd = cfg.FECmd
		}
		if p.BECmd == "" {
			p.BECmd = cfg.BECmd
		}
	}
	return pairs, nil
}

// Validate checks configuration without requiring installed tools or existing roots.
func (c Config) Validate() error {
	if c.Multiplexer != "tmux" && c.Multiplexer != "herdr" {
		return fmt.Errorf("ORK_MULTIPLEXER must be tmux or herdr")
	}
	if len(c.WorktreeRoots) == 0 {
		return fmt.Errorf("ORK_WORKTREES_ROOTS must contain at least one root")
	}
	return nil
}

var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Validate checks values used as paths and dotenv assignments before any writes.
func (p Pair) Validate() error {
	for _, repo := range []string{p.FERepo, p.BERepo} {
		if repo == "" || repo == "." || repo == ".." || strings.ContainsAny(repo, `/\\`) {
			return fmt.Errorf("fe and be must be repo folder names")
		}
	}
	if p.FERepo == p.BERepo {
		return fmt.Errorf("fe and be must be different repos")
	}
	if f := p.FEEnvFile; f != "" && (!filepath.IsLocal(f) || strings.ContainsAny(f, "\r\n")) {
		return fmt.Errorf("fe_env_file must be a relative path inside the worktree")
	}
	keys := append([]string{p.FEEnvVar, p.TaskKey()}, p.FEURLEnvVars...)
	seen := map[string]bool{}
	for _, key := range keys {
		if key == "" {
			continue
		}
		if !envKey.MatchString(key) {
			return fmt.Errorf("invalid environment key %q", key)
		}
		if seen[key] {
			return fmt.Errorf("duplicate environment key %q", key)
		}
		seen[key] = true
	}
	if strings.ContainsAny(p.FEEnvPath, "\r\n\"'#$`\\") || (p.FEEnvPath != "" && !strings.HasPrefix(p.FEEnvPath, "/")) {
		return fmt.Errorf("fe_env_path must be a dotenv-safe URL path starting with /")
	}
	return nil
}

func (p Pair) EnvFile() string {
	if p.FEEnvFile != "" {
		return p.FEEnvFile
	}
	return ".env.local"
}

func (p Pair) TaskKey() string {
	if p.TaskEnvVar != nil {
		return *p.TaskEnvVar
	}
	return "NEXT_PUBLIC_ORK_TASK"
}

// stripComment respects quoted command strings and bash's whitespace before #.
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
