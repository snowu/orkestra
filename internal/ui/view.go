package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

const helpLine = "ENTER=attach tmux   alt-ENTER=cd only   ctrl-n=new-task   ctrl-x=end-task   ctrl-k=kill session   ctrl-r=refresh   ?=info panel   ctrl-s=git status"

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
	b.WriteString(styleBold.Render(fmt.Sprintf("%-16s %-32s %-14s %-8s %-8s %-16s %-9s %s",
		"REPO", "TASK", "BRANCH", "STATE", "AGENT", "SESSION", "LAST USED", "CMD")) + "\n")
	if m.filter != "" {
		b.WriteString("> " + m.filter + "\n")
	}

	listH := m.height - 4
	if m.preview != previewOff {
		listH = m.height - m.height*6/10 - 4
	}
	if listH < 3 {
		listH = 3
	}

	start := 0
	if m.cursor >= listH {
		start = m.cursor - listH + 1
	}
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

		line := lipgloss.NewStyle().Foreground(repoColor(r.Repo)).Render(pad(r.Repo, 16)) + " " +
			pad(r.Task, 32) + " " +
			pad(trunc(branch, 14), 14) + " " +
			stateStyle.Render(pad(state, 8)) + " " +
			agentStyle.Render(pad(agent, 8)) + " " +
			pad(trunc(sess, 16), 16) + " " +
			pad(ago(r.LastUsed), 9) + " " +
			trunc(cmd, 12)
		if i == m.cursor {
			line = styleSel.Render("> ") + line
		} else {
			line = "  " + line
		}
		b.WriteString(line + "\n")
	}
	if len(m.visible) == 0 {
		b.WriteString(styleDim.Render("  (no worktrees found)") + "\n")
	}

	if m.preview != previewOff {
		b.WriteString(styleDim.Render(strings.Repeat("-", max(1, m.width))) + "\n")
		b.WriteString(m.previewText)
	}

	if m.mode == modeConfirmEnd || m.mode == modeConfirmKill {
		b.WriteString("\n" + m.viewConfirm())
	}
	if m.err != "" {
		b.WriteString("\n" + styleYellow.Render(m.err))
	}
	return b.String()
}

func (m *Model) viewConfirm() string {
	sel, _ := m.selected()
	verb := fmt.Sprintf("DELETE worktree + branch (local & origin) for %s/%s", sel.Repo, sel.Task)
	if m.mode == modeConfirmKill {
		verb = fmt.Sprintf("kill tmux session for %s/%s (worktree+branch untouched)", sel.Repo, sel.Task)
	}
	no, yes := "[ no ]", "  yes "
	if m.confirmYes {
		no, yes = "  no  ", "[ yes ]"
	}
	return styleBold.Render(" "+verb) + "\n " +
		styleGreen.Render(no) + "  " + styleYellow.Render(yes) +
		styleDim.Render("   (enter=confirm, esc=cancel, y/n)")
}

func (m *Model) viewPickRepo() string {
	var b strings.Builder
	b.WriteString(styleBold.Render("ctrl-n new-task: pick a repo (esc to cancel)") + "\n")
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
		line := name
		if fav[name] {
			line = name + styleDim.Render("  *")
		}
		if i == m.repoCursor {
			b.WriteString(styleSel.Render("> "+line) + "\n")
		} else {
			b.WriteString("  " + line + "\n")
		}
	}
	return b.String()
}

func (m *Model) viewTaskName() string {
	var b strings.Builder
	b.WriteString(styleBold.Render(fmt.Sprintf("new task in %s (esc = back, enter = create)", m.pickedRepo)) + "\n")
	b.WriteString(fmt.Sprintf("task '%s'> %s█\n\n", m.pickedRepo, m.taskInput))
	b.WriteString(styleDim.Render("existing branches (reference):") + "\n")
	for i, br := range m.branches {
		if i >= max(3, m.height-8) {
			break
		}
		b.WriteString(styleDim.Render("  "+br) + "\n")
	}
	return b.String()
}
