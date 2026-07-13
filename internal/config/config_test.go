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
	cfg, err := Load("testdata/ork.conf.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.WorktreeRoots) == 0 {
		t.Error("example conf should yield roots")
	}
}
