package config

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ork.conf")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaultsOnMissingFile(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.conf"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	home, _ := os.UserHomeDir()
	if len(cfg.WorktreeRoots) != 1 || cfg.WorktreeRoots[0] != filepath.Join(home, "worktrees") {
		t.Errorf("default roots = %v", cfg.WorktreeRoots)
	}
	if cfg.ScanMaxDepth != 3 {
		t.Errorf("default depth = %d", cfg.ScanMaxDepth)
	}
	if cfg.HooksConfig != filepath.Join(home, ".config/ork/hooks.json") {
		t.Errorf("default hooks = %s", cfg.HooksConfig)
	}
	if cfg.ScopeSessionsToRepo {
		t.Error("scope should default false")
	}
}

func TestParseArraysAndExpansion(t *testing.T) {
	home, _ := os.UserHomeDir()
	p := write(t, `
# comment
ORK_WORKTREES_ROOTS=("$HOME/worktrees" '/srv/wt' ~/other)
ORK_FAVORITES=(my-backend my-frontend)
ORK_SCAN_MAXDEPTH=4
ORK_SCOPE_SESSIONS_TO_REPO=1
ORK_HOOKS_CONFIG="$HOME/custom/hooks.json"
CLAUDE_PERSONAL_DIRS=("$HOME/personal")
UNKNOWN_KEY=whatever
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	wantRoots := []string{filepath.Join(home, "worktrees"), "/srv/wt", filepath.Join(home, "other")}
	if len(cfg.WorktreeRoots) != 3 {
		t.Fatalf("roots = %v", cfg.WorktreeRoots)
	}
	for i, w := range wantRoots {
		if cfg.WorktreeRoots[i] != w {
			t.Errorf("root[%d] = %q, want %q", i, cfg.WorktreeRoots[i], w)
		}
	}
	if len(cfg.Favorites) != 2 || cfg.Favorites[0] != "my-backend" {
		t.Errorf("favorites = %v", cfg.Favorites)
	}
	if cfg.ScanMaxDepth != 4 {
		t.Errorf("depth = %d", cfg.ScanMaxDepth)
	}
	if !cfg.ScopeSessionsToRepo {
		t.Error("scope should be true")
	}
	if cfg.HooksConfig != filepath.Join(home, "custom/hooks.json") {
		t.Errorf("hooks = %s", cfg.HooksConfig)
	}
	if len(cfg.ClaudePersonalDirs) != 1 || cfg.ClaudePersonalDirs[0] != filepath.Join(home, "personal") {
		t.Errorf("personal dirs = %v", cfg.ClaudePersonalDirs)
	}
}

func TestExampleConfParses(t *testing.T) {
	// The shipped example must always parse.
	cfg, err := Load("../../ork.conf.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.WorktreeRoots) == 0 {
		t.Error("example conf should yield roots")
	}
}

func TestPairsMergeLegacyAndJSON(t *testing.T) {
	dir := t.TempDir()
	pairsPath := filepath.Join(dir, "pairs.json")
	os.WriteFile(pairsPath, []byte(`[
		{"fe": "lending-frontend", "be": "trade-finance-service",
		 "fe_cmd": "bun run dev -- --port {port}",
		 "be_cmd": "uv run fastapi dev src/app.py --port {port}",
		 "fe_env_var": "NEXT_PUBLIC_TRADE_FINANCE_SERVICE_ENDPOINT",
		 "fe_env_path": "/public/operations"}
	]`), 0o644)
	p := write(t, `
ORK_FE_REPO=cr-frontend
ORK_BE_REPO=cr-managament
ORK_FE_CMD="bun run dev -- --port {port}"
ORK_FE_ENV_VAR=NEXT_PUBLIC_CREDIT_RISK_SERVICE_ENDPOINT
ORK_PAIRS_CONFIG="`+pairsPath+`"
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Pairs) != 2 {
		t.Fatalf("pairs = %+v, want legacy + 1 json", cfg.Pairs)
	}
	if cfg.Pairs[0].FERepo != "cr-frontend" || cfg.Pairs[0].FEEnvVar != "NEXT_PUBLIC_CREDIT_RISK_SERVICE_ENDPOINT" {
		t.Errorf("legacy pair first, got %+v", cfg.Pairs[0])
	}
	if cfg.Pairs[0].BECmd != "bund" {
		t.Errorf("legacy pair should keep default BECmd, got %q", cfg.Pairs[0].BECmd)
	}
	lend := cfg.Pairs[1]
	if lend.BERepo != "trade-finance-service" || lend.FEEnvPath != "/public/operations" {
		t.Errorf("json pair = %+v", lend)
	}

	if p, ok := cfg.PairFor("trade-finance-service"); !ok || p.FERepo != "lending-frontend" {
		t.Errorf("PairFor(be side) = %+v, %v", p, ok)
	}
	if _, ok := cfg.PairFor("unrelated-repo"); ok {
		t.Error("PairFor should miss unrelated repo")
	}
}

