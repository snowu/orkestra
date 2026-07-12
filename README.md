# Orkestra

Command the horde, fell the branches.

Terminal UI to control and jump between coding agents running in git
worktrees + tmux. Built on `fzf` + `tmux`. Ships as a standalone executable
(`ork`) — works from bash or zsh, like `mise`/`nvim`.

## What it does

- Lists every worktree under `~/worktrees/<repo>/<task>`, showing repo,
  task/branch, and whether a tmux session is currently running there (STATE
  column — display only, informational).
- **ENTER** — attach-or-create: always lands you in a tmux session for that
  worktree (`tmux new -A -s <task> -c <worktree>` — attaches if the session
  already exists, creates it otherwise). tmux is required; there's no
  fallback path that just `cd`s without a session.
- **ctrl-n** — new task: pick a repo (fuzzy search, your favorites listed
  first), type a new task name, creates the worktree/branch, runs any
  repo-specific setup hook you've configured, and lands you straight in its
  tmux session.
- **ctrl-x** — end task: asks to confirm, then removes the worktree, kills
  its tmux session (unless another repo's worktree still shares that task
  name — see "Session naming" below), and deletes the branch both locally
  and on `origin`.
- **ctrl-k** — kill session: asks to confirm (`y`/`n`, tmux-style), then
  kills the tmux session for the selected worktree without touching the
  worktree or branch (and without switching you away from wherever you
  currently are). ENTER just recreates the session afterward. Same "don't
  yank a session a sibling repo still shares" guard as ctrl-x.
- **ctrl-r** — refresh the list.

## Requirements

- `tmux`, `fzf` — both required, no fallback without them
- `bash` or `zsh`
- git (worktrees)
- `jq` — only needed if you use per-repo setup hooks (`~/.config/ork/hooks.json`)

## Install

```sh
git clone <this-repo> orkestra
cd orkestra
./install.sh
source ~/.zshrc   # or ~/.bashrc
ork
```

