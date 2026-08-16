// herdr backend: shells out to the herdr CLI (https://herdr.dev), which
// wraps its local socket API. Concept mapping — ork "session" = herdr
// workspace (matched by label, all inside herdr's default session), ork
// "window" = herdr tab, capture/send targets = herdr pane ids.
package mux

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type herdrBackend struct{}

func (herdrBackend) Binary() string { return "herdr" }

// herdrResult unwraps a herdr CLI response body: {"id":..,"result":..} on
// success, {"id":..,"error":{"code":..,"message":..}} on failure.
//
// The error envelope is why this is not just a json.Unmarshal: herdr exits
// 0 even when it reports an error (a dead server answers
// `workspace list` with exit 0 and error.code=server_not_running), so
// exec's error is not a reliable failure signal and an unchecked envelope
// gets mistaken for a result — the caller then sees an empty payload and
// can only report "failed" with no cause.
func herdrResult(out []byte) (any, error) {
	var v any
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, fmt.Errorf("unparseable output: %s", truncate(strings.TrimSpace(string(out)), 200))
	}
	if m, ok := v.(map[string]any); ok {
		if e := jSub(m, "error"); e != nil {
			return nil, herdrAPIErr{Code: jStr(e, "code"), Message: jStr(e, "message")}
		}
		if r, ok := m["result"]; ok {
			return r, nil
		}
	}
	return v, nil
}

// herdrAPIErr is a decoded error envelope. The code is kept structured
// (not folded into the message) so callers can react to specific ones —
// server_not_running is the code that triggers the autostart retry.
type herdrAPIErr struct{ Code, Message string }

func (e herdrAPIErr) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return e.Message + " (" + e.Code + ")"
}

func isServerDown(err error) bool {
	var ae herdrAPIErr
	return errors.As(err, &ae) && ae.Code == "server_not_running"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// herdrRun runs a herdr CLI command once and returns the unwrapped result,
// or an error naming both the command and herdr's own message (%w keeps
// herdrAPIErr's code inspectable through the wrapping).
func herdrRun(args ...string) (any, error) {
	out, err := exec.Command("herdr", args...).CombinedOutput()
	v, perr := herdrResult(out)
	if perr != nil {
		return nil, fmt.Errorf("herdr %s: %w", strings.Join(args, " "), perr)
	}
	if err != nil {
		return nil, fmt.Errorf("herdr %s: %w", strings.Join(args, " "), err)
	}
	return v, nil
}

// serverStarting guards the autostart so a burst of failing calls (a
// refresh fans out one process-info per pane) spawns one server, not one
// per call.
var serverStarting sync.Mutex

// startHerdrServer launches the headless server and waits for the API
// socket to answer. Unlike tmux — where any command autostarts the server —
// herdr's socket must already exist, so without this every ork command
// fails until the user manually runs `herdr`. The saved session.json is
// restored by the server itself, so previous workspaces come back.
func startHerdrServer() error {
	serverStarting.Lock()
	defer serverStarting.Unlock()

	// Another goroutine may have started it while we waited on the lock.
	if _, err := herdrRun("workspace", "list"); err == nil {
		return nil
	}
	// Detached: `herdr server` runs in the foreground until stopped, so it
	// must outlive this process rather than be waited on.
	c := exec.Command("herdr", "server")
	c.Stdin, c.Stdout, c.Stderr = nil, nil, nil
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return err
	}
	go c.Wait() // reap; the process is expected to outlive us

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := herdrRun("workspace", "list"); err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("herdr server did not come up within 5s")
}

// herdrCall is herdrRun plus a one-shot autostart: a dead server is a
// recoverable condition, not a user-facing failure.
func herdrCall(args ...string) (any, error) {
	v, err := herdrRun(args...)
	if err == nil || !isServerDown(err) {
		return v, err
	}
	if serr := startHerdrServer(); serr != nil {
		return nil, fmt.Errorf("%w; autostart failed: %v", err, serr)
	}
	return herdrRun(args...)
}

// herdrJSON is herdrCall for the read paths (workspace/tab/pane listing),
// which degrade to nil on failure exactly like the tmux backend does
// against a dead server.
func herdrJSON(args ...string) any {
	v, err := herdrCall(args...)
	if err != nil {
		return nil
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

// paneProc returns the command name and pid of the pane's foreground
// process, the equivalent of tmux's pane_current_command.
//
// herdr reports foreground_processes as a list, and an idle shell does
// NOT reliably report just itself: prompt hooks fork short-lived children
// (oh-my-zsh's git status is the common one), so sampling an idle pane
// returns [zsh] most of the time and [git] for the few ms a hook is
// running. Taking the list's first or last entry therefore makes Cmd flap
// between refreshes, which misreports the AGENT column and — worse —
// makes cdWorkspace's isShell check miss on an unlucky sample, silently
// skipping the cd into the task's worktree.
//
// shell_pid is the pane's own stable shell, so a process matching it is
// the steady state: prefer it, and only fall back to the group leader for
// panes genuinely running something else (a dev server, an agent).
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
	if shellPID := jInt(m, "shell_pid"); shellPID != 0 {
		for _, p := range procs {
			if jInt(p, "pid") == shellPID {
				return jStr(p, "name", "command"), shellPID
			}
		}
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
// herdr's own message is propagated — "workspace create X failed" alone
// hides the causes users actually hit (no server running, dir gone).
func createWorkspace(name, dir string) (wsID, paneID string, err error) {
	args := []string{"workspace", "create", "--label", name}
	if dir != "" {
		args = append(args, "--cwd", dir)
	}
	v, err := herdrCall(args...)
	if err != nil {
		return "", "", err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return "", "", fmt.Errorf("herdr workspace create %s: unexpected response shape", name)
	}
	wsID = jStr(jSub(m, "workspace"), "workspace_id", "id")
	paneID = jStr(jSub(m, "root_pane"), "pane_id", "id")
	if wsID == "" {
		return "", "", fmt.Errorf("herdr workspace create %s: no workspace id in response", name)
	}
	return wsID, paneID, nil
}

// NewDetached: herdr workspaces are server-side, so "detached" is the
// natural state — create the workspace and run cmd in its root pane. A
// herdr workspace does NOT self-destruct when cmd exits (unlike a tmux
// new-session command), but callers rely on that (the end-task tail
// polls HasSession to know cleanup finished), so the workspace closes
// itself after cmd via the HERDR_WORKSPACE_ID herdr sets in the pane.
func (herdrBackend) NewDetached(name, cmd string) error {
	_, paneID, err := createWorkspace(name, "")
	if err != nil {
		return err
	}
	if paneID == "" {
		return fmt.Errorf("herdr workspace create %s: no root pane in response", name)
	}
	cmd += `; herdr workspace close "$HERDR_WORKSPACE_ID"`
	_, err = herdrCall("pane", "run", paneID, cmd)
	return err
}

func (h herdrBackend) EnsureSession(name, dir string) error {
	if h.HasSession(name) {
		return nil
	}
	_, _, err := createWorkspace(name, dir)
	return err
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
	v, err := herdrCall("tab", "create", "--workspace", wsID, "--cwd", dir, "--label", window)
	if err != nil {
		return err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("herdr tab create %s: unexpected response shape", window)
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
		var (
			paneID string
			err    error
		)
		if wsID, paneID, err = createWorkspace(name, dir); err != nil {
			return err
		}
		if paneID == "" {
			return fmt.Errorf("herdr workspace create %s: no root pane in response", name)
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
