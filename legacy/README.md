# Legacy bash implementation — frozen

The original pure-bash/fzf implementation of ork, kept functional but
**unmaintained**. The Go version at the repo root replaced it (same
behavior, same config, same hooks) — this folder exists so none of the
hard-won knowledge in these scripts' comments is lost, and as a fallback
if you can't build Go.

Contents:

- `ork` — the fzf-driven picker (the old `~/.local/bin/ork`)
- `ork-helper.sh` — non-interactive helper the picker's fzf bindings call
- `worktree-tasks.sh` — the original full-bash `new-task`/`end-task` shell
  functions (the root copy is now a thin shim over the Go binary)
- `ork.sh` — the cd wrapper (identical to the root copy)
- `build.sh` — installs THIS bash version over whatever is in
  `~/.local/bin` (run from this folder, after the root `install.sh` has
  set up rc files at least once)
- `orc.cow` — cowsay orc (copy; the root one is the live one)

Requires `fzf` 0.36+ and `jq` (per-repo hooks) — deps the Go version
dropped.

The comments in `ork` and `ork-helper.sh` document real debugging history
(fzf `become()` vs marker respawn, `-A -d` attach semantics, per-task
session resolution, kill-session-last ordering). Read them before
re-learning any of it the hard way.
