package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// encProcess is Process with every optional key omitted when unset, so a
// group written by ork reads like one written by hand. TaskEnvVar stays a
// pointer: an explicit "" (disable the task label) must survive.
type encProcess struct {
	Label      string   `json:"label"`
	Repo       string   `json:"repo"`
	Cmd        string   `json:"cmd"`
	PortRange  string   `json:"port_range,omitempty"`
	FixedPort  int      `json:"fixed_port,omitempty"`
	EnvVar     string   `json:"env_var,omitempty"`
	EnvPath    string   `json:"env_path,omitempty"`
	URLEnvVars []string `json:"url_env_vars,omitempty"`
	PatchFile  string   `json:"patch_file,omitempty"`
	PatchKey   string   `json:"patch_key,omitempty"`
	EnvFile    string   `json:"env_file,omitempty"`
	TaskEnvVar *string  `json:"task_env_var,omitempty"`
}

type encGroup struct {
	Name      string       `json:"name"`
	Processes []encProcess `json:"processes"`
}

func encodeGroup(g Group) ([]byte, error) {
	e := encGroup{Name: g.Name}
	for _, p := range g.Processes {
		e.Processes = append(e.Processes, encProcess(p))
	}
	return json.MarshalIndent(e, "  ", "  ")
}

// ValidateGroupName: the name is shown in pickers and passed to --group, so
// it must be a non-empty single line.
func ValidateGroupName(name string) error {
	if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "\r\n") {
		return fmt.Errorf("group name must be a non-empty single line")
	}
	return nil
}

// readGroupEntries returns the groups file as raw entries plus each entry's
// name. A missing file is an empty list; an unparseable one is an error —
// rewriting it would destroy whatever the user has there.
func readGroupEntries(path string) ([]json.RawMessage, []string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var raw []json.RawMessage
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, nil, fmt.Errorf("%s: %w — fix it by hand first", path, err)
		}
	}
	names := make([]string, len(raw))
	for i, r := range raw {
		var n struct {
			Name string `json:"name"`
		}
		json.Unmarshal(r, &n)
		names[i] = n.Name
	}
	return raw, names, nil
}

// writeGroupEntries writes entries one per array element. Untouched entries
// keep their original bytes, so hand formatting survives an edit elsewhere.
func writeGroupEntries(path string, entries []json.RawMessage) error {
	var b bytes.Buffer
	b.WriteString("[")
	for i, e := range entries {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n  ")
		b.Write(bytes.TrimSpace(e))
	}
	b.WriteString("\n]\n")
	return replaceFile(path, b.Bytes())
}

// UpsertGroup validates g and writes it to the groups file at path,
// replacing the entry with the same name in place or appending a new one.
func UpsertGroup(path string, g Group) (replaced bool, err error) {
	if err := ValidateGroupName(g.Name); err != nil {
		return false, err
	}
	if err := g.Validate(); err != nil {
		return false, err
	}
	entries, names, err := readGroupEntries(path)
	if err != nil {
		return false, err
	}
	enc, err := encodeGroup(g)
	if err != nil {
		return false, err
	}
	for i, n := range names {
		if n == g.Name {
			entries[i] = enc
			return true, writeGroupEntries(path, entries)
		}
	}
	return false, writeGroupEntries(path, append(entries, enc))
}

// DeleteGroup removes the group called name from the groups file.
func DeleteGroup(path, name string) error {
	entries, names, err := readGroupEntries(path)
	if err != nil {
		return err
	}
	for i, n := range names {
		if n == name {
			return writeGroupEntries(path, append(entries[:i], entries[i+1:]...))
		}
	}
	return fmt.Errorf("no group named %q in %s", name, path)
}

// SetRepoHook sets repo's setup command in the hooks file at path; an
// empty cmd removes the entry.
func SetRepoHook(path, repo, cmd string) error {
	if repo == "" || repo == "." || repo == ".." || strings.ContainsAny(repo, `/\`) {
		return fmt.Errorf("repo must be a repo folder name")
	}
	hooks := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &hooks); err != nil {
			return fmt.Errorf("%s: %w — fix it by hand first", path, err)
		}
	}
	if cmd == "" {
		if _, ok := hooks[repo]; !ok {
			return fmt.Errorf("no hook for %q in %s", repo, path)
		}
		delete(hooks, repo)
	} else {
		hooks[repo] = cmd
	}
	out, err := json.MarshalIndent(hooks, "", "  ")
	if err != nil {
		return err
	}
	return replaceFile(path, append(out, '\n'))
}

// ReadHooks returns the hooks file's entries (empty when missing).
func ReadHooks(path string) (map[string]string, error) {
	hooks := map[string]string{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return hooks, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &hooks); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return hooks, nil
}

// replaceFile writes data to path atomically, keeping the previous version
// as path.bak — these files are hand-maintained and not under version
// control, so one undo step is worth having.
func replaceFile(path string, data []byte) error {
	// Write through a symlinked config (dotfiles repos) instead of
	// replacing the link with a plain file.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if old, err := os.ReadFile(path); err == nil {
		if st, err := os.Stat(path); err == nil {
			mode = st.Mode().Perm()
		}
		if err := os.WriteFile(path+".bak", old, mode); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
