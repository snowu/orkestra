package worktree

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"orkestra/internal/config"
)

// planFixture: web (fe) + api (be) worktrees for "task" under one root.
func planFixture(t *testing.T, fe, be config.Process) (config.Config, config.Group, string, string) {
	t.Helper()
	withFakeMux(t)
	root := t.TempDir()
	feDir, beDir := filepath.Join(root, "web/task"), filepath.Join(root, "api/task")
	os.MkdirAll(feDir, 0o755)
	os.MkdirAll(beDir, 0o755)
	fe.Label, fe.Repo, be.Label, be.Repo = "fe", "web", "be", "api"
	g := config.Group{Name: "g", Processes: []config.Process{fe, be}}
	return config.Config{WorktreeRoots: []string{root}, Groups: []config.Group{g}}, g, feDir, beDir
}

func TestPlanGroupResolvesPortsEnvAndCmds(t *testing.T) {
	cfg, g, feDir, beDir := planFixture(t,
		config.Process{Cmd: "web {port} {be_port}", PortRange: "fe", EnvVar: "API", EnvPath: "/v1", EnvFile: ".env"},
		config.Process{Cmd: "api {port} {fe_port}", PortRange: "be"})
	fePort, bePort := TaskPorts("task")
	plan, err := PlanGroup(cfg, g, "api", "task", beDir)
	if err != nil {
		t.Fatal(err)
	}
	fe, be := plan.Procs[0], plan.Procs[1]
	if fe.Dir != feDir || fe.Cmd != "web "+strconv.Itoa(fePort)+" "+strconv.Itoa(bePort) {
		t.Errorf("fe plan = %+v", fe)
	}
	if be.Cmd != "api "+strconv.Itoa(bePort)+" "+strconv.Itoa(fePort) {
		t.Errorf("be plan = %+v", be)
	}
	if fe.EnvFile != ".env" || fe.Env["API"] != "http://localhost:"+strconv.Itoa(bePort)+"/v1" || fe.Env["NEXT_PUBLIC_ORK_TASK"] != "task" {
		t.Errorf("fe env = %+v", fe.Env)
	}
	if len(be.Env) != 0 {
		t.Errorf("be must get no env updates, got %+v", be.Env)
	}
	if _, err := os.Stat(filepath.Join(feDir, ".env")); !os.IsNotExist(err) {
		t.Fatal("planning wrote the env file")
	}
	if _, _, err := ApplyGroupPlan(plan, beDir); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(feDir, ".env")); !strings.Contains(string(data), "API=http://localhost:") {
		t.Fatalf("env after apply = %q", data)
	}
}

func TestDisabledTaskLabelDoesNotCreateEnv(t *testing.T) {
	empty := ""
	cfg, g, feDir, _ := planFixture(t, config.Process{Cmd: "x", PortRange: "fe", TaskEnvVar: &empty}, config.Process{Cmd: "y"})
	if _, _, err := EnsureGroupWindowsFor(cfg, g, "web", "task", feDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(feDir, ".env.local")); !os.IsNotExist(err) {
		t.Fatal("disabled task label created env file")
	}
}

func TestPatchEnvPreservesSettingsAndMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	os.WriteFile(path, []byte("# comment\nSECRET=keep\n export API = old\nAPI=duplicate\n"), 0o640)
	values := map[string]string{"API": "http://localhost:8123", "TASK": "fix #42"}
	for i := 0; i < 2; i++ {
		if err := patchEnvFile(dir, ".env", values); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(path)
	want := "# comment\nSECRET=keep\nAPI=http://localhost:8123\nTASK=\"fix #42\"\n"
	if string(data) != want {
		t.Fatalf("env = %q", data)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v", st.Mode())
	}
}

func TestPatchEnvFailureLeavesOriginal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	original := "API=old\n" + strings.Repeat("x", 70000)
	os.WriteFile(path, []byte(original), 0o600)
	if err := patchEnvFile(dir, ".env", map[string]string{"API": "new"}); err == nil {
		t.Fatal("expected scan failure")
	}
	data, _ := os.ReadFile(path)
	if string(data) != original {
		t.Fatal("failed patch changed file")
	}
}

func TestPatchEnvFollowsSymlinkedFile(t *testing.T) {
	// Setup hooks often symlink .env.local to the main checkout's copy:
	// the update must land in the target and keep the link a link.
	dir, main := t.TempDir(), t.TempDir()
	target := filepath.Join(main, ".env.local")
	os.WriteFile(target, []byte("KEEP=1\nAPI=old\n"), 0o640)
	link := filepath.Join(dir, ".env.local")
	os.Symlink(target, link)
	if err := patchEnvFile(dir, ".env.local", map[string]string{"API": "new"}); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was replaced by a file")
	}
	if data, _ := os.ReadFile(target); string(data) != "KEEP=1\nAPI=new\n" {
		t.Fatalf("target = %q", data)
	}
}

func TestPatchEnvRejectsSymlinkedParentEscape(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	path := filepath.Join(outside, "secret")
	os.WriteFile(path, []byte("API=old\n"), 0o600)
	os.Symlink(outside, filepath.Join(dir, "config"))
	if err := patchEnvFile(dir, "config/secret", map[string]string{"API": "new"}); err == nil {
		t.Fatal("parent symlink accepted")
	}
	if data, _ := os.ReadFile(path); string(data) != "API=old\n" {
		t.Fatal("outside file changed")
	}
}

func TestTemplatePatchPlanAndLaunch(t *testing.T) {
	cfg, g, feDir, beDir := planFixture(t,
		config.Process{Cmd: "x", PortRange: "fe", PatchFile: "config.pkl", PatchKey: "backend"},
		config.Process{Cmd: "y", PortRange: "be"})
	path := filepath.Join(feDir, "config.pkl")
	original := "# keep\n  backend = \"http://localhost:8000/v1\"\nother = \"http://localhost:8000\"\n"
	os.WriteFile(path, []byte(original), 0o644)
	plan, err := PlanGroup(cfg, g, "web", "task", feDir)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != original {
		t.Fatal("planning changed template")
	}
	if _, _, err := EnsureGroupWindowsFor(cfg, g, "api", "task", beDir); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != string(plan.Procs[0].patchData) || strings.Contains(string(data), `backend = "http://localhost:8000/`) || !strings.Contains(string(data), `other = "http://localhost:8000"`) {
		t.Fatalf("template = %s", data)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v", st.Mode())
	}
	// A bad template fails at planning, before any env file is touched.
	os.WriteFile(path, []byte("backend = \"https://example.test\"\n"), 0o644)
	os.WriteFile(filepath.Join(feDir, ".env.local"), []byte("KEEP=1\n"), 0o600)
	if _, _, err := EnsureGroupWindowsFor(cfg, g, "web", "task", feDir); err == nil {
		t.Fatal("missing localhost URL accepted")
	}
	if data, _ := os.ReadFile(filepath.Join(feDir, ".env.local")); string(data) != "KEEP=1\n" {
		t.Fatal("failed template patch changed environment")
	}
}
