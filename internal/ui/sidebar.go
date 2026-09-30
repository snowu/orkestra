package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	f, err := exec.CommandContext(ctx, "fortune", "-s").Output()
	if err != nil {
		return nil
	}
	folded := foldText(strings.TrimRight(string(f), "\n"), 35)

	var args []string
	if cow := orcCowPath(); cow != "" {
		args = append(args, "-f", cow)
	}
	// Some cowsay versions treat everything after -n as the message.
	args = append(args, "-n")
	c := exec.CommandContext(ctx, "cowsay", args...)
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
		if st, err := os.Stat(c); err == nil && st.Mode().IsRegular() {
			return c
		}
	}
	return ""
}

// foldText wraps at word boundaries like `fold -s -w`.
func foldText(s string, w int) string {
	if w <= 0 {
		return s
	}
	return ansi.Wrap(s, w, "")
}
