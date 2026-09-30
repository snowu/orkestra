package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIProcess(t *testing.T) {
	if os.Getenv("ORK_CLI_TEST") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"ork"}, os.Args[i+1:]...)
			main()
			os.Exit(0)
		}
	}
	os.Exit(2)
}

func TestPairCLI(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "worktrees")
	web, api := filepath.Join(home, "web"), filepath.Join(home, "api")
	git := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.test", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.test")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	for _, repo := range []string{web, api} {
		os.MkdirAll(repo, 0o755)
		git(repo, "init", "-b", "main")
		git(repo, "commit", "--allow-empty", "-m", "init")
		git(repo, "worktree", "add", filepath.Join(root, filepath.Base(repo), "feature"), "-b", "feature")
	}
	pairs := filepath.Join(home, "pairs.json")
	os.WriteFile(pairs, []byte(`[{"fe":"web","be":"api","fe_cmd":"web {port} {be_port}","be_cmd":"api {port} {fe_port}","fe_env_file":".env","task_env_var":"VITE_TASK"}]`), 0o600)
	conf := filepath.Join(home, "ork.conf")
	os.WriteFile(conf, []byte(`ORK_WORKTREES_ROOTS=("`+root+`")
ORK_PAIRS_CONFIG="`+pairs+`"`), 0o600)
	run := func(dir string, args ...string) (string, error) {
		t.Helper()
		c := exec.Command(os.Args[0], append([]string{"-test.run=^TestCLIProcess$", "--"}, args...)...)
		c.Dir = dir
		c.Env = append(os.Environ(), "ORK_CLI_TEST=1", "ORK_CONFIG="+conf, "HOME="+home)
		var out, stderr bytes.Buffer
		c.Stdout, c.Stderr = &out, &stderr
		err := c.Run()
		if out.Len() != 0 {
			t.Fatalf("CLI broke cd contract: stdout=%q", out.String())
		}
		return stderr.String(), err
	}
	if out, err := run(web, "config", "check"); err != nil || !strings.Contains(out, "web + api") {
		t.Fatalf("config check: %v %s", err, out)
	}
	var expected string
	for _, dir := range []string{web, api, filepath.Join(root, "web/feature"), filepath.Join(root, "api/feature/subdir")} {
		os.MkdirAll(dir, 0o755)
		args := []string{"pair", "--dry-run"}
		if dir == web || dir == api {
			args = append(args, "feature")
		}
		out, err := run(dir, args...)
		if err != nil {
			t.Fatalf("dry run: %v %s", err, out)
		}
		if expected == "" {
			expected = out
		} else if out != expected {
			t.Fatalf("different plans: %s vs %s", out, expected)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "web/feature/.env")); !os.IsNotExist(err) {
		t.Fatal("dry run wrote environment")
	}
	if out, err := run(web, "pair"); err == nil || !strings.Contains(out, "supply a task") {
		t.Fatalf("missing task: %v %s", err, out)
	}
	os.WriteFile(pairs, []byte(`[{"fe":"web"}]`), 0o600)
	if out, err := run(web, "config", "check"); err == nil || !strings.Contains(out, "pair 1") {
		t.Fatalf("invalid config: %v %s", err, out)
	}
}
