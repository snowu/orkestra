package mux

import (
	"encoding/json"
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
