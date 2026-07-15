# Orkestra

Command the horde, fell the branches.

Terminal UI to control and jump between coding agents running in git
worktrees + tmux. A single Go binary with a native TUI (bubbletea) driving
`tmux` + `git` — works from bash or zsh, like `mise`/`nvim`. (The original
bash/fzf implementation lives in `legacy/`, functional but frozen.)

## What it does

- Lists every worktree under your configured `ORK_WORKTREES_ROOTS` (default
  `~/worktrees/<repo>/<task>`), showing repo, task/branch, whether a tmux
  session is currently running there (STATE column), and — if you've
  installed the Claude Code hook (see below) — whether the agent in that
  session is actively `running`, `waiting` on you, or waiting on an
  `input`/permission prompt (AGENT column).
- The list refreshes itself live: the TUI watches the agent-state directory
  (fsnotify), so whenever the Claude Code hook fires (agent
  starts/stops/needs input) the picker reloads automatically — no need to
  press ctrl-r to see a status change.
- Repo names are colored for grouping — distinct repos get maximally
  distinct colors, stable across runs while your set of repos is stable.
- Type to fuzzy-filter the list. An orc (cowsay + fortune) heckles from the
  right margin if you have both installed.
- **ENTER** — attach-or-create: lands you in a tmux session for that
  worktree (attaches if the session already exists, creates it otherwise).
- **alt-ENTER** — cd only, no tmux session: for when you just want to look
  at a worktree (check status, poke around) without spawning or attaching
  an agent session for it.
- **ctrl-n** — new task: pick a repo (fuzzy search across all of `$HOME`,
  your favorites listed first), type a new task name, creates the
  worktree/branch off the repo's actual default branch, copies the repo's
  `.env.local` if it has one, runs any repo-specific setup hook you've
  configured, and lands you straight in its tmux session. Configured fe/be
  pairs also appear as a combined `fe + be` entry (searching either name
  finds it) — picking it, or hitting **ctrl-b** on either sibling's row,
  creates the same-named task in both repos at once (one shared session,
  since sessions are task-named).
- **ctrl-x** — end task: asks to confirm ("no" is the default), then
  removes the worktree, kills its tmux session (unless another repo's
  worktree still shares that task name — see "Session naming" below), and
  deletes the branch both locally and on `origin`.
- **ctrl-k** — kill session: asks to confirm, then kills the tmux session
  for the selected worktree without touching the worktree or branch (and
  without switching you away from wherever you currently are). ENTER just
  recreates the session afterward. Same "don't yank a session a sibling
  repo still shares" guard as ctrl-x.
- **ctrl-r** — refresh the list manually.
- **tab** — cycle the bottom panel: info (branch/path/tmux summary + live
  pane preview, refreshed every second — shown by default) → `git status`
  for the selected worktree → hidden.
- **ctrl-s** — toggle a 50/50 split panel: `git status` on the left, the
  live info panel on the right — check for uncommitted changes without
  losing sight of what the agent is doing.

## Requirements

- `tmux` — required, no fallback without it
- `bash` or `zsh`
- git (worktrees)
- Go 1.22+ — build-time only (`install.sh` compiles the binary; e.g.
  `mise use -g go@latest`)
