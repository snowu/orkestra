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
	root := mk(t, "repoA/task1", "repoA/task2", "repoB/task1", "reaver/src", "reaver/docs")
	for _, wt := range []string{"repoA/task1", "repoA/task2", "repoB/task1"} {
		os.WriteFile(filepath.Join(root, wt, ".git"), []byte("gitdir: elsewhere"), 0o644)
	}
	// a worktree sitting directly in the root (<root>/reaver): its plain
	// subdirs are not tasks
	os.WriteFile(filepath.Join(root, "reaver/.git"), []byte("gitdir: elsewhere"), 0o644)
	// stray file at repo level must be skipped
	os.WriteFile(filepath.Join(root, "repoA/notadir"), []byte("x"), 0o644)
	got := AllWorktreeDirs([]string{root, filepath.Join(root, "missing-root")})
	if len(got) != 3 {
		t.Fatalf("got %v", got)
	}
	for _, g := range got {
		if filepath.Base(filepath.Dir(g)) == "reaver" {
			t.Errorf("non-worktree subdir listed: %s", g)
		}
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

func TestBranchCandidates(t *testing.T) {
	repoRoot, run := gitRepo(t) // helper from ops_test.go, same package

	run("branch", "recent-one")
	run("branch", "recent-two")
	linked := filepath.Join(t.TempDir(), "linked")
	run("worktree", "add", linked, "-b", "has-worktree")
	// Primary checkout on a non-base branch; origin/HEAD names main as base.
	run("update-ref", "refs/remotes/origin/main", "refs/heads/main")
	run("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	run("checkout", "-b", "on-main")

	got := BranchCandidates(repoRoot, 0)
	names := map[string]BranchCand{}
	for _, c := range got {
		names[c.Name] = c
	}
	if _, ok := names["recent-one"]; !ok {
		t.Error("free branch missing from candidates")
	}
	if c, ok := names["recent-one"]; !ok || c.InMain {
		t.Error("free branch must have InMain == false")
	}
	if _, ok := names["has-worktree"]; ok {
		t.Error("branch held by a LINKED worktree must be excluded")
	}
	if c, ok := names["on-main"]; !ok {
		t.Error("branch held by the primary checkout must be a candidate")
	} else if !c.InMain {
		t.Error("branch held by the primary checkout must have InMain == true")
	}
	if c := names["recent-one"]; c.Repo != filepath.Base(repoRoot) {
		t.Errorf("Repo = %q, want %q", c.Repo, filepath.Base(repoRoot))
	}
	if c := names["recent-one"]; c.Tip.IsZero() {
		t.Error("Tip time not populated")
	}

	// newest first
	if len(got) >= 2 && got[0].Tip.Before(got[1].Tip) {
		t.Error("candidates must be sorted newest tip first")
	}

	// base branch held by the primary checkout: AddExisting always refuses
	// it, so it must not be offered
	run("checkout", "main")
	for _, c := range BranchCandidates(repoRoot, 0) {
		if c.Name == "main" {
			t.Error("base branch in the primary checkout must be excluded")
		}
	}

	// an age filter that excludes everything (all commits are "now")
	if c := BranchCandidates(repoRoot, -time.Hour); len(c) != 0 {
		t.Errorf("negative maxAge should exclude everything, got %d", len(c))
	}
	// a generous filter keeps them
	if c := BranchCandidates(repoRoot, 48*time.Hour); len(c) == 0 {
		t.Error("48h filter should keep just-created branches")
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
