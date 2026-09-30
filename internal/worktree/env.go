package worktree

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// patchEnvFile applies a plan in one atomic write so read/write failures cannot
// leave a partially updated dotenv file. Unrelated settings and mode survive.
func patchEnvFile(dir, file string, values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	path := filepath.Join(dir, file)
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("environment file %s: %w", path, err)
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, parent)
	if err != nil || (rel != "." && !filepath.IsLocal(rel)) {
		return fmt.Errorf("environment file %s escapes worktree", path)
	}
	mode := os.FileMode(0o600)
	if st, err := os.Lstat(path); err == nil {
		if !st.Mode().IsRegular() {
			return fmt.Errorf("environment file %s must be a regular file", path)
		}
		mode = st.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}
	var lines []string
	seen := map[string]bool{}
	assignment := func(key, value string) string {
		// Double quotes protect whitespace, # and newlines; escape dotenv
		// interpolation as well as JSON-compatible quote/backslash escapes.
		if strings.ContainsAny(value, " \t\r\n#\"'$`\\") {
			value = strings.ReplaceAll(strconv.Quote(value), "$", `\$`)
		}
		return key + "=" + value
	}
	f, err := os.Open(path)
	if err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := sc.Text()
			key, _, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export ")), "=")
			key = strings.TrimSpace(key)
			if value, replace := values[key]; ok && replace {
				if !seen[key] {
					lines = append(lines, assignment(key, value))
					seen[key] = true
				}
			} else {
				lines = append(lines, line)
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	var keys []string
	for key := range values {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		lines = append(lines, assignment(key, values[key]))
	}
	tmp, err := os.CreateTemp(parent, ".ork-env-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	_, err = tmp.WriteString(strings.Join(lines, "\n") + "\n")
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp.Name(), path)
}