- `fortune` + `cowsay` — optional, for the orc sidebar
- [Claude Code](https://claude.com/claude-code) — optional; only needed for
  the AGENT column and live picker refresh (see below). Everything else
  works without it.
- fzf and jq are no longer needed (the native TUI replaced fzf; hooks.json
  is parsed natively)

## Install

```sh
git clone <this-repo> orkestra
cd orkestra
./install.sh
source ~/.zshrc   # or ~/.bashrc
ork
```

The installer:
- builds the Go binary and copies it (plus `orc.cow`) → `~/.local/bin/`
  (put this dir on your `$PATH` if it isn't already)
- copies `worktree-tasks.sh`, `ork.sh` → `~/scripts/` (sourced from your
  shell rc; `new-task`/`end-task` are thin shims over `ork new-task` /
  `ork end-task`)
- installs the Claude Code hook (`hooks/ork-agent-state.sh` →
  `~/.claude/hooks/`) and wires it into `~/.claude/settings.json` — see
  "Claude Code agent status" below. Skipped with a note if `~/.claude`
  isn't found; safe/idempotent to re-run later once it is.
- creates `~/.ork.conf` from the example, if you don't already have one

No Go available? The frozen bash version still works: `legacy/build.sh`
(requires fzf + jq; see `legacy/README.md`).

### Why a wrapper function at all?

`ork` is a plain binary — it can't change your shell's current directory by
itself (no subprocess can). It prints the target directory as its last
stdout line when it wants you to land somewhere; `ork.sh` defines a ~4-line
`ork()` shell function that captures that and `cd`s for you. Everything
else (the TUI, tmux control, git operations) runs in the binary itself.

If you already have your own `new-task`/`end-task` functions with the same
`<worktrees-root>/<repo>/<task>` layout, remove the `worktree-tasks.sh`
source line from your rc file — the shims are conveniences, not
dependencies; the picker talks to the binary directly.

## Configuration — `~/.ork.conf`

```sh
# Where per-task worktrees get created/found — one subfolder per repo
# (<root>/<repo>/<task>). The FIRST entry is where new ones get created;
# ork searches all entries when looking up an existing one. Defaults to
# ~/worktrees if unset.
ORK_WORKTREES_ROOTS=("$HOME/worktrees")

# Repos shown first in the ctrl-n repo picker. Repos themselves are never
# configured — ork finds them by scanning $HOME live (cached on disk for
# 60s) for any directory containing a .git.
ORK_FAVORITES=(my-backend my-frontend)

# How deep that $HOME scan goes (repo dir + one level for .git = depth 3
# by default). Raise it if your repos live nested deeper than that.
# ORK_SCAN_MAXDEPTH=3
```

The file is plain bash syntax (the shell shims still source it); the Go
binary parses the assignments above natively and ignores anything else.

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

### FE/BE pairs

Sibling repos that share task names can be declared as pairs — ctrl-g
spawns fe/be tmux windows running each side's dev command in its worktree,
with a stable per-task port ({port} placeholder, FE 3000-3999, BE
8000-8999) and the fe `.env.local` rewritten to point at the task's
backend. One pair fits in `~/.ork.conf` (`ORK_FE_REPO`/`ORK_BE_REPO`/...);
any number more go in `~/.config/ork/pairs.json` (path override:
`ORK_PAIRS_CONFIG`):

```json
[
  {
    "fe": "my-frontend", "be": "my-backend",
    "fe_cmd": "bun run dev -- --port {port}",
    "be_cmd": "uv run fastapi dev src/app.py --port {port}",
    "fe_env_var": "NEXT_PUBLIC_MY_SERVICE_ENDPOINT",
    "fe_env_path": "/public/operations",
    "fe_url_env_vars": ["NEXTAUTH_URL"]
  }
]
```

`fe_env_path` is optional — appended after the port in the URL written to
`fe_env_var`. `fe_url_env_vars` keys get rewritten to the task's own fe
origin (`http://localhost:<fePort>`) — for apps that hardcode it, like
next-auth's `NEXTAUTH_URL`, which would otherwise bounce auth redirects to
whatever runs on port 3000. `fe_cmd`/`be_cmd` fall back to the built-in
defaults when omitted. A repo row triggers pairing only if it belongs to a declared pair.

### Session naming

Tmux sessions are named after the task, not the repo — by design, so one
agent can span multiple repos under the same task (a BE+FE pair sharing one
session for context continuity). Set `ORK_SCOPE_SESSIONS_TO_REPO=1` in
`~/.ork.conf` if you'd rather sessions never collide across repos. Ending a
task only kills the shared task-named session if no other repo's worktree
under that same task name still exists.

## Claude Code agent status (optional)

The AGENT column (`running` / `waiting` / `input`) and the picker's live
auto-refresh are both driven by a Claude Code hook, installed automatically
by `install.sh` if `~/.claude` exists:

- `hooks/ork-agent-state.sh` → `~/.claude/hooks/ork-agent-state.sh`
- wired into `~/.claude/settings.json`:
  - `UserPromptSubmit` / `PreToolUse` / `PostToolUse` → `... running`
  - `Stop` → `... waiting`
  - `Notification` / `PermissionRequest` → `... input`

This is push-based, not polled: Claude Code fires the hook the instant its
own state changes, the hook writes that into
`~/.cache/ork/agent-state/<tmux-session>`, and every open picker sees the
write immediately via a filesystem watch (fsnotify) on that directory. No
pane-content scraping, no fixed poll interval, no missed-update window.
(The bash version needed the hook to poke each picker over a local port —
the Go TUI watches the directory itself, so that nudge step is obsolete;
the hook script is unchanged and still works for both.)

Only sessions running Claude Code report a status — a plain shell, vim,
or another tool in that tmux session just shows blank in the AGENT column.
This hook is Claude-Code-specific; other CLIs have their own hook/plugin
systems and aren't wired up here.

The install/uninstall steps are idempotent and non-destructive: re-running
`install.sh` won't create duplicate entries in `settings.json`, and any
*other* hooks you already have configured on the same events are left
completely untouched. `uninstall.sh` reverses all of it while leaving
unrelated `settings.json` content alone.

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

## Development

```sh
go build -o bin/ork ./cmd/ork   # build
go test ./...                    # run the test suite
./build.sh                       # rebuild + reinstall over an existing install
```

Layout: `cmd/ork` (entrypoint, cd contract), `internal/config` (~/.ork.conf
parser), `internal/worktree` (discovery, rows, new-task/end-task),
`internal/tmux` (tmux CLI wrapper), `internal/agentstate` (hook state files
+ fsnotify watch), `internal/hooks` (hooks.json), `internal/ui` (bubbletea
TUI).

## Uninstall

```sh
./uninstall.sh                # removes ork files + rc source lines, keeps ~/.ork.conf
./uninstall.sh --purge-config # also deletes ~/.ork.conf and ~/.config/ork/hooks.json
```

Also removes the Claude Code hook (`~/.claude/hooks/ork-agent-state.sh`)
and strips only `ork`'s entries from `~/.claude/settings.json` — any other
hooks you have configured there are left untouched — plus the disposable
`~/.cache/ork/` state (agent-state, repo-scan cache; unrelated to
`~/.ork.conf`, always removed regardless of `--purge-config`).
