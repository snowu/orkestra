package mux

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// herdr exits 0 while reporting an error in the body, so the envelope is
// the only failure signal — swallowing it made every failure surface as an
// opaque "workspace create X failed".
func TestHerdrResultEnvelope(t *testing.T) {
	_, err := herdrResult([]byte(`{"id":"cli:workspace:list","error":{"code":"server_not_running","message":"no herdr server is running at /x/herdr.sock"}}`))
	if err == nil {
		t.Fatal("error envelope must produce an error")
	}
	if got := err.Error(); got != "no herdr server is running at /x/herdr.sock (server_not_running)" {
		t.Errorf("message should carry herdr's own text and code, got %q", got)
	}

	v, err := herdrResult([]byte(`{"id":"cli:workspace:create","result":{"workspace":{"workspace_id":"w1"}}}`))
	if err != nil {
		t.Fatalf("success envelope: %v", err)
	}
	if jStr(jSub(v.(map[string]any), "workspace"), "workspace_id", "id") != "w1" {
		t.Errorf("result should be unwrapped, got %+v", v)
	}

	// Unenveloped payloads (bare arrays) still pass through.
	if v, err := herdrResult([]byte(`[{"pane_id":"p1"}]`)); err != nil || len(jList(v, "panes")) != 1 {
		t.Errorf("bare array: %+v %v", v, err)
	}

	if _, err := herdrResult([]byte("herdr: command not found")); err == nil {
		t.Error("non-JSON output must error")
	}
}

// The autostart path keys off the code surviving herdrRun's wrapping.
func TestIsServerDown(t *testing.T) {
	_, err := herdrResult([]byte(`{"error":{"code":"server_not_running","message":"no server"}}`))
	if !isServerDown(err) {
		t.Error("bare envelope error should be recognized")
	}
	if !isServerDown(fmt.Errorf("herdr workspace create x: %w", err)) {
		t.Error("code must survive %w wrapping")
	}
	_, other := herdrResult([]byte(`{"error":{"code":"not_found","message":"no such workspace"}}`))
	if isServerDown(other) || isServerDown(errors.New("plain")) {
		t.Error("only server_not_running triggers autostart")
	}
}

func TestJListShapes(t *testing.T) {
	// Keyed under the plural noun (the documented CLI response shape).
	v := decode(t, `{"workspaces":[{"workspace_id":"w1","label":"mytask"},{"workspace_id":"w2","label":"other"}]}`)
	ws := jList(v, "workspaces")
	if len(ws) != 2 || jStr(ws[0], "workspace_id", "id") != "w1" || jStr(ws[1], "label") != "other" {
		t.Errorf("keyed shape: %+v", ws)
	}
	// Bare top-level array.
	ws = jList(decode(t, `[{"pane_id":"p1"}]`), "panes")
	if len(ws) != 1 || jStr(ws[0], "pane_id", "id") != "p1" {
		t.Errorf("bare array shape: %+v", ws)
	}
	// Garbage degrades to empty, not panic.
	if jList(nil, "panes") != nil || jList(decode(t, `{"panes":"nope"}`), "panes") != nil {
		t.Error("bad input should yield nil")
	}
}

func TestJFieldHelpers(t *testing.T) {
	m := decode(t, `{"pane_id":"p9","cwd":"/tmp","foreground_cwd":"/home/u/wt","process_info":{"foreground_processes":[{"name":"zsh","pid":100},{"name":"node","pid":123}],"shell_pid":100}}`).(map[string]any)
	if jStr(m, "foreground_cwd", "cwd") != "/home/u/wt" {
		t.Error("foreground_cwd should win over cwd")
	}
	if jStr(m, "missing", "cwd") != "/tmp" {
		t.Error("fallback key")
	}
	// Group leader wins (matches tmux pane_current_command) — the tail of
	// foreground_processes can be a transient prompt-hook child.
	procs := jList(jSub(m, "process_info")["foreground_processes"], "foreground_processes")
	if jStr(procs[0], "name", "command") != "zsh" || jInt(procs[0], "pid") != 100 {
		t.Errorf("process fields: %+v", procs[0])
	}
	if jSub(m, "nope") != nil || jInt(m, "nope") != 0 {
		t.Error("absent keys should zero out")
	}
}
