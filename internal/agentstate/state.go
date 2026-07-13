// Package agentstate reads the per-session state files written by the
// Claude Code hook (~/.claude/hooks/ork-agent-state.sh) and watches the
// dir to drive live TUI refresh. Push-based: the hook fires the instant
// Claude Code's own state changes; no pane scraping, no polling.
package agentstate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// StaleAfter: not the primary freshness mechanism — only catches sessions
// that died without ever firing a Stop hook (killed pane, crashed process).
const StaleAfter = 600 * time.Second

// Dir returns the default state dir (~/.cache/ork/agent-state).
func Dir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache/ork/agent-state")
}

// Read returns the cached state ("running"/"waiting"/"input") for a tmux
// session, or "" if missing or older than staleAfter.
func Read(dir, session string, staleAfter time.Duration) string {
	p := filepath.Join(dir, session)
	st, err := os.Stat(p)
	if err != nil || time.Since(st.ModTime()) > staleAfter {
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// Watch emits on ch whenever anything in dir changes, debounced 100ms so a
// burst of hook writes coalesces into one reload. Creates dir if missing.
func Watch(ctx context.Context, dir string) (<-chan struct{}, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := w.Add(dir); err != nil {
		w.Close()
		return nil, err
	}
	ch := make(chan struct{}, 1)
	go func() {
		defer w.Close()
		var timer *time.Timer
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-w.Events:
				if !ok {
					return
				}
				if timer != nil {
					timer.Stop()
				}
				timer = time.AfterFunc(100*time.Millisecond, func() {
					select {
					case ch <- struct{}{}:
					default:
					}
				})
			case <-w.Errors:
			}
		}
	}()
	return ch, nil
}
