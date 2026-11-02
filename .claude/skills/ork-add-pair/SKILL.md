---
name: ork-add-pair
description: Add a new FE/BE (or any multi-repo) process group to ork's config so ctrl-g/ctrl-a can spawn every side for a task. Use when asked to "add a pair to ork", "set up ork for repo X and Y", or "wire up ctrl-g for <repo>".
---

# ork: add a repo pair (process group)

ork (`~/personal/orkestra`) drives tmux/herdr sessions per task-worktree. Repos sharing task names can be declared as a "group" so ctrl-g spawns one detached window per process and ctrl-a attaches them all. A classic fe/be pair is just a 2-process group.

Config lives in `~/.config/ork/` (JSON, no code execution) plus `~/.ork.conf` (bash-sourced, line-parsed by `internal/config/config.go`). **Never edit files under `~/code/**` for this** — ork-side only.

## Files involved

- `~/.config/ork/groups.json` (override: `ORK_GROUPS_CONFIG`) — array of `{name, processes: [{label, repo, cmd, port_range, fixed_port, env_var, env_path, url_env_vars, env_file, task_env_var, patch_file, patch_key}]}`. This is what ctrl-g reads (`config.Group`/`config.Process`, `internal/ui/keys.go` ctrl+g → `worktree.EnsureGroupWindows`). There is no `pairs.json` and no `ORK_FE_REPO`/`ORK_BE_REPO` shorthand anymore.
- `~/.config/ork/hooks.json` — per-repo one-shot setup command run after `new-task` creates a worktree (cwd = new worktree). Typical use: copy `.env`/`.envrc` from the canonical repo checkout and install deps.

## Steps

1. **Identify the repo folder names** exactly as they appear under `~/code/<repo>` (these are what ork scans for). Do not guess — `ls ~/code` or check `~/worktrees/<repo>`.
2. **Check for existing env files** in the canonical repo (`~/code/<repo>`) to know what the setup hook should copy — look for `.env`, `.env.local`, `.envrc` etc. Match exactly what's there; don't assume `.env.local` if the repo actually uses `.envrc` (direnv).
3. **Add a group entry** to `~/.config/ork/groups.json`:
   ```json
   {"name": "<group-name>", "processes": [
     {"label": "fe", "repo": "<repo-a>", "cmd": "<command run in repo-a's worktree>", "port_range": "fe"},
     {"label": "be", "repo": "<repo-b>", "cmd": "<command run in repo-b's worktree>", "port_range": "be"}
   ]}
   ```
   - `label` names the window and must be unique within the group.
   - `{port}` in `cmd` gets substituted with a stable per-task port when `port_range` is `"fe"` (3000-3999) or `"be"` (8000-8999). If the port is hardcoded in the repo's own config, omit `port_range`, don't use `{port}`, and set `fixed_port` so ork can warn when it's already taken.
   - `{fe_port}`/`{be_port}` in `cmd` give either side's port (e.g. a BE that needs its FE's origin for CORS).
   - `env_file` (default `.env.local`) picks the dotenv file — e.g. `.env` for Vite. `task_env_var` names the key that gets the task name (default `NEXT_PUBLIC_ORK_TASK`, only for processes with `port_range: "fe"`/`env_var`/`url_env_vars`; `""` disables) — use `VITE_…` for Vite apps.
   - `env_var`/`env_path`/`url_env_vars` are optional — only add if that process's `.env.local` needs rewriting to point at the task's own BE port (`env_var`) or its own FE origin (`url_env_vars`, e.g. `NEXTAUTH_URL`).
   - `patch_file`/`patch_key` are for apps whose backend url lives elsewhere — e.g. a Pkl/YAML/JSON template that gets *rendered* into a generated config file on every dev-server start (patching the generated file directly gets clobbered the moment the dev command runs). `patch_file` is a path relative to that process's worktree; ork rewrites `key = "http://localhost:PORT"` (or `key: "..."`) in it to the task's BE port. Find the real source (grep the repo's dev/build task for what generates the file you'd naively patch) — don't patch generated output.
   - If the repo uses direnv (`.envrc`), the setup hook must also run `direnv allow .` after copying it in, or the dev command fails with "is blocked" on first run in the new worktree.
4. **Add/update setup hooks** in `~/.config/ork/hooks.json` for each repo in the group, if not already present — typically copying env files from the canonical checkout plus installing deps.
5. **Validate**: run `ork config check` — it lists the groups and exits non-zero on any problem (bad JSON, unknown/typo'd keys, invalid group). Normal startup only warns and skips an invalid group, so don't rely on ork "still starting" as proof. Then preview a real task with `ork group <task> --dry-run` from one of the repos (worktrees, ports, commands, env/template updates — writes nothing). Also check hooks.json parses (`jq . ~/.config/ork/hooks.json`).
6. Do **not** run `./build.sh` or touch Go source for a config-only change — config is read at runtime from these files.

## Gotchas

- `hooks.json` and `groups.json` are separate files — a repo can have a setup hook without being in a group, and vice versa.
- A repo may belong to several groups. ork picks the group with the most member worktrees present for the task, and prompts with a picker on a tie — so adding a second group containing an already-grouped repo is fine, but expect the picker until the task's worktrees disambiguate it.
- Processes whose repo has no worktree for the task are skipped (with a note), not errors. If the real launch command is a single combined task (e.g. a `mise` task that starts everything), a 1-process group is fine.
