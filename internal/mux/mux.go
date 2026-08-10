// Package mux abstracts the terminal-multiplexer backend (tmux or
// herdr) behind one function surface. The backend is forced by
// ORK_MULTIPLEXER — Select errors rather than falling back, so ork
// always uses exactly what the user chose at install time.
package mux

import (
	"fmt"
	"os/exec"
)

type Pane struct {
	Session string // session name (herdr: workspace label)
	Target  string // tmux: session:window.pane; herdr: pane_id
	CWD     string
	Cmd     string
	PID     int
}

// WindowInfo identifies a window by a unique id usable as a capture
// target (tmux: @N window id; herdr: the tab's pane_id) — names are NOT
// unique (two "zsh" windows are common), and name-based targets silently
// resolve to the first match.
type WindowInfo struct {
	ID, Name, Cmd string
}

type Backend interface {
	Binary() string
	ListPanes() []Pane
	HasSession(name string) bool
	KillSession(name string)
	NewDetached(name, cmd string) error
	NewOrAttach(name, dir string) error
	EnsureSession(name, dir string) error
	EnsureWindow(session, window, dir, cmd string) error
	SessionWindowNames(session string) []string
	SessionWindows(session string) []WindowInfo
	CapturePane(target string) string
	SendKeys(target, keys string)
	CurrentWindow() (session, windowID string)
	EvacuateWindow(windowID, dst string) error
	Inside() bool
	SessionSummary(name string) (windows, clients int)
}

var active Backend = tmuxBackend{}

// Select installs the backend named in ORK_MULTIPLEXER. The choice is
// forced: an unknown name or a missing binary is an error, never a
// silent fallback to the other multiplexer.
func Select(name string) error {
	var b Backend
	switch name {
	case "", "tmux":
		b = tmuxBackend{}
	case "herdr":
		b = herdrBackend{}
	default:
		return fmt.Errorf("unknown ORK_MULTIPLEXER %q (want tmux or herdr)", name)
	}
	if _, err := exec.LookPath(b.Binary()); err != nil {
		return fmt.Errorf("%s not installed — required (ORK_MULTIPLEXER=%s)", b.Binary(), b.Binary())
	}
	active = b
	return nil
}

// Binary returns the active backend's executable name (for messages).
func Binary() string { return active.Binary() }

func ListPanes() []Pane                          { return active.ListPanes() }
func HasSession(name string) bool                { return active.HasSession(name) }
func KillSession(name string)                    { active.KillSession(name) }
func NewDetached(name, cmd string) error         { return active.NewDetached(name, cmd) }
func NewOrAttach(name, dir string) error         { return active.NewOrAttach(name, dir) }
func EnsureSession(name, dir string) error       { return active.EnsureSession(name, dir) }
func EnsureWindow(s, w, dir, cmd string) error   { return active.EnsureWindow(s, w, dir, cmd) }
func SessionWindowNames(session string) []string { return active.SessionWindowNames(session) }
func SessionWindows(session string) []WindowInfo { return active.SessionWindows(session) }
func CapturePane(target string) string           { return active.CapturePane(target) }
func SendKeys(target, keys string)               { active.SendKeys(target, keys) }
func CurrentWindow() (string, string)            { return active.CurrentWindow() }
func EvacuateWindow(windowID, dst string) error  { return active.EvacuateWindow(windowID, dst) }
func Inside() bool                               { return active.Inside() }
func SessionSummary(name string) (int, int)      { return active.SessionSummary(name) }