The installer copies:
- `ork`, `ork-helper.sh` → `~/.local/bin/` (the real executable — put this
  dir on your `$PATH` if it isn't already)
- `worktree-tasks.sh`, `ork.sh` → `~/scripts/` (sourced from your shell rc)
- `~/.ork.conf` from the example, if you don't already have one

### Why a wrapper function at all?

`ork` is a plain binary — it can't change your shell's current directory by
itself (no subprocess can). It prints the target directory as its last
stdout line when it wants you to land somewhere; `ork.sh` defines a ~4-line
`ork()` shell function that captures that and `cd`s for you. Everything
else (the fzf UI, tmux control, git operations) runs in the binary itself.

If you already have your own `new-task`/`end-task` functions with the same
`~/worktrees/<repo>/<task>` layout, remove the `worktree-tasks.sh` source
line from your rc file — `ork` only needs `new-task` and `end-task` to
exist, it doesn't care how they're implemented.

## Configuration — `~/.ork.conf`

```sh
# Repos shown first in the ctrl-n repo picker.
ORK_FAVORITES=(my-backend my-frontend)
```

### Per-repo setup hooks — `~/.config/ork/hooks.json`

Optional: runs a shell command after `new-task` creates a worktree for that
repo (cwd = the new worktree). Kept as plain JSON in its own file (not
shell-sourced like `~/.ork.conf`) so a hooks file you copy from elsewhere
can't execute anything beyond the one command string it declares for a
given repo.

```json
{
  "my-backend": "cp -rpvu \"$HOME/code/my-backend/infra\" .",
  "my-frontend": "bun install"
}
```

Key = repo folder basename. Repos without a matching key just skip the hook
step. Point at a different file with `ORK_HOOKS_CONFIG` in `~/.ork.conf`.
Requires `jq`.

### Session naming

Tmux sessions are named after the task, not the repo — by design, so one
agent can span multiple repos under the same task (a BE+FE pair sharing one
session for context continuity). Set `ORK_SCOPE_SESSIONS_TO_REPO=1` in
`~/.ork.conf` if you'd rather sessions never collide across repos. Ending a
task only kills the shared task-named session if no other repo's worktree
under that same task name still exists.

## Terminal keybind (optional)

The installer can bind a keybind that opens the `ork` picker.

**tmux window (recommended)** — if you use tmux, this is the safe option:
**prefix + o** (tmux's own prefix system, e.g. `ctrl-b` then `o`) opens
`ork` in a new tmux window that fills the whole client, without touching
whatever pane/program had focus. It's a normal tmux prefix-table binding —
it only fires after you've pressed the prefix, and never leaks into the
pane's own program (vim, Claude Code, anything). Deliberately *not* a
standalone chord shared with the terminal-emulator layer below — keeping
tmux in its own prefix namespace means it can never collide with (or
duplicate) a terminal-emulator keybind. `install.sh` asks about this
first, defaulting to yes; the key after the prefix is configurable
(default `o`). The window closes itself automatically when `ork` exits —
no manual cleanup, and it's not a floating popup, so there's no border
eating into the screen (tmux 3.2a's `display-popup` always draws one; a
plain window doesn't).

**Terminal-emulator keybinds** — an optional second layer, using a
standalone chord (default **ctrl+alt+o**). Only terminals with
their *own* built-in keybind engine are supported (Ghostty, kitty,
Alacritty) — each types `ork` + Enter into the shell you're already
looking at. The existing `ork()` wrapper runs normally and its
cd-on-exit works as usual. Only useful when a shell prompt is focused —
if something else has focus (vim, an editor, etc.) the keystroke goes
there instead. Prefer the tmux window if you have tmux available.

gnome-terminal and macOS Terminal.app are **not** supported: neither has a
built-in keybind-to-command mechanism — they rely entirely on an external,
desktop-environment-specific global-hotkey system (GNOME Shell's
`gsettings`, or macOS System Settings) that this script can't reliably
configure or even detect is actually running (e.g. it silently no-ops
under tiling window managers like i3/sway, where nothing consumes a
`gsettings` custom-keybinding write). Wire those by hand in your WM/DE if
you want them, or just alias/type `ork` — it's a plain command.

What gets written:

- **tmux** (`~/.tmux.conf`) — `bind-key o new-window -n ork ork`
  (prefix table — plain `o`, no `-n`/root-table flag, so it only fires
  after your tmux prefix; `-n ork` here is `new-window`'s own flag for
  the window's *name*, unrelated to the prefix-table `-n`). Fills the
  whole client, no border, closes itself when `ork` exits.
  `keybind-install.sh` reloads `~/.tmux.conf` into any running tmux server
  automatically, so this applies immediately if tmux is already running —
  no manual `tmux source-file` needed.
- **Ghostty** (`~/.config/ghostty/config`) — `keybind = ctrl+alt+o=text:ork\n`.
  **After installing, reload Ghostty's config** (`ctrl+shift+,`, i.e.
  `reload_config`) or restart it.
- **kitty** (`~/.config/kitty/kitty.conf`) — `map ctrl+alt+o send_text all ork\r`
- **Alacritty** (`~/.config/alacritty/alacritty.toml`) — `[[keyboard.bindings]]`
  with `mods = "Control|Alt"`, `key = "O"`, `chars = "ork\r"`

`./install.sh` asks about the tmux keybind first (skipped entirely if
`tmux` isn't installed; otherwise default yes, prompts for the key after
the prefix, default `o`), then whether to also set up a terminal-emulator
keybind (default yes too — bare Enter accepts every prompt in the
installer, consistently) via a numbered menu (comma-separated multi-pick,
e.g. `1,3`), prompting separately for that layer's chord (default
`ctrl+alt+o`). Use `--no-keybind` to skip both prompts non-interactively;
scripted runs (no tty) always skip them regardless of flags, since there's
no terminal to prompt.

You can also run
`./keybind-install.sh <terminal>[,<terminal>...] [CHORD] [TMUX_KEY]`
directly, e.g. `./keybind-install.sh tmux,ghostty ctrl+shift+k g` (chord
for ghostty, `g` for tmux's prefix key). Re-running (same or different
chord/key) replaces the previous binding rather than stacking — it's
fenced with `# >>> ork keybind >>>` markers, and `./uninstall.sh` removes
it without touching anything else in those files — pre-existing config
content is never modified or deleted, and files `ork` didn't create are
never removed. `ctrl+shift+o` is deliberately not the terminal-emulator
default: it collides with Ghostty's built-in new-split binding.

Other terminals: add an equivalent binding yourself — PRs welcome.

## Uninstall

```sh
./uninstall.sh                # removes ork files + rc source lines, keeps ~/.ork.conf
./uninstall.sh --purge-config # also deletes ~/.ork.conf
```
