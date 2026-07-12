# worktree-orch

Terminal UI to control and jump between coding agents running in git
worktrees + tmux. Built on `fzf` + `tmux`. Ships as a standalone executable
(`orch`) — works from bash or zsh, like `mise`/`nvim`.

## What it does

- Lists every worktree under `~/worktrees/<repo>/<task>`, showing repo,
  task/branch, and whether a tmux pane is currently running there.
- **ENTER** — jump: switches to the live tmux pane, or `cd`s into the idle
  worktree folder and runs `git status`.
- **ctrl-n** — new task: pick a repo (fuzzy search, your favorites listed
  first), type a new task name, creates the worktree/branch and runs any
  repo-specific setup hook you've configured.
- **ctrl-x** — end task: asks to confirm, then removes the worktree and
  deletes the branch both locally and on `origin`.
- **ctrl-r** — refresh the list.

## Requirements

- `tmux`, `fzf`
- `bash` or `zsh`
- git (worktrees)

## Install

```sh
git clone <this-repo> worktree-orch
cd worktree-orch
./install.sh
source ~/.zshrc   # or ~/.bashrc
orch
```

The installer copies:
- `orch`, `orch-helper.sh` → `~/.local/bin/` (the real executable — put this
  dir on your `$PATH` if it isn't already)
- `worktree-tasks.sh`, `orch.sh` → `~/scripts/` (sourced from your shell rc)
- `~/.orch.conf` from the example, if you don't already have one

### Why a wrapper function at all?

`orch` is a plain binary — it can't change your shell's current directory by
itself (no subprocess can). It prints the target directory as its last
stdout line when it wants you to land somewhere; `orch.sh` defines a ~4-line
`orch()` shell function that captures that and `cd`s for you. Everything
else (the fzf UI, tmux control, git operations) runs in the binary itself.

If you already have your own `new-task`/`end-task` functions with the same
`~/worktrees/<repo>/<task>` layout, remove the `worktree-tasks.sh` source
line from your rc file — `orch` only needs `new-task` and `end-task` to
exist, it doesn't care how they're implemented.

## Configuration — `~/.orch.conf`

```sh
# Repos shown first in the ctrl-n repo picker.
ORCH_FAVORITES=(my-backend my-frontend)

# Optional: runs after `new-task` creates a worktree for that repo (cwd =
# the new worktree). Function name = repo folder name, non-alnum -> "_".
ORCH_HOOK_my_backend() {
  cp -rpvu "$HOME/code/my-backend/infra" .
}

ORCH_HOOK_my_frontend() {
  bun install
}
```

Repos without a matching `ORCH_HOOK_*` just skip the hook step.

## Terminal keybind (optional)

The installer can bind a keybind (default **ctrl+alt+o**) so your terminal
types `orch` + Enter into the focused shell — the `orch()` wrapper runs and
its cd-on-exit works as usual. It only fires usefully when a shell prompt is
focused.

Supported (config must already exist under `~/.config/<terminal>/`):

- **Ghostty** — `keybind = ctrl+alt+o=text:orch\n`
- **kitty** — `map ctrl+alt+o send_text all orch\r`
- **Alacritty** — `[[keyboard.bindings]]` with `mods = "Control|Alt"`, `key = "O"`, `chars = "orch\r"`

`./install.sh` asks before installing it, then asks for the chord (Enter
keeps the default); use `--keybind` / `--no-keybind` to skip the first
prompt. You can also run `./keybind-install.sh [CHORD]` directly, e.g.
`./keybind-install.sh ctrl+shift+k`. Re-running (with the same or a
different chord) replaces the previous binding rather than stacking — it's
fenced with `# >>> orch keybind >>>` markers, and `./uninstall.sh` removes
it. `ctrl+shift+o` is deliberately not the default: it collides with
Ghostty's built-in new-split binding.

Other terminals: add an equivalent "send text" binding yourself — PRs
welcome.

## Uninstall

```sh
./uninstall.sh                # removes orch files + rc source lines, keeps ~/.orch.conf
./uninstall.sh --purge-config # also deletes ~/.orch.conf
```
