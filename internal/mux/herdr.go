// herdr backend: shells out to the herdr CLI (https://herdr.dev), which
// wraps its local socket API. Concept mapping — ork "session" = herdr
// workspace (matched by label, all inside herdr's default session), ork
// "window" = herdr tab, capture/send targets = herdr pane ids.
package mux

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

type herdrBackend struct{}

func (herdrBackend) Binary() string { return "herdr" }

// herdrJSON runs a herdr CLI command (responses are JSON by default) and
// returns the decoded payload, unwrapped from the {"id":..,"result":..}
// envelope. Any exec/parse failure returns nil — callers degrade to the
// same "no session/pane" behavior the tmux backend has on a dead server.
func herdrJSON(args ...string) any {
	out, err := exec.Command("herdr", args...).Output()
	if err != nil {
		return nil
	}
	var v any
	if json.Unmarshal(out, &v) != nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		if r, ok := m["result"]; ok {
			return r
		}
	}
	return v
}

// jList digs a []map out of v: either v itself is the array, or it lives
// under one of keys (herdr responses key lists by their plural noun).
func jList(v any, keys ...string) []map[string]any {
	if m, ok := v.(map[string]any); ok {
		for _, k := range keys {
			if inner, ok := m[k]; ok {
				v = inner
				break
			}
		}
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []map[string]any
	for _, e := range arr {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// jStr returns the first present string among keys ("" if none).
func jStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func jInt(m map[string]any, keys ...string) int {
	for _, k := range keys {
		if f, ok := m[k].(float64); ok {
			return int(f)
		}
	}
	return 0
}

// jSub returns m[key] as a map (nil if absent/not a map).
func jSub(m map[string]any, key string) map[string]any {
	sub, _ := m[key].(map[string]any)
	return sub
}

func herdrWorkspaces() []map[string]any {
	return jList(herdrJSON("workspace", "list"), "workspaces")
}

// workspaceID resolves an ork session name to a herdr workspace id by
// label ("" when no such workspace).
func workspaceID(name string) string {
	for _, w := range herdrWorkspaces() {
		if jStr(w, "label") == name {
			return jStr(w, "workspace_id", "id")
		}
	}
	return ""
}

func herdrTabs(wsID string) []map[string]any {
	return jList(herdrJSON("tab", "list", "--workspace", wsID), "tabs")
}

func herdrPanes(wsID string) []map[string]any {
	return jList(herdrJSON("pane", "list", "--workspace", wsID), "panes")
}

// paneProc returns the foreground command name and pid for a pane — the
// FIRST entry of foreground_processes (the group leader, matching tmux's
// pane_current_command). Not the last: the list includes short-lived
// children (an idle zsh briefly reports [zsh, mkdir] while a prompt hook
// runs), which would misreport what the pane is running and break the
// shell check cdWorkspace relies on.
func paneProc(paneID string) (cmd string, pid int) {
	v := herdrJSON("pane", "process-info", "--pane", paneID)
	m, ok := v.(map[string]any)
	if !ok {
		return "", 0
	}
	if p := jSub(m, "process_info"); p != nil {
		m = p
	}
	procs := jList(m["foreground_processes"], "foreground_processes")
	if len(procs) == 0 {
		return "", 0
	}
	return jStr(procs[0], "name", "command"), jInt(procs[0], "pid")
}

func (herdrBackend) HasSession(name string) bool { return workspaceID(name) != "" }

func (herdrBackend) KillSession(name string) {
	if id := workspaceID(name); id != "" {
		exec.Command("herdr", "workspace", "close", id).Run()
	}
}

// createWorkspace makes a labeled workspace rooted at dir and returns its
// workspace id and root pane id (creation returns both in one response).
func createWorkspace(name, dir string) (wsID, paneID string) {
	args := []string{"workspace", "create", "--label", name}
	if dir != "" {
		args = append(args, "--cwd", dir)
	}
	v := herdrJSON(args...)
	m, ok := v.(map[string]any)
	if !ok {
		return "", ""
	}
	wsID = jStr(jSub(m, "workspace"), "workspace_id", "id")
	paneID = jStr(jSub(m, "root_pane"), "pane_id", "id")
	return wsID, paneID
}

// NewDetached: herdr workspaces are server-side, so "detached" is the
// natural state — create the workspace and run cmd in its root pane. A
// herdr workspace does NOT self-destruct when cmd exits (unlike a tmux
// new-session command), but callers rely on that (the end-task tail
// polls HasSession to know cleanup finished), so the workspace closes
// itself after cmd via the HERDR_WORKSPACE_ID herdr sets in the pane.
func (herdrBackend) NewDetached(name, cmd string) error {
	_, paneID := createWorkspace(name, "")
	if paneID == "" {
		return errHerdr("workspace create " + name)
	}
	cmd += `; herdr workspace close "$HERDR_WORKSPACE_ID"`
	return exec.Command("herdr", "pane", "run", paneID, cmd).Run()
}

func (h herdrBackend) EnsureSession(name, dir string) error {
	if h.HasSession(name) {
		return nil
	}
	if ws, _ := createWorkspace(name, dir); ws == "" {
		return errHerdr("workspace create " + name)
	}
	return nil
}

// EnsureWindow: tab labeled window inside the workspace, running cmd via
// `pane run` (the tab's own interactive shell — same aliases/functions
// rationale as the tmux backend's send-keys approach).
func (h herdrBackend) EnsureWindow(session, window, dir, cmd string) error {
	if err := h.EnsureSession(session, dir); err != nil {
		return err
	}
	wsID := workspaceID(session)
	for _, t := range herdrTabs(wsID) {
		if jStr(t, "label") == window {
			return nil
		}
	}
	v := herdrJSON("tab", "create", "--workspace", wsID, "--cwd", dir, "--label", window)
	m, ok := v.(map[string]any)
	if !ok {
		return errHerdr("tab create " + window)
	}
	paneID := jStr(jSub(m, "root_pane"), "pane_id", "id")
	if paneID == "" {
		// Creation response without the pane — find it via the pane list.
		tabID := jStr(jSub(m, "tab"), "tab_id", "id")
		for _, p := range herdrPanes(wsID) {
			if jStr(p, "tab_id") == tabID {
				paneID = jStr(p, "pane_id", "id")
				break
			}
		}
	}
	if paneID == "" {
		return errHerdr("no pane for tab " + window)
	}
	return exec.Command("herdr", "pane", "run", paneID, cmd).Run()
}

func (herdrBackend) SessionWindowNames(session string) []string {
	wsID := workspaceID(session)
	if wsID == "" {
		return nil
	}
	var names []string
	for _, t := range herdrTabs(wsID) {
		if l := jStr(t, "label"); l != "" {
			names = append(names, l)
		}
	}
	return names
}

// SessionWindows maps each tab to its first pane — WindowInfo.ID must be
// a capture target, and herdr captures address panes, not tabs.
func (herdrBackend) SessionWindows(session string) []WindowInfo {
	wsID := workspaceID(session)
	if wsID == "" {
		return nil
	}
	tabLabel := map[string]string{}
	for _, t := range herdrTabs(wsID) {
		tabLabel[jStr(t, "tab_id", "id")] = jStr(t, "label")
	}
	seen := map[string]bool{}
	var wins []WindowInfo
	for _, p := range herdrPanes(wsID) {
		tabID := jStr(p, "tab_id")
		if seen[tabID] {
			continue
		}
		seen[tabID] = true
		paneID := jStr(p, "pane_id", "id")
		cmd, _ := paneProc(paneID)
		wins = append(wins, WindowInfo{ID: paneID, Name: tabLabel[tabID], Cmd: cmd})
	}
	return wins
}

func (herdrBackend) ListPanes() []Pane {
	var panes []Pane
	for _, w := range herdrWorkspaces() {
		label := jStr(w, "label")
		for _, p := range herdrPanes(jStr(w, "workspace_id", "id")) {
			paneID := jStr(p, "pane_id", "id")
			cmd, pid := paneProc(paneID)
			panes = append(panes, Pane{
				Session: label,
				Target:  paneID,
				CWD:     jStr(p, "foreground_cwd", "cwd"),
				Cmd:     cmd,
				PID:     pid,
			})
		}
	}
	return panes
}

// CapturePane returns the pane's recent content. Two herdr quirks both
// break the preview if ignored:
//   - source must be recent-unwrapped, NOT visible: a workspace that was
//     never focused in a client has an unrendered (near-empty) visible
//     buffer, and plain "recent" comes back hard-wrapped at whatever tiny
//     width the unrendered pane defaulted to. recent-unwrapped returns
//     logical lines regardless of render state; the preview truncates
//     long lines ANSI-aware anyway.
//   - herdr emits CRLF (tmux emits bare \n) — the \r must go: a preview
//     cell containing one makes the terminal cursor jump to column 0
//     mid-frame, shearing the whole TUI layout.
func (herdrBackend) CapturePane(target string) string {
	out, _ := exec.Command("herdr", "pane", "read", target, "--source", "recent-unwrapped", "--format", "ansi").Output()
	return strings.ReplaceAll(string(out), "\r", "")
}

func (herdrBackend) SendKeys(target, keys string) {
	exec.Command("herdr", "pane", "send-text", target, keys).Run()
}

func (herdrBackend) Inside() bool { return os.Getenv("HERDR_ENV") == "1" }

// CurrentWindow returns (workspace label, own pane id) — the pane id is
// what EvacuateWindow moves, playing the role tmux's window id plays.
func (h herdrBackend) CurrentWindow() (session, windowID string) {
	if !h.Inside() {
		return "", ""
	}
	wsID := os.Getenv("HERDR_WORKSPACE_ID")
	for _, w := range herdrWorkspaces() {
		if jStr(w, "workspace_id", "id") == wsID {
			return jStr(w, "label"), os.Getenv("HERDR_PANE_ID")
		}
	}
	return "", ""
}

// EvacuateWindow moves ork's own pane out of a doomed workspace into dst
// (new tab of dst if it exists, else a fresh workspace labeled dst) and
// focuses it so the user follows.
func (herdrBackend) EvacuateWindow(windowID, dst string) error {
	if dstID := workspaceID(dst); dstID != "" {
		if err := exec.Command("herdr", "pane", "move", windowID, "--new-tab", "--workspace", dstID).Run(); err != nil {
			return err
		}
		return exec.Command("herdr", "workspace", "focus", dstID).Run()
	}
	if err := exec.Command("herdr", "pane", "move", windowID, "--new-workspace", "--label", dst).Run(); err != nil {
		return err
	}
	if dstID := workspaceID(dst); dstID != "" {
		return exec.Command("herdr", "workspace", "focus", dstID).Run()
	}
	return nil
}

// NewOrAttach lands the user in the workspace labeled name rooted at dir.
// Inside herdr: focus is enough (the client is already attached). Outside:
// focus, then exec into `herdr session attach` — replaces this process and
// takes over the terminal, same shape as the tmux backend.
func (h herdrBackend) NewOrAttach(name, dir string) error {
	wsID := workspaceID(name)
	if wsID == "" {
		var paneID string
		if wsID, paneID = createWorkspace(name, dir); wsID == "" || paneID == "" {
			return errHerdr("workspace create " + name)
		}
	} else {
		cdWorkspace(wsID, dir)
	}
	if err := exec.Command("herdr", "workspace", "focus", wsID).Run(); err != nil {
		return err
	}
	if h.Inside() {
		return nil
	}
	herdrPath, err := exec.LookPath("herdr")
	if err != nil {
		return err
	}
	sess := os.Getenv("HERDR_SESSION")
	if sess == "" {
		sess = "default"
	}
	// The herdr client draws its UI on stdout — but ork usually runs under
	// ork.sh's `$(command ork ...)` command substitution, where stdout is a
	// captured pipe. Exec'ing the client with that stdout leaves it running
	// invisibly (looks like a hang). Rebind fd 1 to the controlling
	// terminal first; the attach path never prints the cd-target line, so
	// nothing is lost from the captured stdout.
	if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		syscall.Dup2(int(tty.Fd()), 1)
	}
	return syscall.Exec(herdrPath, []string{"herdr", "session", "attach", sess}, os.Environ())
}

// cdWorkspace: same intent as the tmux backend's cdSession — reused
// workspaces should land in the target worktree dir. Only panes whose
// foreground process is a shell get the cd (pane run types into the
// foreground program blindly, like send-keys).
func cdWorkspace(wsID, dir string) {
	for _, p := range herdrPanes(wsID) {
		paneID := jStr(p, "pane_id", "id")
		if cmd, _ := paneProc(paneID); isShell(cmd) {
			exec.Command("herdr", "pane", "run", paneID, "cd "+shellQuote(dir)).Run()
			return
		}
	}
}

// SessionSummary: tab count stands in for windows; herdr has no
// per-workspace client count, so clients is always 0.
func (herdrBackend) SessionSummary(name string) (windows, clients int) {
	if wsID := workspaceID(name); wsID != "" {
		windows = len(herdrTabs(wsID))
	}
	return windows, 0
}

type herdrErr string

func (e herdrErr) Error() string { return "herdr: " + string(e) + " failed" }

func errHerdr(op string) error { return herdrErr(op) }
