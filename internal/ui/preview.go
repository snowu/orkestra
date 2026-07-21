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
// pane's bottommost lines (all windows side by side when fe/be spawned).
// pathStyle: the row's repo color from the top pane, so the path reads as
// belonging to that row.
func infoPreview(cfg config.Config, r worktree.Row, lines, width int, pathStyle lipgloss.Style) string {
	var b strings.Builder
	const gap = "    "

	// Always the real branch name — never the "=" shorthand; this panel is
	// meant to be exact where the row list is compact.
	branch := worktree.GitBranch(r.Path)
	if branch == "" {
		branch = "none"
	}
	shortPath := strings.Replace(r.Path, homeDir(), "~", 1)

	// Header packed into two lines across the full width instead of one
	// stacked field per line — the vertical space belongs to the live tail.
	line1 := styleCyan.Render(" branch:") + " " + styleBold.Render(branch)
	if cfg.FERepo != "" && cfg.BERepo != "" {
		fePort, bePort := worktree.TaskPorts(r.Task)
		feOn, beOn := styleDim.Render("-"), styleDim.Render("-")
		if r.FELive {
			feOn = styleGreen.Render("up")
		}
		if r.BELive {
			beOn = styleGreen.Render("up")
		}
		line1 += gap + styleCyan.Render("fe") + fmt.Sprintf(" :%d ", fePort) + feOn +
			"  " + styleCyan.Render("be") + fmt.Sprintf(" :%d ", bePort) + beOn
	}
	line1 += gap + strings.TrimRight(windowsLine(cfg, r), "\n")

	line2 := styleCyan.Render(" path:") + " " + pathStyle.Render(shortPath)

	pane := resolvePane(cfg, r)
	if pane == nil {
		b.WriteString(line1 + "\n" + line2 + "\n\n(no live tmux session)\n")
		return b.String()
	}
	note := ""
	if pane.CWD != r.Path {
		note = "  (shared, cwd: " + pathStyle.Render(strings.Replace(pane.CWD, homeDir(), "~", 1)) + ")"
	}
	line2 += gap + styleCyan.Render("running:") + " " + styleGreen.Render(pane.Cmd) + fmt.Sprintf(" (pid %d)%s", pane.PID, note)

	b.WriteString(line1 + "\n" + line2 + "\n")
	b.WriteString(styleDim.Render(strings.Repeat("-", 40)) + "\n")
	b.WriteString(windowsPreview(pane.Session, pane.Target, lines-3, width))
	return b.String()
}

// windowsPreview captures the live tail of every window in session, side by
// side — one column per window (main | fe | be), so spawned dev servers are
// visible without leaving ork. width<=0 means "don't columnize": single
// window falls back to the plain full-width tail.
func windowsPreview(session, activeTarget string, lines, width int) string {
	// Skip windows running ork itself (the prefix+o keybind opens ork as a
	// window of the current session) — capturing our own UI would render a
	// recursive mirror in the preview. Windows are captured by unique id,
	// never by name: names collide (two "zsh" windows), and a name target
	// silently resolves to the first match — every column would show the
	// same window.
	var wins []tmux.WindowInfo
	for _, w := range tmux.SessionWindows(session) {
		if w.Cmd == "ork" || w.Name == "ork" {
			continue
		}
		wins = append(wins, w)
	}
	if len(wins) == 0 {
		return lastLines(tmux.CapturePane(activeTarget), lines)
	}
	if len(wins) == 1 {
		return lastLines(tmux.CapturePane(wins[0].ID), lines)
	}

	var cols [][]string
	for _, w := range wins {
		content := lastLines(tmux.CapturePane(w.ID), lines-1)
		col := []string{styleBold.Render(w.Name)}
		col = append(col, strings.Split(content, "\n")...)
		cols = append(cols, col)
	}
	return joinColumns(cols, lines, width)
}

// joinColumns renders columns side by side, ANSI-aware (same reasoning as
// splitPreview: escape codes are invisible but non-zero-length, so padding
// math must use rendered width).
func joinColumns(cols [][]string, lines, width int) string {
	if width <= 0 {
		width = 200
	}
	colW := width/len(cols) - 2
	if colW < 20 {
		colW = 20
	}
	n := 0
	for _, c := range cols {
		if len(c) > n {
			n = len(c)
		}
	}
	if n > lines {
		n = lines
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		for j, c := range cols {
			var cell string
			if i < len(c) {
				cell = c[i]
			}
			cell = ansi.Truncate(cell, colW, "…")
			if j < len(cols)-1 {
				if w := lipgloss.Width(cell); w < colW {
					cell += strings.Repeat(" ", colW-w)
				}
				cell += styleDim.Render(" │ ")
			}
			b.WriteString(cell)
		}
		b.WriteString("\n")
	}
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

// gitDiffStatPreview: which files changed and by how much. Against HEAD so
// staged and unstaged edits both count — the row's whole story, not just
// what happens to be unstaged. Untracked files don't diff; git status
// (always shown alongside) covers those.
func gitDiffStatPreview(r worktree.Row) string {
	if _, err := os.Stat(r.Path); err != nil {
		return ""
	}
	out, _ := exec.Command("git", "-C", r.Path, "-c", "color.diff=always", "diff", "--stat", "HEAD").CombinedOutput()
	if strings.TrimSpace(string(out)) == "" {
		return styleDim.Render("(no changes)")
	}
	return string(out)
}

// gitStatusSplit: the tab view — git status (left) and diff --stat
// (right), 50/50.
func gitStatusSplit(r worktree.Row, lines, width int) string {
	return twoColumns(
		styleBold.Render("status")+"\n"+strings.TrimRight(gitStatusPreview(r), "\n"),
		styleBold.Render("diff --stat HEAD")+"\n"+strings.TrimRight(gitDiffStatPreview(r), "\n"),
		lines, width)
}

// splitPreview: ctrl-s view — git status + diff --stat stacked (left) and
// the live info panel (right), 50/50.
func splitPreview(cfg config.Config, r worktree.Row, lines, width int, pathStyle lipgloss.Style) string {
	colW := width/2 - 2
	if colW < 20 {
		colW = 20
	}
	left := strings.TrimRight(gitStatusPreview(r), "\n") + "\n" +
		styleDim.Render(strings.Repeat("-", 24)) + "\n" +
		strings.TrimRight(gitDiffStatPreview(r), "\n")
	return twoColumns(left, strings.TrimRight(infoPreview(cfg, r, lines, colW, pathStyle), "\n"), lines, width)
}

// twoColumns renders left │ right at 50/50. Column padding/truncation must
// be ANSI-aware: both sides carry color codes, and byte-length math would
// shear the divider.
func twoColumns(leftText, rightText string, lines, width int) string {
	colW := width/2 - 2
	if colW < 20 {
		colW = 20
	}
	left := strings.Split(leftText, "\n")
	right := strings.Split(rightText, "\n")

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