func TestPairsJSONOnlyDefaultsCmds(t *testing.T) {
	dir := t.TempDir()
	pairsPath := filepath.Join(dir, "pairs.json")
	os.WriteFile(pairsPath, []byte(`[{"fe": "a-fe", "be": "a-be"}]`), 0o644)
	cfg, err := Load(write(t, `ORK_PAIRS_CONFIG="`+pairsPath+`"`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Pairs) != 1 || cfg.Pairs[0].FECmd != "rund" || cfg.Pairs[0].BECmd != "bund" {
		t.Fatalf("pairs = %+v", cfg.Pairs)
	}
}

func TestPairConfigurationErrors(t *testing.T) {
	for _, content := range []string{
		`[`, `null`, `[] []`, `[{"fe":"a","be":"b","fe_comand":"typo"}]`,
		`[{"fe":"a"}]`, `[{"fe":"a","be":"a"}]`,
		`[{"fe":"../a","be":"b"}]`,
		`[{"fe":"a","be":"b"},{"fe":"b","be":"c"}]`,
		`[{"fe":"a","be":"b","fe_env_file":"../.env"}]`,
		`[{"fe":"a","be":"b","fe_env_var":"BAD-KEY"}]`,
		`[{"fe":"a","be":"b","fe_env_var":"NEXT_PUBLIC_ORK_TASK"}]`,
		`[{"fe":"a","be":"b","fe_env_path":"/x\nINJECT=1"}]`,
	} {
		t.Run(content, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pairs.json")
			os.WriteFile(path, []byte(content), 0o600)
			_, err := Load(write(t, `ORK_PAIRS_CONFIG="`+path+`"`))
			if err == nil {
				t.Fatal("invalid pair accepted")
			}
		})
	}
}

func TestDefaultPairsWithoutShellConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config/ork")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "pairs.json"), []byte(`[{"fe":"web","be":"api","task_env_var":"","fe_env_file":".env"}]`), 0o600)
	cfg, err := Load(filepath.Join(home, ".ork.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Pairs) != 1 || cfg.Pairs[0].TaskKey() != "" || cfg.Pairs[0].EnvFile() != ".env" {
		t.Fatalf("pairs = %+v", cfg.Pairs)
	}
}

func TestExportCommentsAndCommandDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pairs.json")
	os.WriteFile(path, []byte(`[{"fe":"a","be":"b"}]`), 0o600)
	cfg, err := Load(write(t, `export ORK_PAIRS_CONFIG="`+path+`" # pairs
ORK_FE_CMD="echo '# keep' {port}" # trailing comment
ORK_BE_CMD='python app.py --port {port}'
ORK_SCOPE_SESSIONS_TO_REPO=1 # shared config`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Pairs[0].FECmd != "echo '# keep' {port}" || cfg.Pairs[0].BECmd != "python app.py --port {port}" || !cfg.ScopeSessionsToRepo {
		t.Fatalf("config = %+v", cfg)
	}
	for _, line := range []string{"ORK_WORKTREES_ROOTS=()", "ORK_SCAN_MAXDEPTH=oops", "ORK_SCOPE_SESSIONS_TO_REPO=true", "ORK_MULTIPLEXER=unknown", "ORK_FE_REPO=only-one"} {
		if _, err := Load(write(t, line)); err == nil {
			t.Errorf("accepted %s", line)
		}
	}
}
