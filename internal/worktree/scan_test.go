package worktree

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func mk(t *testing.T, paths ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range paths {
		if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestAllWorktreeDirs(t *testing.T) {
	root := mk(t, "repoA/task1", "repoA/task2", "repoB/task1")
	// stray file at repo level must be skipped
	os.WriteFile(filepath.Join(root, "repoA/notadir"), []byte("x"), 0o644)
	got := AllWorktreeDirs([]string{root, filepath.Join(root, "missing-root")})
	if len(got) != 3 {
		t.Fatalf("got %v", got)
	}
}

func TestAllRepoDirsFindsGitDirsOnly(t *testing.T) {
	home := mk(t,
		"proj1/.git",
		"group/proj2/.git",
		"node_modules/dep/.git", // pruned
		"toodeep/a/b/proj/.git", // beyond maxdepth 3
	)
	// worktree-style .git FILE — must be excluded
	os.MkdirAll(filepath.Join(home, "wt1"), 0o755)
	os.WriteFile(filepath.Join(home, "wt1/.git"), []byte("gitdir: elsewhere"), 0o644)

	cache := filepath.Join(t.TempDir(), "cache")
	got := AllRepoDirs(home, 3, cache, time.Minute)
	want := map[string]bool{
		filepath.Join(home, "proj1"):       true,
		filepath.Join(home, "group/proj2"): true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected %s", g)
		}
	}
}

func TestAllRepoDirsUsesCache(t *testing.T) {
	home := mk(t, "proj1/.git")
	cache := filepath.Join(t.TempDir(), "cache")
	AllRepoDirs(home, 3, cache, time.Minute)
	// new repo appears but cache is fresh — must NOT be seen
	os.MkdirAll(filepath.Join(home, "proj2/.git"), 0o755)
	got := AllRepoDirs(home, 3, cache, time.Minute)
	if len(got) != 1 {
		t.Fatalf("cache ignored: %v", got)
	}
	// expired cache rescans
	got = AllRepoDirs(home, 3, cache, 0)
	if len(got) != 2 {
		t.Fatalf("rescan failed: %v", got)
	}
}

func TestFindRepoRootAndWorktree(t *testing.T) {
	if FindRepoRoot([]string{"/a/foo", "/b/bar"}, "bar") != "/b/bar" {
		t.Error("FindRepoRoot by basename failed")
	}
	if FindRepoRoot([]string{"/a/foo"}, "nope") != "" {
		t.Error("missing repo should be empty")
	}
	root := mk(t, "repoA/task1")
	if FindWorktree([]string{root}, "repoA", "task1") != filepath.Join(root, "repoA/task1") {
		t.Error("FindWorktree failed")
	}
	if FindWorktree([]string{root}, "repoA", "none") != "" {
		t.Error("missing worktree should be empty")
	}
}
