// Package tmux shells out to the tmux CLI — a stable interface; no
// control-mode or library dependency needed for what ork does.
package tmux

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Pane struct {
	Session string // session name
	Target  string // session:window.pane
	CWD     string
	Cmd     string
	PID     int
}

const paneFormat = "#{session_name}:#{window_index}.#{pane_index}\t#{pane_current_path}\t#{pane_current_command}\t#{pane_pid}"

// ListPanes snapshots every pane on the server. Empty slice when no
// server is running.
func ListPanes() []Pane {
	out, err := exec.Command("tmux", "list-panes", "-a", "-F", paneFormat).Output()
	if err != nil {
		return nil
	}
	return ParsePanes(string(out))
}

// ParsePanes parses list-panes output in paneFormat (split out for tests).
func ParsePanes(out string) []Pane {
	var panes []Pane
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 4 || f[0] == "" {
			continue
		}
		pid, _ := strconv.Atoi(f[3])
		panes = append(panes, Pane{
			Session: strings.SplitN(f[0], ":", 2)[0],
			Target:  f[0],
			CWD:     f[1],
			Cmd:     f[2],
			PID:     pid,
		})
	}
	return panes
}

// HasSession uses tmux's exact-match (=) target syntax.
func HasSession(name string) bool {
	return exec.Command("tmux", "has-session", "-t", "="+name).Run() == nil
}

func KillSession(name string) {
	exec.Command("tmux", "kill-session", "-t", "="+name).Run()
}

// InsideTmux reports whether we're already running inside a tmux client.
func InsideTmux() bool { return os.Getenv("TMUX") != "" }

// NewOrAttach lands the user in a session named name rooted at dir.
//
// Outside tmux: exec straight into `tmux new -A` — replaces this process
// and takes over the terminal cleanly.
//
// Inside tmux: exec'ing would nest (tmux refuses), so create-if-needed
// then switch-client. Deliberately NOT `new -A -d`: when -A hits an
// existing session, -d is reinterpreted as attach-session's "detach other
// clients", which would yank other clients off the session.
func NewOrAttach(name, dir string) error {
	if InsideTmux() {
		if !HasSession(name) {
			if err := exec.Command("tmux", "new", "-d", "-s", name, "-c", dir).Run(); err != nil {
				return err
			}
		} else {
			cdSession(name, dir)
		}
		return exec.Command("tmux", "switch-client", "-t", name).Run()
	}
	existed := HasSession(name)
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return err
	}
	if existed {
		cdSession(name, dir)
	}
	return syscall.Exec(tmuxPath, []string{"tmux", "new", "-A", "-s", name, "-c", dir}, os.Environ())
}

// cdSession sends a cd command to an existing session's active pane so
// reused ("shared") sessions land in the target worktree dir instead of
// wherever the pane's cwd happened to be from creation.
func cdSession(name, dir string) {
	exec.Command("tmux", "send-keys", "-t", name, "cd "+shellQuote(dir), "Enter").Run()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// CapturePane returns the pane's visible content. Occasionally empty on
// the first try (socket contention when many panes/clients are active) —
// one retry makes it reliable.
func CapturePane(target string) string {
	out, _ := exec.Command("tmux", "capture-pane", "-pet", target).Output()
	if len(out) == 0 {
		time.Sleep(50 * time.Millisecond)
		out, _ = exec.Command("tmux", "capture-pane", "-pet", target).Output()
	}
	return string(out)
}

// SessionSummary returns window and attached-client counts for a session.
func SessionSummary(name string) (windows, clients int) {
	if out, err := exec.Command("tmux", "list-windows", "-t", "="+name).Output(); err == nil {
		windows = countLines(string(out))
	}
	if out, err := exec.Command("tmux", "list-clients", "-t", "="+name).Output(); err == nil {
		clients = countLines(string(out))
	}
	return
}

func countLines(s string) int {
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}
