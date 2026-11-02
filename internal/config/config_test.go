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

func TestGroupsForRepo(t *testing.T) {
	cfg := Config{Groups: []Group{
		{Name: "a", Processes: []Process{{Label: "x", Repo: "shared"}}},
		{Name: "b", Processes: []Process{{Label: "y", Repo: "shared"}, {Label: "z", Repo: "other"}}},
		{Name: "c", Processes: []Process{{Label: "w", Repo: "solo-only"}}},
	}}

	gs := cfg.GroupsForRepo("shared")
	if len(gs) != 2 || gs[0].Name != "a" || gs[1].Name != "b" {
		t.Errorf("shared should resolve to [a b] in file order, got %+v", gs)
	}

	gs = cfg.GroupsForRepo("solo-only")
	if len(gs) != 1 || gs[0].Name != "c" {
		t.Errorf("solo-only should resolve to [c], got %+v", gs)
	}

	gs = cfg.GroupsForRepo("nowhere")
	if len(gs) != 0 {
		t.Errorf("unknown repo should resolve to no groups, got %+v", gs)
	}
}

func TestGroupValidate(t *testing.T) {
	ok := Group{Name: "g", Processes: []Process{
		{Label: "a", Repo: "r1", Cmd: "x"},
		{Label: "b", Repo: "r2", Cmd: "y"},
	}}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid group rejected: %v", err)
	}
	dup := Group{Name: "g", Processes: []Process{
		{Label: "a", Repo: "r1", Cmd: "x"},
		{Label: "a", Repo: "r2", Cmd: "y"},
	}}
	if err := dup.Validate(); err == nil {
		t.Error("duplicate labels must be rejected — they name windows")
	}
	empty := Group{Name: "g"}
	if err := empty.Validate(); err == nil {
		t.Error("a group with no processes must be rejected")
	}
	noLabel := Group{Name: "g", Processes: []Process{{Repo: "r1", Cmd: "x"}}}
	if err := noLabel.Validate(); err == nil {
		t.Error("a process with no label must be rejected")
	}
}

func TestLoadGroupsAndResolution(t *testing.T) {
	dir := t.TempDir()
	groupsPath := filepath.Join(dir, "groups.json")
	os.WriteFile(groupsPath, []byte(`[
	  {"name":"mfe","processes":[
	    {"label":"remote","repo":"web-remote","cmd":"pnpm dev","fixed_port":4023},
	    {"label":"host","repo":"web-host","cmd":"./go app.dev","fixed_port":4000},
	    {"label":"be","repo":"shared-be","cmd":"uv run x --port {port}","port_range":"be"}
	  ]},
	  {"name":"be-solo","processes":[
	    {"label":"be","repo":"shared-be","cmd":"uv run x --port {port}","port_range":"be"}
	  ]}
	]`), 0o644)

	cfg := Config{GroupsConfig: groupsPath}
	cfg.loadGroups()

	if len(cfg.Groups) != 2 || len(cfg.Groups[0].Processes) != 3 {
		t.Fatalf("groups = %+v", cfg.Groups)
	}

	// A repo only in one group resolves to that single group.
	gs := cfg.GroupsForRepo("web-remote")
	if len(gs) != 1 || gs[0].Name != "mfe" {
		t.Errorf("web-remote resolved to %+v", gs)
	}

	// A repo in multiple groups: GroupsForRepo returns ALL of them, in
	// file order — the flaw this replaces was picking just the first.
	gs = cfg.GroupsForRepo("shared-be")
	if len(gs) != 2 || gs[0].Name != "mfe" || gs[1].Name != "be-solo" {
		t.Errorf("shared-be should resolve to both groups in file order, got %+v", gs)
	}

	// Unknown repo resolves to nothing.
	if gs := cfg.GroupsForRepo("nope"); len(gs) != 0 {
		t.Errorf("unknown repo should resolve to no groups, got %+v", gs)
	}
}

func TestLoadGroupsMissingFileIsSilent(t *testing.T) {
	cfg := Config{GroupsConfig: filepath.Join(t.TempDir(), "absent.json")}
	cfg.loadGroups()
	if len(cfg.Groups) != 0 {
		t.Errorf("missing file should yield no groups, got %+v", cfg.Groups)
	}
}

func TestLoadGroupsRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "groups.json")
	// Duplicate labels — must be dropped, not loaded half-broken.
	os.WriteFile(p, []byte(`[{"name":"bad","processes":[
	  {"label":"x","repo":"r1","cmd":"a"},{"label":"x","repo":"r2","cmd":"b"}]}]`), 0o644)
	cfg := Config{GroupsConfig: p}
	cfg.loadGroups()
	if len(cfg.Groups) != 0 {
		t.Errorf("invalid group must be dropped, got %+v", cfg.Groups)
	}
}

