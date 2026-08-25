---
name: ork-add-pair
description: Add a new FE/BE (or any two-repo) pair to ork's config so ctrl-g/ctrl-a can spawn both sides for a task. Use when asked to "add a pair to ork", "set up ork for repo X and Y", or "wire up ctrl-g for <repo>".
---

# ork: add a repo pair

ork (`~/personal/orkestra`) drives tmux/herdr sessions per task-worktree. Two repos sharing task names can be declared as a "pair" so ctrl-g spawns detached fe/be windows and ctrl-a attaches both.

Config lives in `~/.config/ork/` (JSON, no code execution) plus `~/.ork.conf` (bash-sourced, line-parsed by `internal/config/config.go`). **Never edit files under `~/code/**` for this** — ork-side only.

## Files involved

- `~/.config/ork/pairs.json` — array of `{fe, be, fe_cmd, be_cmd, fe_env_var, fe_env_path, fe_url_env_vars}`. This is what ctrl-g actually reads (`config.Pair`, `internal/ui/keys.go` ctrl+g → `worktree.EnsureFEBEWindows`).
- `~/.config/ord/hooks.json` — per-repo one-shot setup command run after `new-task` creates a worktree (cwd = new worktree). Typical use: copy `.env`/`.envrc` from the canonical repo checkout and install deps.
- `~/.config/ork/groups.json` — a *separate*, richer multi-process config (used elsewhere, not by ctrl-g). Only touch this if asked explicitly.

## Steps

1. **Identify the repo folder names** exactly as they appear under `~/code/<repo>` (these are what ork scans for). Do not guess — `ls ~/code` or check `~/worktrees/<repo>`.
2. **Check for existing env files** in the canonical repo (`~/code/<repo>`) to know what the setup hook should copy — look for `.env`, `.env.local`, `.envrc` etc. Match exactly what's there; don't assume `.env.local` if the repo actually uses `.envrc` (direnv).
3. **Add a pair entry** to `~/.config/ork/pairs.json`:
   ```json
   {
     "fe": "<repo-a>",
     "be": "<repo-b>",
     "fe_cmd": "<command run in repo-a's worktree>",
     "be_cmd": "<command run in repo-b's worktree>"
   }
   ```
   `{port}` in either cmd gets substituted with a stable per-task port. `fe_env_var`/`fe_env_path`/`fe_url_env_vars` are optional — only add if the FE needs its `.env.local` rewritten to point at the task's own BE port. `fe_patch_file`/`fe_patch_key` are for apps whose backend url lives elsewhere — e.g. a Pkl/YAML/JSON template that gets *rendered* into a generated config file on every dev-server start (patching the generated file directly gets clobbered the moment the dev command runs). `fe_patch_file` is a path relative to the fe worktree; ork rewrites the first `key = "http://localhost:PORT"` (or `key: "..."`) line in it to the task's BE port. Find the real source (grep the repo's dev/build task for what generates the file you'd naively patch) — don't patch generated output.
   - If the repo uses direnv (`.envrc`), the setup hook must also run `direnv allow .` after copying it in, or the dev command fails with "is blocked" on first run in the new worktree.
4. **Add/update setup hooks** in `~/.config/ork/hooks.json` for each repo in the pair, if not already present — typically copying env files from the canonical checkout plus installing deps. Validate the JSON after editing (trailing commas break the whole file silently — a broken hooks.json falls back to no hooks with no error).
5. **Validate both JSON files parse** (`python3 -m json.tool <file>` or `jq .`) before finishing.
6. Do **not** run `./build.sh` or touch Go source for a config-only change — config is read at runtime from these files.

## Gotchas

- `hooks.json` and `pairs.json` are separate files — a repo can have a setup hook without being in a pair, and vice versa.
- Legacy single-pair keys (`ORK_FE_REPO`/`ORK_BE_REPO`/`ORK_FE_CMD`/`ORK_BE_CMD`/`ORK_FE_ENV_VAR`) in `~/.ork.conf` become `Pairs[0]` and take priority over a `pairs.json` entry for the same repo names — check `~/.ork.conf` isn't already claiming one of the two repos before adding to `pairs.json`, or the file entry will be silently shadowed.
- ctrl-g always runs both `fe_cmd` and `be_cmd` — there's no "just one side" mode. If the real launch command is a single combined task (e.g. a `mise` task that starts everything), point both `fe_cmd` and `be_cmd` at it, or point one side at the combined command and give the other side a no-op / `true`.
