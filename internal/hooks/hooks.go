// Package hooks runs the optional per-repo setup command from
// ~/.config/ork/hooks.json after new-task creates a worktree. Plain JSON
// (not shell-sourced) so a hooks file copied from elsewhere can't execute
// anything beyond the one command string it declares per repo.
package hooks

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
)

// Out receives hook output. stderr by default (cd contract: stdout is
// reserved); the MCP server swaps in a buffer to return it to the caller.
var Out io.Writer = os.Stderr

// Lookup returns the hook command for repo, or "" if the file is missing,
// unparsable, or has no entry — hooks are optional, most repos have none.
func Lookup(configPath, repo string) string {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return ""
	}
	return m[repo]
}

// RunRepoHook executes repo's hook command with cwd = the new worktree.
// Output goes to stderr (cd contract: stdout is reserved). Silent no-op
// when there's no hook.
func RunRepoHook(configPath, repo, wtDir string) error {
	cmd := Lookup(configPath, repo)
	if cmd == "" {
		return nil
	}
	c := exec.Command("bash", "-c", cmd)
	c.Dir = wtDir
	c.Stdout = Out
	c.Stderr = Out
	return c.Run()
}
