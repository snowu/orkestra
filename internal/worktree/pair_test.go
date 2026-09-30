package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orkestra/internal/config"
)

func TestPlanPairBothSidesAndMissingSibling(t *testing.T) {
	root := t.TempDir()
	fe, be := filepath.Join(root, "web/task"), filepath.Join(root, "api/task")
	os.MkdirAll(fe, 0o755)
	os.MkdirAll(be, 0o755)
	key := "VITE_TASK"
	cfg := config.Config{WorktreeRoots: []string{root}, Pairs: []config.Pair{{
		FERepo: "web", BERepo: "api", FECmd: "web --port {port} --api {be_port}", BECmd: "api --port {port} --web {fe_port}",
		FEEnvFile: ".env", TaskEnvVar: &key, FEEnvVar: "VITE_API_URL", FEEnvPath: "/v1", FEURLEnvVars: []string{"APP_URL"},
	}}}
	for _, repo := range []string{"web", "api"} {
		wt := fe
		if repo == "api" {
			wt = be
		}
		plan, err := PlanPair(cfg, repo, "task", wt)
		if err != nil {
			t.Fatal(err)
		}
		if plan.FEDir != fe || plan.BEDir != be || strings.Contains(plan.FECmd+plan.BECmd, "{") {
			t.Fatalf("plan = %+v", plan)
		}
		if plan.EnvFile != ".env" || plan.Env[key] != "task" || !strings.HasSuffix(plan.Env["VITE_API_URL"], "/v1") {
			t.Fatalf("env = %v", plan.Env)
		}
		if _, err := os.Stat(filepath.Join(fe, ".env")); !os.IsNotExist(err) {
			t.Fatal("planning changed env file")
		}
	}
	if _, err := PlanPair(cfg, "other", "task", fe); err == nil {
		t.Fatal("unrelated repo accepted")
	}
	if _, err := PlanPair(cfg, "web", "missing", fe); err == nil {
		t.Fatal("missing sibling accepted")
	}
	plan, _ := PlanPair(cfg, "api", "task", be)
	fed, bed, fec, bec, err := prepFEBE(cfg, "api", "task", be)
	if err != nil {
		t.Fatal(err)
	}
	if fed != plan.FEDir || bed != plan.BEDir || fec != plan.FECmd || bec != plan.BECmd {
		t.Fatal("launcher differs from preview")
	}
	data, err := os.ReadFile(filepath.Join(fe, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range plan.Env {
		if !strings.Contains(string(data), key+"="+value+"\n") {
			t.Fatalf("missing planned setting %s in %s", key, data)
		}
	}
}

func TestDisabledTaskLabelDoesNotCreateEnv(t *testing.T) {
	root := t.TempDir()
	fe, be := filepath.Join(root, "web/task"), filepath.Join(root, "api/task")
	os.MkdirAll(fe, 0o755)
	os.MkdirAll(be, 0o755)
	empty := ""
	cfg := config.Config{WorktreeRoots: []string{root}, Pairs: []config.Pair{{FERepo: "web", BERepo: "api", TaskEnvVar: &empty}}}
	if _, _, _, _, err := prepFEBE(cfg, "web", "task", fe); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fe, ".env.local")); !os.IsNotExist(err) {
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

func TestPatchEnvRejectsSymlink(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	path := filepath.Join(outside, "secret")
	os.WriteFile(path, []byte("API=old\n"), 0o600)
	os.Symlink(path, filepath.Join(dir, ".env"))
	if err := patchEnvFile(dir, ".env", map[string]string{"API": "new"}); err == nil {
		t.Fatal("symlink accepted")
	}
	os.Symlink(outside, filepath.Join(dir, "config"))
	if err := patchEnvFile(dir, "config/secret", map[string]string{"API": "new"}); err == nil {
		t.Fatal("parent symlink accepted")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "API=old\n" {
		t.Fatal("outside file changed")
	}
}