func TestProcessValidationErrors(t *testing.T) {
	for _, proc := range []string{
		`{"label":"a","repo":"../a","cmd":"x"}`,
		`{"label":"a","repo":"","cmd":"x"}`,
		`{"label":"a","repo":"r","cmd":"x","port_range":"api"}`,
		`{"label":"a","repo":"r","cmd":"x --port {port}"}`,
		`{"label":"a","repo":"r","cmd":"x","env_file":"../.env"}`,
		`{"label":"a","repo":"r","cmd":"x","env_var":"BAD-KEY"}`,
		`{"label":"a","repo":"r","cmd":"x","port_range":"fe","env_var":"NEXT_PUBLIC_ORK_TASK"}`,
		`{"label":"a","repo":"r","cmd":"x","env_var":"A","env_path":"/x\nINJECT=1"}`,
		`{"label":"a","repo":"r","cmd":"x","patch_file":"config.pkl"}`,
		`{"label":"a","repo":"r","cmd":"x","patch_file":"../config.pkl","patch_key":"backend"}`,
		`{"label":"a","repo":"r","cmd":"x","patch_file":".env.local","patch_key":"backend"}`,
	} {
		t.Run(proc, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "groups.json")
			os.WriteFile(path, []byte(`[{"name":"g","processes":[`+proc+`]}]`), 0o600)
			cfg, err := Load(write(t, `ORK_GROUPS_CONFIG="`+path+`"`))
			if err != nil {
				t.Fatal(err) // invalid groups are problems, not load errors
			}
			if len(cfg.Groups) != 0 || len(cfg.Problems) == 0 {
				t.Fatalf("invalid process accepted: groups=%+v problems=%v", cfg.Groups, cfg.Problems)
			}
		})
	}
}

func TestGroupsFileProblems(t *testing.T) {
	for name, content := range map[string]string{
		"bad json":       `[`,
		"unknown field":  `[{"name":"g","processes":[{"label":"a","repo":"r","cmd":"x","port-range":"fe"}]}]`,
		"duplicate name": `[{"name":"g","processes":[{"label":"a","repo":"r","cmd":"x"}]},{"name":"g","processes":[{"label":"a","repo":"s","cmd":"x"}]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "groups.json")
			os.WriteFile(path, []byte(content), 0o600)
			cfg, err := Load(write(t, `ORK_GROUPS_CONFIG="`+path+`"`))
			if err != nil || len(cfg.Problems) == 0 {
				t.Fatalf("err=%v problems=%v", err, cfg.Problems)
			}
		})
	}
}

func TestTaskKeyDefaults(t *testing.T) {
	empty := ""
	for _, c := range []struct {
		p    Process
		want string
	}{
		{Process{PortRange: "fe"}, "NEXT_PUBLIC_ORK_TASK"},
		{Process{EnvVar: "API"}, "NEXT_PUBLIC_ORK_TASK"},
		{Process{PortRange: "be"}, ""},
		{Process{PortRange: "fe", TaskEnvVar: &empty}, ""},
	} {
		if got := c.p.TaskKey(); got != c.want {
			t.Errorf("%+v: TaskKey = %q, want %q", c.p, got, c.want)
		}
	}
}

func TestDefaultGroupsWithoutShellConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config/ork")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "groups.json"), []byte(`[{"name":"g","processes":[
	  {"label":"fe","repo":"web","cmd":"x","port_range":"fe","task_env_var":"","env_file":".env"}]}]`), 0o600)
	cfg, err := Load(filepath.Join(home, ".ork.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Groups) != 1 {
		t.Fatalf("groups = %+v problems = %v", cfg.Groups, cfg.Problems)
	}
	p := cfg.Groups[0].Processes[0]
	if p.TaskKey() != "" || p.EnvFileOrDefault() != ".env" {
		t.Fatalf("process = %+v", p)
	}
}

func TestExportAndComments(t *testing.T) {
	cfg, err := Load(write(t, `export ORK_FAVORITES=("a b" c) # favs
ORK_HOOKS_CONFIG="/tmp/x #1.json" # trailing comment
ORK_SCOPE_SESSIONS_TO_REPO=true # shared config`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Favorites) != 2 || cfg.Favorites[0] != "a b" || cfg.HooksConfig != "/tmp/x #1.json" || !cfg.ScopeSessionsToRepo {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestBadValuesKeepDefaults(t *testing.T) {
	for _, line := range []string{"ORK_WORKTREES_ROOTS=()", "ORK_SCAN_MAXDEPTH=oops", "ORK_SCOPE_SESSIONS_TO_REPO=maybe", "ORK_FE_REPO=legacy"} {
		cfg, err := Load(write(t, line))
		if err != nil {
			t.Errorf("%s: must not be fatal: %v", line, err)
			continue
		}
		if len(cfg.Problems) == 0 {
			t.Errorf("%s: expected a problem to be reported", line)
		}
		if len(cfg.WorktreeRoots) == 0 || cfg.ScanMaxDepth != 3 || cfg.ScopeSessionsToRepo {
			t.Errorf("%s: defaults not kept: %+v", line, cfg)
		}
	}
	if _, err := Load(write(t, "ORK_MULTIPLEXER=screen")); err == nil {
		t.Error("unknown multiplexer must be fatal")
	}
}
