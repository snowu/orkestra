package agentstate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRead(t *testing.T) {
	dir := t.TempDir()
	if got := Read(dir, "missing", StaleAfter); got != "" {
		t.Errorf("missing = %q", got)
	}
	p := filepath.Join(dir, "sess1")
	os.WriteFile(p, []byte("running\n"), 0o644)
	if got := Read(dir, "sess1", StaleAfter); got != "running" {
		t.Errorf("fresh = %q", got)
	}
	old := time.Now().Add(-11 * time.Minute)
	os.Chtimes(p, old, old)
	if got := Read(dir, "sess1", StaleAfter); got != "" {
		t.Errorf("stale = %q", got)
	}
}

func TestWatch(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state") // must get created
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := Watch(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "sess1"), []byte("waiting"), 0o644)
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("no event after write")
	}
}
