// Package hooks runs the optional per-repo setup command from
// ~/.config/ork/hooks.json after new-task creates a worktree. Plain JSON
// (not shell-sourced) so a hooks file copied from elsewhere can't execute
// anything beyond the one command string it declares per repo.
package hooks

import (
	"encoding/json"
	"os"
	"os/exec"
)

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
	c.Stdout = os.Stderr
	c.Stderr = os.Stderr
	return c.Run()
}
