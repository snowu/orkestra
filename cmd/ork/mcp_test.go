package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPWorkflow drives the server through an in-memory client the way an
// agent would: configure a group, create its worktrees, preview its start.
func TestMCPWorkflow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	conf := filepath.Join(home, ".ork.conf")
	os.WriteFile(conf, []byte(`ORK_WORKTREES_ROOTS=("$HOME/worktrees")`+"\n"), 0o644)
	t.Setenv("ORK_CONFIG", conf)
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(k, "test")
	}
	for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "test@example.test")
	}
	for _, repo := range []string{"web", "api"} {
		dir := filepath.Join(home, "code", repo)
		os.MkdirAll(dir, 0o755)
		for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "init"}, {"branch", "feat/login"}} {
			if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %s", args, out)
			}
		}
	}
	os.WriteFile(filepath.Join(home, "code", "web", ".env.local"), []byte("A=1\n"), 0o600)

	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := newMCPServer().Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	call := func(name string, args map[string]any, wantErr bool) string {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		text := res.Content[0].(*mcp.TextContent).Text
		if res.IsError != wantErr {
			t.Fatalf("%s: IsError=%v: %s", name, res.IsError, text)
		}
		return text
	}

	tools, err := cs.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) < 10 {
		t.Fatalf("tools: %v %v", tools, err)
	}

	call("set_group", map[string]any{"group": map[string]any{"name": "app", "processes": []any{
		map[string]any{"label": "fe", "repo": "web", "cmd": "dev --port {port}", "port_range": "fe", "env_var": "API_URL"},
		map[string]any{"label": "be", "repo": "api", "cmd": "serve --port {port}", "port_range": "be"},
	}}}, false)
	call("set_group", map[string]any{"group": map[string]any{"name": "bad", "processes": []any{
		map[string]any{"label": "x", "repo": "api", "cmd": "serve {port}"},
	}}}, true)
	if got := call("get_config", nil, false); !strings.Contains(got, `"name": "app"`) || strings.Contains(got, `"bad"`) {
		t.Fatalf("get_config: %s", got)
	}
	if got := call("list_repos", map[string]any{"query": "we"}, false); !strings.Contains(got, `"web"`) || strings.Contains(got, `"api"`) {
		t.Fatalf("list_repos: %s", got)
	}

	// An existing branch in a group: both members get a feat-login worktree.
	got := call("create_worktree", map[string]any{"group": "app", "branch": "feat/login"}, false)
	var created struct {
		Task      string
		Worktrees []struct{ Repo, Path string }
	}
	json.Unmarshal([]byte(got), &created)
	if created.Task != "feat-login" || len(created.Worktrees) != 2 {
		t.Fatalf("create_worktree: %s", got)
	}
	if data, _ := os.ReadFile(filepath.Join(home, "worktrees", "web", "feat-login", ".env.local")); string(data) != "A=1\n" {
		t.Fatal(".env.local not copied")
	}
	call("create_worktree", map[string]any{"repo": "api", "task": "solo"}, false)
	call("create_worktree", map[string]any{"repo": "api", "group": "app", "task": "x"}, true)

	if got := call("list_worktrees", map[string]any{"task": "feat-login"}, false); strings.Count(got, `"repo"`) != 2 {
		t.Fatalf("list_worktrees: %s", got)
	}
	if got := call("list_branches", map[string]any{"repo": "web"}, false); strings.Contains(got, "feat/login") {
		t.Fatalf("branch with a worktree still listed: %s", got)
	}

	got = call("plan_group", map[string]any{"task": "feat-login", "group": "app"}, false)
	if !strings.Contains(got, `"API_URL": "http://localhost:`) || !strings.Contains(got, "serve --port 8") {
		t.Fatalf("plan_group: %s", got)
	}
	call("plan_group", map[string]any{"task": "nope", "repo": "web"}, true)

	call("set_repo_hook", map[string]any{"repo": "web", "command": "true"}, false)
	call("delete_group", map[string]any{"name": "app"}, false)
	if got := call("get_config", nil, false); strings.Contains(got, `"app"`) || !strings.Contains(got, `"web": "true"`) {
		t.Fatalf("after edits: %s", got)
	}
}
