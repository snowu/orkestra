package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"orkestra/internal/worktree"
)

const helpLine = "ENTER=attach tmux   alt-ENTER=cd only   ctrl-n=new-task   ctrl-x=end-task   ctrl-k=kill session   ctrl-r=refresh   tab=cycle info/status   ctrl-s=split   ctrl-g=spawn fe/be   ctrl-a=open all   ctrl-o=browser"

func trunc(s string, w int) string {
	if len(s) > w {
		if w <= 3 {
			return s[:w]
		}
		return s[:w-3] + "..."
	}
	return s
}

func pad(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-len(s))
}

// ago renders "5m ago" style relative times; "-" for never.
func ago(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func (m *Model) View() string {
	switch m.mode {
	case modePickRepo:
		return m.viewPickRepo()
	case modeTaskName:
		return m.viewTaskName()
	}

	var b strings.Builder
	b.WriteString(styleDim.Render(helpLine) + "\n")
	// Two leading spaces match the rows' cursor-marker prefix so the
	// header sits exactly over its columns.
	b.WriteString("  " + styleBold.Render(fmt.Sprintf("%-16s %-32s %-14s %-8s %-8s %-10s %-16s %-9s %s",
		"REPO", "TASK", "BRANCH", "STATE", "AGENT", "FE/BE", "SESSION", "LAST USED", "CMD")) + "\n")
	// Always drawn (even empty) so typing a filter doesn't shift the rows.
	// The kill/end confirmation takes over this line — top of the screen,
	// next to the rows it's about, instead of buried under the preview.
	if m.mode == modeConfirmEnd || m.mode == modeConfirmKill {
		b.WriteString(m.viewConfirm() + "\n")
	} else {
		// fzf-style match counter, like the bash picker had.
		b.WriteString("> " + m.filter + styleDim.Render(fmt.Sprintf("   %d/%d", len(m.visible), len(m.rows))) + "\n")
	}

	listH := m.height - 5
	if m.preview != previewOff {
		listH = m.height - m.height*6/10 - 5
	}
	if listH < 3 {
		listH = 3
	}

	// Visible width of a full row (plain text, before styling): the padded
	// columns joined by single spaces, plus the 2-char cursor prefix.
	const rowPlainWidth = 2 + 16 + 1 + 32 + 1 + 14 + 1 + 8 + 1 + 8 + 1 + 10 + 1 + 16 + 1 + 9 + 1 + 12

	// Cow sidebar sits a comfortable gap right of the table, but never
	// past the terminal edge — a wide fortune bubble gets pulled left
	// toward the minimum gap, and hidden entirely if it still can't fit
	// (otherwise lines wrap and the whole layout shears).
	cowW := 0
	for _, l := range m.cow {
		if len(l) > cowW {
			cowW = len(l)
		}
	}
	cowCol := rowPlainWidth + 25
	if cowCol+cowW > m.width {
		cowCol = m.width - cowW
	}
	showCow := len(m.cow) > 0 && cowCol >= rowPlainWidth+6

	start := 0
	if m.cursor >= listH {
		start = m.cursor - listH + 1
	}
	rendered := 0
	for i := start; i < len(m.visible) && i < start+listH; i++ {
		r := m.rows[m.visible[i]]

		branch := r.Branch
		if branch == r.Task {
			branch = "="
		}
		if branch == "" {
			branch = "none"
		}
		state, stateStyle := "idle", styleYellow
		if r.Live {
			state, stateStyle = "live", styleGreen
		}
		agent := r.Agent
		agentStyle := styleDim
		switch agent {
		case "running":
			agentStyle = styleGreen
		case "waiting":
			agentStyle = styleCyan
		case "input":
			agentStyle = styleYellow
		case "":
			agent = "-"
		}
		sess, cmd := r.Session, r.Cmd
		if sess == "" {
			sess = "-"
		}
		if cmd == "" {
			cmd = "-"
		}

		feCh, feStyle := "-", styleDim
		if r.FELive {
			feCh, feStyle = "f", styleGreen
		}
		beCh, beStyle := "-", styleDim
		if r.BELive {
			beCh, beStyle = "b", styleGreen
		}
		// FE port shown only when something is actually running — the
		// number is meaningless (and noisy) for idle rows. FE over BE:
		// that's the one you open in the browser.
		portStr := ""
		if r.FELive || r.BELive {
			fePort, _ := worktree.TaskPorts(r.Task)
			portStr = fmt.Sprintf(" %d", fePort)
		}

		// rs renders a segment, adding the selection background on the
		// cursor row. Per-segment because each column's color codes end in
		// a reset that would kill a single line-wide background — the whole
		// row highlights only if every segment carries the bg itself.
		selected := i == m.cursor
		rs := func(st lipgloss.Style, s string) string {
			if selected {
				st = st.Background(colorSelBg)
			}
			return st.Render(s)
		}
		plain := renderer.NewStyle()

		taskStyle, sessStyle := plain, plain
		if c, ok := m.taskColors[r.Task]; ok {
			taskStyle = renderer.NewStyle().Foreground(c)
			sessStyle = taskStyle
		}

		febeShown := rs(feStyle, feCh) + rs(plain, "/") + rs(beStyle, beCh) + rs(styleCyan, portStr) +
			rs(plain, strings.Repeat(" ", max(0, 10-(3+len(portStr)))))

		cmdShown := trunc(cmd, 12)
		prefix := "  "
		if selected {
			prefix = "> "
		}
		line := rs(styleSel, prefix) +
			rs(renderer.NewStyle().Foreground(m.repoColors[r.Repo]), pad(trunc(r.Repo, 16), 16)) + rs(plain, " ") +
			rs(taskStyle, pad(trunc(r.Task, 32), 32)) + rs(plain, " ") +
			rs(plain, pad(trunc(branch, 14), 14)) + rs(plain, " ") +
			rs(stateStyle, pad(state, 8)) + rs(plain, " ") +
			rs(agentStyle, pad(agent, 8)) + rs(plain, " ") +
			febeShown + rs(plain, " ") +
			rs(sessStyle, pad(trunc(sess, 16), 16)) + rs(plain, " ") +
			rs(plain, pad(ago(r.LastUsed), 9)) + rs(plain, " ") +
			rs(plain, cmdShown)
		// Paste the cowsay block beside the table — padding computed on
		// plain-text width (escape codes are invisible but non-zero-length).
		if ci := i - start; showCow && ci < len(m.cow) {
			plainLen := rowPlainWidth - 12 + len(cmdShown)
			padN := cowCol - plainLen
			if padN < 1 {
				padN = 1
			}
			line += strings.Repeat(" ", padN) + styleDim.Render(m.cow[ci])
		}
		// Whole-row clamp: anything past the terminal edge (long cmd, cow
		// bubble pulled left) would soft-wrap and shear the frame diff,
		// same failure mode as the preview.
		b.WriteString(ansi.Truncate(line, max(1, m.width), "…") + "\n")
		rendered++
	}
	if len(m.visible) == 0 {
		b.WriteString(styleDim.Render("  (no worktrees found)") + "\n")
		rendered++
	}
	// Pad the list area to a fixed height so filtering down to fewer rows
	// doesn't collapse the panel and yank the preview upward — and the orc
	// keeps its remaining lines on the blank rows.
	for ; rendered < listH; rendered++ {
		blank := ""
		if ci := rendered; showCow && ci < len(m.cow) {
			blank = strings.Repeat(" ", cowCol) + styleDim.Render(m.cow[ci])
		}
		b.WriteString(blank + "\n")
	}

	if m.preview != previewOff {
		b.WriteString(styleDim.Render(strings.Repeat("-", max(1, m.width))) + "\n")
		b.WriteString(fitPreview(m.previewText, m.previewLines(), max(1, m.width)))
	}

	if m.err != "" {
		b.WriteString("\n" + styleYellow.Render(m.err))
	}
	return b.String()
}

// fitPreview hard-clamps the preview block to the space View budgets for
// it: at most maxLines lines, each ANSI-truncated to the terminal width.
// Without this, one over-wide line (raw git-status paths, captured pane
// columns) soft-wraps, the frame grows taller than bubbletea thinks it is,
// and its line diff smears stale fragments across the screen until the
// next full repaint.
func fitPreview(text string, maxLines, width int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	for i, l := range lines {
		// Tabs first: the terminal renders \t as up to 8 cells but width
		// math counts 1, so an un-expanded tab can still wrap the line.
		l = strings.ReplaceAll(l, "\t", "        ")
		lines[i] = ansi.Truncate(l, width, "…")
	}
	return strings.Join(lines, "\n")
}

func (m *Model) viewConfirm() string {
	sel, _ := m.selected()
	verb := fmt.Sprintf("DELETE worktree + branch (local & origin) for %s/%s", sel.Repo, sel.Task)
	if m.mode == modeConfirmKill {
		verb = fmt.Sprintf("kill tmux session for %s/%s (worktree+branch untouched)", sel.Repo, sel.Task)
	}
	no, yes := "[no]", " yes"
	if m.confirmYes {
		no, yes = " no ", "[yes]"
	}
	// Single line: takes over the filter slot at the top of the screen, so
	// it must not wrap or shift the layout.
	return styleBold.Render(verb+"?") + "  " +
		styleGreen.Render(no) + " " + styleYellow.Render(yes) +
		styleDim.Render("  (enter/esc/y/n)")
}

func (m *Model) viewPickRepo() string {
	var b strings.Builder
	b.WriteString(styleBold.Render("ctrl-n new-task: pick a repo (esc to cancel, ctrl-b = fe+be pair)") + "\n")
	b.WriteString("repo> " + m.repoFilter + "\n\n")
	repos := m.filteredRepos()
	fav := map[string]bool{}
	for _, f := range m.cfg.Favorites {
		fav[f] = true
	}
	listH := max(3, m.height-5)
	start := 0
	if m.repoCursor >= listH {
		start = m.repoCursor - listH + 1
	}
	for i := start; i < len(repos) && i < start+listH; i++ {
		name := repos[i]
		sel := i == m.repoCursor
		if pair, isPair := m.pairEntries[name]; isPair {
			b.WriteString(m.renderPairEntry(pair, sel) + "\n")
			continue
		}
		line := name
		if fav[name] {
			line = name + styleDim.Render("  *")
		}
		if sel {
			b.WriteString(styleSel.Render("> "+line) + "\n")
		} else {
			b.WriteString("  " + line + "\n")
		}
	}
	return b.String()
}

// renderPairEntry draws a fe/be pair row as two repo-colored names joined
// by a link line, so pairs read as one unit instead of a plain string.
func (m *Model) renderPairEntry(pair [2]string, selected bool) string {
	feStyle := renderer.NewStyle().Foreground(m.pairSideColor(pair[0], pair[1], 0))
	beStyle := renderer.NewStyle().Foreground(m.pairSideColor(pair[0], pair[1], 1))
	link, prefix := styleDim, "  "
	if selected {
		feStyle = feStyle.Background(colorSelBg)
		beStyle = beStyle.Background(colorSelBg)
		link = link.Background(colorSelBg)
		prefix = styleSel.Render("> ")
	}
	return prefix + feStyle.Render(pair[0]) + link.Render(" ──⇄── ") + beStyle.Render(pair[1])
}

// pairSideColor reuses the repo's list color when it has rows on screen;
// otherwise falls back to a stable pair of distinct hues so the two sides
// are always distinguishable even before any worktree exists.
func (m *Model) pairSideColor(fe, be string, side int) lipgloss.Color {
	name := [2]string{fe, be}[side]
	if c, ok := m.repoColors[name]; ok {
		return c
	}
	fallback := [2]lipgloss.Color{"80", "179"} // cyan fe, amber be
	return fallback[side]
}

func (m *Model) viewTaskName() string {
	var b strings.Builder
	target := m.pickedRepo
	if m.pickedRepo2 != "" {
		target = m.pickedRepo + " + " + m.pickedRepo2
	}
	b.WriteString(styleBold.Render(fmt.Sprintf("new task in %s (esc = back, enter = create)", target)) + "\n")
	b.WriteString(fmt.Sprintf("task '%s'> %s█\n\n", target, m.taskInput))
	b.WriteString(styleDim.Render("existing branches (reference):") + "\n")
	for i, br := range m.branches {
		if i >= max(3, m.height-8) {
			break
		}
		b.WriteString(styleDim.Render("  "+br) + "\n")
	}
	return b.String()
}
