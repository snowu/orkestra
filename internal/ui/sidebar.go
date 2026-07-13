package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// cowSidebar reproduces the bash fortune_sidebar: one fortune/cowsay block
// whose lines get pasted to the right of the worktree rows, so the orc
// appears to sit beside the table. Empty when either binary is missing.
func cowSidebar() []string {
	if _, err := exec.LookPath("fortune"); err != nil {
		return nil
	}
	if _, err := exec.LookPath("cowsay"); err != nil {
		return nil
	}
	f, err := exec.Command("fortune", "-s").Output()
	if err != nil {
		return nil
	}
	folded := foldText(strings.TrimRight(string(f), "\n"), 35)

	args := []string{"-n"}
	if cow := orcCowPath(); cow != "" {
		args = append(args, "-f", cow)
	}
	c := exec.Command("cowsay", args...)
	c.Stdin = strings.NewReader(folded)
	out, err := c.Output()
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(out), "\n"), "\n")
}

// orcCowPath finds orc.cow next to the real executable (dev checkout or
// install dir), or via ORK_ROOT.
func orcCowPath() string {
	candidates := []string{}
	if root := os.Getenv("ORK_ROOT"); root != "" {
		candidates = append(candidates, filepath.Join(root, "orc.cow"))
	}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "orc.cow"),
			filepath.Join(dir, "..", "orc.cow"), // bin/ork in the dev checkout
		)
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// foldText wraps at word boundaries like `fold -s -w`.
func foldText(s string, w int) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		for len(line) > w {
			cut := strings.LastIndex(line[:w+1], " ")
			if cut <= 0 {
				cut = w
			}
			out = append(out, strings.TrimRight(line[:cut], " "))
			line = strings.TrimLeft(line[cut:], " ")
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
