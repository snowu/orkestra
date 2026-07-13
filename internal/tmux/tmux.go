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

// NewDetached starts a detached session running cmd; the session dies on
// its own when cmd exits. Used for transient work whose output the TUI
// tails via CapturePane (e.g. end-task cleanup).
func NewDetached(name, cmd string) error {
	return exec.Command("tmux", "new-session", "-d", "-s", name, cmd).Run()
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

// cdSession sends a cd command to a shell pane of an existing session so
// reused ("shared") sessions land in the target worktree dir instead of
// wherever the pane's cwd happened to be from creation.
//
// It scans the session's panes for one actually sitting at a shell prompt
// and targets that pane id directly — NOT the session's active pane:
// send-keys types blindly, so with vim/claude in the foreground the "cd"
// would be pasted straight into that program (and Enter submitted). It
// also can't assume the active pane is even relevant — when ork itself is
// launched from a tmux keybind (`bind o new-window ork`), the active pane
// IS the ork window, which closes right after. No shell pane → do nothing.
func cdSession(name, dir string) {
	out, _ := exec.Command("tmux", "list-panes", "-s", "-t", "="+name, "-F", "#{pane_id}\t#{pane_current_command}").Output()
	for _, line := range strings.Split(string(out), "\n") {
		id, cmd, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		switch cmd {
		case "zsh", "bash", "fish", "sh", "dash", "ksh":
			exec.Command("tmux", "send-keys", "-t", id, "cd "+shellQuote(dir), "Enter").Run()
			return
		}
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// SendKeys types keys into target (pane id or session), verbatim.
func SendKeys(target, keys string) {
	exec.Command("tmux", "send-keys", "-t", target, keys).Run()
}

// EnsureSession creates a detached session named name rooted at dir if it
// doesn't already exist, running the shell's default command.
func EnsureSession(name, dir string) error {
	if HasSession(name) {
		return nil
	}
	return exec.Command("tmux", "new", "-d", "-s", name, "-c", dir).Run()
}

// EnsureWindow makes sure session has a window named window running cmd
// rooted at dir; a no-op if that window already exists.
//
// cmd is sent via send-keys to the window's own interactive shell rather
// than passed as the tmux new-window command: rund/bund-style commands are
// typically shell aliases/functions from .bashrc/.zshrc, which a
// non-interactive `sh -c cmd` won't have sourced — the pane would exit
// instantly with "command not found" and the window would vanish before
// anyone saw it.
func EnsureWindow(session, window, dir, cmd string) error {
	for _, w := range SessionWindowNames(session) {
		if w == window {
			return nil
		}
	}
	target := session + ":" + window
	if err := exec.Command("tmux", "new-window", "-d", "-t", session, "-n", window, "-c", dir).Run(); err != nil {
		return err
	}
	return exec.Command("tmux", "send-keys", "-t", target, cmd, "Enter").Run()
}

// SessionWindowNames lists window names for session; empty if the session
// doesn't exist.
func SessionWindowNames(session string) []string {
	out, err := exec.Command("tmux", "list-windows", "-t", "="+session, "-F", "#{window_name}").Output()
	if err != nil {
		return nil
	}
	var names []string
	for _, w := range strings.Split(string(out), "\n") {
		if w = strings.TrimSpace(w); w != "" {
			names = append(names, w)
		}
	}
	return names
}

// WindowInfo identifies a window by its unique id (@N) — names are NOT
// unique (two "zsh" windows are common), and name-based targets silently
// resolve to the first match, which made side-by-side previews replicate
// one window into every column.
type WindowInfo struct {
	ID, Name, Cmd string
}

// SessionWindows lists session's windows with their active pane command.
func SessionWindows(session string) []WindowInfo {
	out, err := exec.Command("tmux", "list-windows", "-t", "="+session, "-F", "#{window_id}\t#{window_name}\t#{pane_current_command}").Output()
	if err != nil {
		return nil
	}
	var wins []WindowInfo
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.SplitN(strings.TrimSpace(l), "\t", 3)
		if len(f) == 3 && f[0] != "" {
			wins = append(wins, WindowInfo{ID: f[0], Name: f[1], Cmd: f[2]})
		}
	}
	return wins
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
