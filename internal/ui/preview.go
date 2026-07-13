package ui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"orkestra/internal/config"
	"orkestra/internal/tmux"
	"orkestra/internal/worktree"
)

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

func repoCachePath() string {
	return filepath.Join(homeDir(), ".cache/ork/repo-scan")
}

// resolvePane: same rule as row building — match by cwd first, else by a
// session named after the task (sessions are shared by task name across
// repos by default).
func resolvePane(cfg config.Config, r worktree.Row) *tmux.Pane {
	panes := tmux.ListPanes()
	for i, p := range panes {
		if p.CWD == r.Path {
			return &panes[i]
		}
	}
	if tmux.HasSession(r.Task) {
		for i, p := range panes {
			if p.Session == r.Task {
				return &panes[i]
			}
		}
	}
	return nil
}

// windowsLine lists the base session's windows — fe/be live as windows in
// this one session (ctrl-g/ctrl-a), not separate sessions, so this is the
// full picture: switch with ctrl-b + window number, no other session to
// hunt for.
func windowsLine(cfg config.Config, r worktree.Row) string {
	name := worktree.SessionName(cfg, r.Repo, r.Task)
	names := tmux.SessionWindowNames(name)
	if len(names) == 0 {
		return styleCyan.Render(" windows:") + " none\n"
	}
	return styleCyan.Render(" windows:") + " " + strings.Join(names, ", ") + "\n"
}

// infoPreview: branch/path summary + tmux session details + the live
// pane's bottommost lines.
func infoPreview(cfg config.Config, r worktree.Row, lines int) string {
	var b strings.Builder

	// Always the real branch name — never the "=" shorthand; this panel is
	// meant to be exact where the row list is compact.
	branch := worktree.GitBranch(r.Path)
	if branch == "" {
		branch = "none"
	}
	shortPath := strings.Replace(r.Path, homeDir(), "~", 1)
	b.WriteString(styleCyan.Render(" branch:") + " " + styleBold.Render(branch) + "\n")
	b.WriteString(styleCyan.Render(" path:") + "   " + styleDim.Render(shortPath) + "\n")

	if cfg.FERepo != "" && cfg.BERepo != "" {
		port := worktree.TaskPort(r.Task)
		feOn, beOn := styleDim.Render("-"), styleDim.Render("-")
		if r.FELive {
			feOn = styleGreen.Render("up")
		}
		if r.BELive {
			beOn = styleGreen.Render("up")
		}
		b.WriteString(styleCyan.Render(" fe/be port:") + fmt.Sprintf(" %d  ", port) +
			"fe: " + feOn + "  be: " + beOn + "\n")
	}

	b.WriteString(windowsLine(cfg, r))

	pane := resolvePane(cfg, r)
	if pane == nil {
		b.WriteString("\n(no live tmux session)\n")
		return b.String()
	}
	note := ""
	if pane.CWD != r.Path {
		note = "  (shared, cwd: " + strings.Replace(pane.CWD, homeDir(), "~", 1) + ")"
	}
	b.WriteString(styleCyan.Render(" running:") + " " + styleGreen.Render(pane.Cmd) + fmt.Sprintf(" (pid %d)%s", pane.PID, note) + "\n")

	b.WriteString(styleDim.Render(strings.Repeat("-", 40)) + "\n")
	content := tmux.CapturePane(pane.Target)
	tail := lastLines(content, lines-5)
	b.WriteString(tail)
	return b.String()
}

func gitStatusPreview(r worktree.Row) string {
	// Deliberately plain `git status` — same output you'd see cd'ing in
	// and running it by hand. color.status=always because git strips color
	// when stdout isn't a tty (it's a pipe here); the TUI passes ANSI
	// through fine.
	if _, err := os.Stat(r.Path); err != nil {
		return "(worktree not found: " + r.Path + ")"
	}
	out, _ := exec.Command("git", "-C", r.Path, "-c", "color.status=always", "status").CombinedOutput()
	return string(out)
}

// splitPreview: ctrl-s view — git status (left) and the live info panel
// (right), 50/50. Column padding/truncation must be ANSI-aware: both sides
// carry color codes, and byte-length math would shear the divider.
func splitPreview(cfg config.Config, r worktree.Row, lines, width int) string {
	colW := width/2 - 2
	if colW < 20 {
		colW = 20
	}
	left := strings.Split(strings.TrimRight(gitStatusPreview(r), "\n"), "\n")
	right := strings.Split(strings.TrimRight(infoPreview(cfg, r, lines), "\n"), "\n")

	n := len(left)
	if len(right) > n {
		n = len(right)
	}
	if n > lines {
		n = lines
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		var l, rr string
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			rr = right[i]
		}
		l = ansi.Truncate(l, colW, "…")
		if w := lipgloss.Width(l); w < colW {
			l += strings.Repeat(" ", colW-w)
		}
		b.WriteString(l + styleDim.Render(" │ ") + ansi.Truncate(rr, colW, "…") + "\n")
	}
	return b.String()
}

func lastLines(s string, n int) string {
	if n < 1 {
		n = 1
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func existingBranches(repoRoot string) []string {
	if repoRoot == "" {
		return nil
	}
	out, err := exec.Command("git", "-C", repoRoot, "branch", "--format=%(refname:short)").Output()
	if err != nil {
		return nil
	}
	var branches []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			branches = append(branches, l)
		}
	}
	return branches
}
