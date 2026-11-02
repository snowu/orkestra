package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpsertGroupKeepsOtherEntriesVerbatim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "groups.json")
	orig := `[
  {"name":"a","processes":[{"label":"x","repo":"r1","cmd":"run"}]},
  {"name":"b","processes":[{"label":"y","repo":"r2","cmd":"go"}]}
]`
	os.WriteFile(path, []byte(orig), 0o600)

	off := ""
	replaced, err := UpsertGroup(path, Group{Name: "b", Processes: []Process{{Label: "fe", Repo: "web", Cmd: "dev --port {port}", PortRange: "fe", TaskEnvVar: &off}}})
	if err != nil || !replaced {
		t.Fatalf("replaced=%v err=%v", replaced, err)
	}
	data, _ := os.ReadFile(path)
	s := string(data)
	if !strings.Contains(s, `{"name":"a","processes":[{"label":"x","repo":"r1","cmd":"run"}]}`) {
		t.Fatalf("untouched entry reformatted:\n%s", s)
	}
	if strings.Contains(s, "fixed_port") || !strings.Contains(s, `"task_env_var": ""`) {
		t.Fatalf("encoding: want unset keys omitted and explicit empty task_env_var kept:\n%s", s)
	}
	var gs []Group
	if err := json.Unmarshal(data, &gs); err != nil || len(gs) != 2 || gs[1].Processes[0].Label != "fe" {
		t.Fatalf("result %v %v", gs, err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	if bak, _ := os.ReadFile(path + ".bak"); string(bak) != orig {
		t.Fatal("backup missing")
	}

	if _, err := UpsertGroup(path, Group{Name: "c", Processes: []Process{{Label: "z", Repo: "r", Cmd: "x {port}"}}}); err == nil {
		t.Fatal("invalid group ({port} without port_range) written")
	}
	if err := DeleteGroup(path, "a"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if err := json.Unmarshal(data, &gs); err != nil || len(gs) != 1 || gs[0].Name != "b" {
		t.Fatalf("after delete %v %v", gs, err)
	}
	if DeleteGroup(path, "nope") == nil {
		t.Fatal("deleting a missing group should fail")
	}
}

func TestUpsertGroupCreatesFileAndRefusesBrokenOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "groups.json")
	if _, err := UpsertGroup(path, Group{Name: "g", Processes: []Process{{Label: "a", Repo: "r", Cmd: "c"}}}); err != nil {
		t.Fatal(err)
	}
	var gs []Group
	data, _ := os.ReadFile(path)
	if json.Unmarshal(data, &gs) != nil || len(gs) != 1 {
		t.Fatalf("new file: %s", data)
	}
	os.WriteFile(path, []byte("[{broken"), 0o644)
	if _, err := UpsertGroup(path, Group{Name: "g", Processes: []Process{{Label: "a", Repo: "r", Cmd: "c"}}}); err == nil {
		t.Fatal("overwrote an unparseable groups file")
	}
}

func TestReplaceFileWritesThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.json")
	link := filepath.Join(dir, "link.json")
	os.WriteFile(real, []byte("{}"), 0o644)
	os.Symlink(real, link)
	if err := SetRepoHook(link, "web", "bun install"); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced by a file")
	}
	h, err := ReadHooks(real)
	if err != nil || h["web"] != "bun install" {
		t.Fatalf("hooks %v %v", h, err)
	}
	if err := SetRepoHook(link, "web", ""); err != nil {
		t.Fatal(err)
	}
	if h, _ := ReadHooks(real); len(h) != 0 {
		t.Fatalf("hook not removed: %v", h)
	}
	if SetRepoHook(link, "../x", "y") == nil {
		t.Fatal("path-like repo accepted")
	}
}
