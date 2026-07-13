// Package worktree: discovery of repos and per-task worktrees, row
// assembly, and the new-task/end-task lifecycle.
package worktree

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Directory names never worth descending into while scanning for repos —
// dependency trees and build output can contain thousands of nested dirs
// (and occasionally their own vendored .git dirs). Same list as the bash
// version's _ORK_SCAN_PRUNE.
var scanPrune = map[string]bool{
	"node_modules": true, ".cache": true, "vendor": true, "dist": true,
	"build": true, "target": true, ".venv": true, "venv": true,
	"__pycache__": true, ".terraform": true,
}

// AllWorktreeDirs lists every <root>/<repo>/<task> dir across all roots.
func AllWorktreeDirs(roots []string) []string {
	var out []string
	for _, root := range roots {
		repos, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, repo := range repos {
			if !repo.IsDir() {
				continue
			}
			tasks, err := os.ReadDir(filepath.Join(root, repo.Name()))
			if err != nil {
				continue
			}
			for _, task := range tasks {
				if task.IsDir() {
					out = append(out, filepath.Join(root, repo.Name(), task.Name()))
				}
			}
		}
	}
	return out
}

// AllRepoDirs lists every repo checkout under home — a repo is any dir
// containing a .git DIRECTORY (worktrees have a .git file, so they're
// naturally excluded). Results are cached on disk: unbounded rescans
// ranged 0.7s-17s depending on disk contention, which made pickers look
// frozen.
func AllRepoDirs(home string, maxDepth int, cachePath string, ttl time.Duration) []string {
	if st, err := os.Stat(cachePath); err == nil && time.Since(st.ModTime()) <= ttl {
		if data, err := os.ReadFile(cachePath); err == nil {
			return splitLines(string(data))
		}
	}

	var out []string
	depthOf := func(p string) int {
		rel, err := filepath.Rel(home, p)
		if err != nil || rel == "." {
			return 0
		}
		return strings.Count(rel, string(filepath.Separator)) + 1
	}
	filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		name := d.Name()
		if scanPrune[name] {
			return fs.SkipDir
		}
		if name == ".git" {
			out = append(out, filepath.Dir(path))
			return fs.SkipDir
		}
		if depthOf(path) >= maxDepth {
			return fs.SkipDir
		}
		return nil
	})

	os.MkdirAll(filepath.Dir(cachePath), 0o755)
	os.WriteFile(cachePath, []byte(strings.Join(out, "\n")+"\n"), 0o644)
	return out
}

// FindRepoRoot resolves a repo checkout dir by basename; first match wins.
func FindRepoRoot(repos []string, name string) string {
	for _, r := range repos {
		if filepath.Base(r) == name {
			return r
		}
	}
	return ""
}

// FindWorktree returns the first existing <root>/<repo>/<task> across
// roots; empty if none exist yet.
func FindWorktree(roots []string, repo, task string) string {
	for _, root := range roots {
		p := filepath.Join(root, repo, task)
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	return ""
}

// WorktreeOrDefault is FindWorktree with the bash scripts' usual fallback
// to the first root's would-be path when the worktree doesn't exist yet.
func WorktreeOrDefault(roots []string, repo, task string) string {
	if wt := FindWorktree(roots, repo, task); wt != "" {
		return wt
	}
	return filepath.Join(roots[0], repo, task)
}

func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}
