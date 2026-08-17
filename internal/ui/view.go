package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"orkestra/internal/worktree"
)

const helpLine = "ENTER=attach   ctrl-n=new-task   ctrl-g=spawn fe/be   ctrl-k=kill   ctrl-x=end-task   ?=help"

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
	case modeConfirmSteal:
		return m.viewConfirmSteal()
	case modeScan:
		return m.viewScan()
	case modeHelp:
		return m.viewHelpOverlay()
	case modeGroupPick:
		return m.viewGroupPick()
	}
	return m.viewList()
}

func (m *Model) viewList() string {
	var b strings.Builder
	helpRow := styleDim.Render(helpLine)
	if len(m.cfg.Groups) > 0 {
		status := styleDim.Render("proxy :3000 down")
		if m.proxyUp {
			status = styleGreen.Render("proxy :3000 up")
		}
		helpRow += "   " + status
	}
	b.WriteString(helpRow + "\n")
	// Two leading spaces match the rows' cursor-marker prefix so the
	// header sits exactly over its columns.
	// Extra 2 columns for the pair-link bracket left of REPO.
	b.WriteString("    " + styleBold.Render(fmt.Sprintf("%-16s %-32s %-14s %-8s %-8s %-10s %-16s %-9s %s",
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
	const rowPlainWidth = 2 + 2 + 16 + 1 + 32 + 1 + 14 + 1 + 8 + 1 + 8 + 1 + 10 + 1 + 16 + 1 + 9 + 1 + 12

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

		countStr, countStyle := "", styleDim
		if r.GroupSize > 0 {
			countStr = fmt.Sprintf("%d/%d", r.GroupLive, r.GroupSize)
			countStyle = styleDim
			if r.GroupLive > 0 {
				countStyle = styleGreen
			}
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

		febeShown := rs(countStyle, countStr) + rs(styleCyan, portStr) +
			rs(plain, strings.Repeat(" ", max(0, 10-(len(countStr)+len(portStr)))))

		cmdShown := trunc(cmd, 12)
		prefix := "  "
		if selected {
			prefix = "> "
		}
		// Group-link bracket: the N sibling rows of one group sort adjacent
		// (see BuildRows), so the first gets ╭, the last ╰, and any rows in
		// between get │, in the column left of REPO, colored like the task.
		link, linkStyle := "  ", taskStyle
		nextSib := i+1 < len(m.visible) && worktree.GroupSiblings(m.cfg, r, m.rows[m.visible[i+1]])
		prevSib := i > 0 && worktree.GroupSiblings(m.cfg, r, m.rows[m.visible[i-1]])
		switch {
		case nextSib && prevSib:
			link = "│ "
		case nextSib:
			link = "╭ "
		case prevSib:
			link = "╰ "
		}
		line := rs(styleSel, prefix) + rs(linkStyle, link) +
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
		verb = fmt.Sprintf("kill session for %s/%s (worktree+branch untouched)", sel.Repo, sel.Task)
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
		sel := i == m.repoCursor
		line := name
		if _, isGroup := m.repoGroups[name]; isGroup {
			line = styleCyan.Render(name)
		} else if fav[name] {
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

func (m *Model) viewTaskName() string {
	var b strings.Builder
	target := m.pickedRepo
	if m.pickedGroup != nil {
		target = fmt.Sprintf("%s (%d repos)", m.pickedGroup.Name, len(m.pickedGroup.Processes))
	}
	b.WriteString(styleBold.Render(fmt.Sprintf("new task in %s (esc = back, ↑↓ = reuse a branch)", target)) + "\n")

	cursor := " "
	if m.branchCursor == 0 {
		cursor = ">"
	}
	line := fmt.Sprintf("%s new branch '%s'> %s█", cursor, target, m.taskInput)
	if m.branchCursor == 0 {
		line = styleSel.Render(line)
	}
	b.WriteString(line + "\n\n")

	branches := m.filteredBranches()
	if len(branches) == 0 {
		b.WriteString(styleDim.Render("  (no branches without a worktree)") + "\n")
		return b.String()
	}
	b.WriteString(styleDim.Render("branches without a worktree:") + "\n")
	for i, br := range branches {
		if i >= max(3, m.height-10) {
			break
		}
		row := fmt.Sprintf("  %s  %s", br.Name, humanAge(br.Tip))
		if br.InMain {
			row += styleDim.Render(" (in main repo)")
		}
		if m.branchCursor == i+1 {
			row = styleSel.Render("> " + strings.TrimLeft(row, " "))
		} else {
			row = styleDim.Render(row)
		}
		b.WriteString(row + "\n")
	}
	return b.String()
}

func (m *Model) viewConfirmSteal() string {
	c := m.stealConflict
	if c == nil {
		return ""
	}
	where := "worktree"
	fix := c.Path + " will be detached"
	if c.IsMain {
		where = "main repo"
		fix = c.Path + " will be switched to its base branch"
	}
	var b strings.Builder
	b.WriteString(styleBold.Render(fmt.Sprintf("branch '%s' is checked out in %s (%s)", c.Branch, c.Path, where)) + "\n\n")
	b.WriteString(fmt.Sprintf("move it to a new worktree for %s?\n\n", worktree.TaskNameFor(c.Branch)))
	b.WriteString("  " + fix + "   [enter]\n")
	b.WriteString("  " + styleDim.Render("cancel                          [esc]") + "\n")
	return b.String()
}

func (m *Model) viewScan() string {
	var b strings.Builder
	b.WriteString(styleBold.Render("branches from the last 48h without a worktree (esc = back)") + "\n")
	b.WriteString(fmt.Sprintf("filter> %s█\n\n", m.scanFilter))
	if m.scanning {
		b.WriteString(styleDim.Render("  scanning repos…") + "\n")
		return b.String()
	}
	cands := m.filteredScan()
	if len(cands) == 0 {
		b.WriteString(styleDim.Render("  (nothing recent without a worktree)") + "\n")
		return b.String()
	}
	for i, c := range cands {
		if i >= max(3, m.height-8) {
			break
		}
		row := fmt.Sprintf("  %-20s %-40s %s", c.Repo, c.Name, humanAge(c.Tip))
		if c.InMain {
			row += styleDim.Render(" (in main repo)")
		}
		if i == m.scanCursor {
			row = styleSel.Render(row)
		}
		b.WriteString(row + "\n")
	}
	return b.String()
}

// viewGroupPick renders the ctrl-g ambiguity picker: repo/task matched
// several configured groups with an equal claim, so the user picks which
// one to actually spawn. Each candidate is shown with its process labels
// and repos so the choice is informative — the whole point is knowing what
// ctrl-g is about to start.
func (m *Model) viewGroupPick() string {
	var b strings.Builder
	b.WriteString(styleBold.Render(fmt.Sprintf("%s/%s matches multiple groups — pick one (esc to cancel)", m.groupRepo, m.groupTask)) + "\n\n")
	for i, g := range m.groupCands {
		var procs []string
		for _, p := range g.Processes {
			procs = append(procs, fmt.Sprintf("%s(%s)", p.Label, p.Repo))
		}
		line := fmt.Sprintf("%-16s %s", g.Name, strings.Join(procs, " "))
		if i == m.groupCursor {
			b.WriteString(styleSel.Render("> "+line) + "\n")
		} else {
			b.WriteString(styleDim.Render("  "+line) + "\n")
		}
	}
	return b.String()
}

// helpBinding is one row of the help modal: a key and what it does.
type helpBinding struct{ key, desc string }

// helpGroup is a named cluster of bindings, grouped by what the user is
// trying to DO rather than by which handler happens to own the key.
type helpGroup struct {
	title    string
	bindings []helpBinding
}

var helpGroups = []helpGroup{
	{"Navigate", []helpBinding{
		{"up / ctrl-p", "move cursor up"},
		{"down / ctrl-j", "move cursor down"},
		{"(typing)", "filter the list"},
		{"backspace", "delete filter character"},
		{"esc", "clear filter"},
	}},
	{"Worktrees", []helpBinding{
		{"ctrl-n", "new task (pick repo or group, then name it or reuse a branch)"},
		{"ctrl-f", "scan: branches from the last 48h with no worktree"},
		{"ctrl-x", "end task (remove worktree + branch)"},
	}},
	{"Sessions & dev servers", []helpBinding{
		{"enter", "attach session"},
		{"alt+enter", "cd only (no attach)"},
		{"ctrl-k", "kill session"},
		{"ctrl-g", "spawn fe/be dev servers in background (prompts if ambiguous)"},
		{"ctrl-a", "open all: attach with fe/be windows"},
		{"ctrl-o", "open fe in browser"},
		{"ctrl-r", "refresh"},
	}},
	{"View", []helpBinding{
		{"tab", "cycle info / git status / off"},
		{"ctrl-s", "split preview (git status | live info)"},
	}},
	{"Quit", []helpBinding{
		{"ctrl-c", "quit"},
	}},
}

// helpProseLines are the condensed prose sections, one or two lines each,
// covering screens that have no dedicated key-binding group above.
var helpProseLines = []string{
	"",
	"New-task: typed text (row 0) creates a NEW branch; arrow down to reuse",
	"an existing branch instead — typing both filters the list and names it.",
	"",
	"Repo picker: group rows (shown first, e.g. \"name (a+b+c)\") create a",
	"worktree in every member repo for the same task in one go.",
	"",
	"Scan (ctrl-f / ork scan): recent branches with no worktree. Rows marked",
	"\"(in main repo)\" prompt to switch that checkout to its base branch.",
}

// helpContentLines renders the help body (title + groups + prose) as plain
// (unstyled-width-wise, but ANSI-colored) lines, without any clamping —
// clamping/truncation is applied by the caller once box size is known.
func helpContentLines() []string {
	var lines []string
	lines = append(lines, styleBold.Render("ork help")+styleDim.Render("  (?, esc, q, or enter to close)"))
	lines = append(lines, "")
	for _, g := range helpGroups {
		lines = append(lines, styleCyan.Render(g.title))
		for _, bind := range g.bindings {
			lines = append(lines, styleDim.Render(fmt.Sprintf("  %-16s %s", bind.key, bind.desc)))
		}
		lines = append(lines, "")
	}
	for _, l := range helpProseLines {
		if l == "" {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, styleDim.Render("  "+l))
	}
	// Trim a trailing blank line, if any.
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// viewHelp renders the full-screen (non-overlay) help modal. Used as a
// fallback when the terminal is too small for the floating box, and by the
// overlay path itself when computing content. Clamped to m.height like every
// other screen in this file — an overflowing modal would shear the frame
// diff the same way an overflowing preview does.
func (m *Model) viewHelp() string {
	lines := helpContentLines()
	maxLines := max(3, m.height)
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, max(1, m.width), "…")
	}
	return strings.Join(lines, "\n")
}

// overlay composites `box` (a block of ANSI-styled lines) on top of `bg` at
// the given row/col offset. bg lines may contain ANSI escapes, so splice
// points are computed with ansi.StringWidth/Truncate — never len() or plain
// slicing — or the frame shears (see comments elsewhere in this file about
// exactly that failure mode).
func overlay(bg []string, box []string, row, col int) []string {
	out := make([]string, len(bg))
	copy(out, bg)

	for i, boxLine := range box {
		r := row + i
		if r < 0 || r >= len(out) {
			continue
		}
		bgLine := out[r]
		bgW := ansi.StringWidth(bgLine)
		// Pad the background line if it's shorter than the box's left edge.
		if bgW < col {
			bgLine += strings.Repeat(" ", col-bgW)
			bgW = col
		}
		left := ansi.Truncate(bgLine, col, "")
		boxW := ansi.StringWidth(boxLine)
		rightEdge := col + boxW
		var right string
		if bgW > rightEdge {
			right = ansi.TruncateLeft(bgLine, rightEdge, "")
		}
		out[r] = left + boxLine + right
	}
	return out
}

// viewHelpOverlay renders the normal list frame and composites a centered,
// rounded-border help box on top of it, so worktree rows stay visible
// around the edges. Falls back to the full-page help render if the terminal
// is too small for a sensible box.
func (m *Model) viewHelpOverlay() string {
	bgStr := m.viewList()
	bg := strings.Split(bgStr, "\n")

	content := helpContentLines()

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("80")). // styleCyan's color
		Padding(0, 1)

	// Budget content to fit within the frame, leaving at least a 2-line/
	// 4-column margin on each side for the border+padding to land inside
	// m.width/m.height.
	maxContentW := m.width - 8
	maxContentH := m.height - 6
	if len(bg)-6 < maxContentH {
		maxContentH = len(bg) - 6
	}
	if maxContentW < 20 || maxContentH < 5 {
		// Terminal too small for a floating box — degrade to full-page.
		return m.viewHelp()
	}

	trimmed := content
	if len(trimmed) > maxContentH {
		trimmed = trimmed[:maxContentH]
	}
	for i, l := range trimmed {
		trimmed[i] = ansi.Truncate(l, maxContentW, "…")
	}

	boxStr := boxStyle.Render(strings.Join(trimmed, "\n"))
	boxLines := strings.Split(boxStr, "\n")

	boxH := len(boxLines)
	boxW := 0
	for _, l := range boxLines {
		if w := ansi.StringWidth(l); w > boxW {
			boxW = w
		}
	}

	if boxW > m.width || boxH > len(bg) {
		// Still doesn't fit (extreme aspect ratios) — fall back.
		return m.viewHelp()
	}

	row := max(0, (len(bg)-boxH)/2)
	col := max(0, (m.width-boxW)/2)

	out := overlay(bg, boxLines, row, col)
	// Final safety clamp: never emit a line wider than m.width or more
	// lines than m.height.
	if len(out) > m.height {
		out = out[:m.height]
	}
	for i, l := range out {
		out[i] = ansi.Truncate(l, max(1, m.width), "")
	}
	return strings.Join(out, "\n")
}

// humanAge renders a branch tip age compactly ("3h", "2d").
func humanAge(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
