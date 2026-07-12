# Terminal keybind for `orch` — design

## Goal

Let a user press a keybind (default `ctrl+alt+o`, changeable at install time)
at a shell prompt and have the terminal type `orch` + Enter, opening the
picker. Because the keybind types
into the *current* interactive shell, the existing `orch()` wrapper function
(loaded from the shell rc) runs normally and its `cd`-on-exit works with no
extra machinery.

Supported terminals: **Ghostty**, **kitty**, **Alacritty**. Other terminals are
not auto-wired — users add an equivalent binding themselves or open a PR.

## Why text-inject (not spawn-a-window)

`orch` is a plain binary; the `cd` into the chosen worktree happens in the
`orch()` shell function that captures the binary's last stdout line. A freshly
spawned terminal window is a new process with no caller to `cd`, so a
"launch orch in a new window" keybind would break the cd behavior. Typing
`orch\n` into the already-running interactive shell reuses the loaded function
and keeps one uniform behavior across all three terminals.

Trade-off (accepted): the keybind only does something useful when a shell
prompt is focused. Pressing it mid-command just types the text.

## Components

### New file: `keybind-install.sh`

Standalone, idempotent, also invoked by `install.sh`. For each of the three
terminals, if its config file or config dir already exists, insert a fenced
block containing the keybind. Terminals whose config dir does not exist are
skipped (do not create configs for terminals the user doesn't use). If none of
the three exist, print a note and exit 0.

Fence (all three config formats accept `#` comments, so this is uniform):

```
# >>> orch keybind >>>
<terminal-specific line(s)>
# <<< orch keybind <<<
```

Idempotency: before inserting, delete any existing fenced block in that file,
then append the current block. Re-running never duplicates.

Per-terminal payload (shown for the default chord `ctrl+alt+o`):

- **Ghostty** — `~/.config/ghostty/config`
  ```
  keybind = ctrl+alt+o=text:orch\n
  ```
- **kitty** — `~/.config/kitty/kitty.conf`
  ```
  map ctrl+alt+o send_text all orch\r
  ```
- **Alacritty** — `~/.config/alacritty/alacritty.toml` (modern TOML only; the
  legacy YAML `key_bindings` form is not targeted)
  ```toml
  [[keyboard.bindings]]
  key = "O"
  mods = "Control|Alt"
  chars = "orch\r"
  ```

### Chord selection

The chord is user-configurable, not hardcoded. `keybind-install.sh` accepts an
optional first argument: `keybind-install.sh [CHORD]`, where `CHORD` is
Ghostty-style (`mod1+mod2+key`, e.g. `ctrl+alt+o`, `super+o`). Default when
omitted: `ctrl+alt+o` (chosen because `ctrl+shift+o` collides with Ghostty's
default new-split binding).

The script parses `CHORD` into:
- **Ghostty/kitty form**: used as-is (`ctrl+alt+o`).
- **Alacritty form**: split into `mods` (each modifier capitalized, joined
  with `|` — `ctrl`→`Control`, `alt`→`Alt`, `shift`→`Shift`, `super`→`Super`)
  and `key` (the final token, uppercased — `o` → `O`).

Only single-chord input is supported (no `>`-separated leader sequences);
Alacritty has no sequence support, so the plan keeps one form across all three
terminals rather than special-casing Ghostty/kitty with a leader key.

`install.sh` prompts for the chord before calling `keybind-install.sh`:
`Keybind chord? [ctrl+alt+o]:` — Enter accepts the default, or the user types
a replacement in the same `mod+mod+key` form.

### `install.sh` integration

After the existing copy/rc steps, either:
- prompt `Install a terminal keybind for detected terminals? [y/N]`, then on
  yes prompt `Keybind chord? [ctrl+alt+o]:` (Enter = default), or
- skip both prompts when run non-interactively or with a `--keybind` /
  `--no-keybind` flag (non-interactive default: don't install).

On yes, call `keybind-install.sh [CHORD]` (CHORD omitted if the user accepted
the default — the script's own default applies either way).

### `uninstall.sh` integration

For each of the three config files, if present, delete the fenced
`# >>> orch keybind >>>` ... `# <<< orch keybind <<<` block. Leave the rest of
each config untouched.

### README

New "Terminal keybind" section: what the keybind does, the three supported
terminals, how to change the chord, and a note that other terminals can be
wired by hand or via PR.

## Error handling

- Missing config dir/file for a terminal → skip that terminal silently (report
  in summary line).
- Config file exists but is read-only / write fails → print a warning naming
  the file, continue with the others, exit non-zero at the end.
- No supported terminal found → print note, exit 0.

## Testing

- Fixture config dirs in a temp `$HOME`; run `keybind-install.sh`, assert the
  fenced block is present and correct for each format, using the default
  chord.
- Run it twice; assert exactly one block (idempotency).
- Run `keybind-install.sh ctrl+shift+k` (custom chord); assert Ghostty/kitty
  lines use `ctrl+shift+k` and Alacritty's `mods`/`key` reflect it
  (`Control|Shift` / `K`).
- Run `uninstall.sh`; assert block removed and surrounding config intact,
  regardless of which chord was installed.
- Absent-config case: no file created, exit 0.
