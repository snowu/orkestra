package hooks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLookup(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hooks.json")
	os.WriteFile(p, []byte(`{"my-backend": "echo hi"}`), 0o644)
	if got := Lookup(p, "my-backend"); got != "echo hi" {
		t.Errorf("hit = %q", got)
	}
	if got := Lookup(p, "other"); got != "" {
		t.Errorf("miss = %q", got)
	}
	if got := Lookup(filepath.Join(t.TempDir(), "nope.json"), "x"); got != "" {
		t.Errorf("missing file = %q", got)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte(`not json`), 0o644)
	if got := Lookup(bad, "x"); got != "" {
		t.Errorf("bad json = %q", got)
	}
}
