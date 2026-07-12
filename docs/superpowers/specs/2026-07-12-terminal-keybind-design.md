# Terminal keybind for `orch` — design

## Goal

Let a user press a keybind (default `ctrl+shift+o`) at a shell prompt and have
the terminal type `orch` + Enter, opening the picker. Because the keybind types
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

Per-terminal payload:

- **Ghostty** — `~/.config/ghostty/config`
  ```
  keybind = ctrl+shift+o=text:orch\n
  ```
- **kitty** — `~/.config/kitty/kitty.conf`
  ```
  map ctrl+shift+o send_text all orch\r
  ```
- **Alacritty** — `~/.config/alacritty/alacritty.toml` (modern TOML only; the
  legacy YAML `key_bindings` form is not targeted)
  ```toml
  [[keyboard.bindings]]
  key = "O"
  mods = "Control|Shift"
  chars = "orch\r"
  ```

The keybind chord is a single variable at the top of the script
(`KEYBIND_GHOSTTY`, `KEYBIND_KITTY`, `KEYBIND_ALACRITTY` derived from one
`CHORD="ctrl+shift+o"` where formats allow) so it's easy to change.

### `install.sh` integration

After the existing copy/rc steps, either:
- prompt `Install ctrl+shift+o keybind for detected terminals? [y/N]`, or
- skip the prompt when run non-interactively or with a `--keybind` /
  `--no-keybind` flag.

On yes, call `keybind-install.sh`.

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
  fenced block is present and correct for each format.
- Run it twice; assert exactly one block (idempotency).
- Run `uninstall.sh`; assert block removed and surrounding config intact.
- Absent-config case: no file created, exit 0.
