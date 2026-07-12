#!/usr/bin/env bash
# Reverses install.sh: removes orch/orch-helper.sh from ~/.local/bin,
# worktree-tasks.sh/orch.sh from ~/scripts, and the matching `source` lines
# from ~/.bashrc and ~/.zshrc. Leaves ~/.orch.conf alone unless --purge-config
# is passed (it's your data — favorites/hooks you may have customized).
set -eu

BIN_DEST="$HOME/.local/bin"
SCRIPTS_DEST="$HOME/scripts"
PURGE_CONFIG=0

for arg in "$@"; do
  [[ "$arg" == "--purge-config" ]] && PURGE_CONFIG=1
done

rm -f "$BIN_DEST/orch" "$BIN_DEST/orch-helper.sh"
rm -f "$SCRIPTS_DEST/worktree-tasks.sh" "$SCRIPTS_DEST/orch.sh"
echo "Removed orch, orch-helper.sh, worktree-tasks.sh, orch.sh"

for rc in "$HOME/.bashrc" "$HOME/.zshrc"; do
  [[ -f "$rc" ]] || continue
  sed -i.bak \
    -e '/^source ~\/scripts\/worktree-tasks\.sh$/d' \
    -e '/^source ~\/scripts\/orch\.sh$/d' \
    "$rc"
  rm -f "$rc.bak"
  echo "Cleaned source lines from $rc"
done

if [[ "$PURGE_CONFIG" -eq 1 ]]; then
  rm -f "$HOME/.orch.conf"
  echo "Removed ~/.orch.conf"
elif [[ -f "$HOME/.orch.conf" ]]; then
  # Strip only the ORCH_WORKTREES_ROOTS line install.sh wrote (plus a
  # leftover ORCH_CODE_ROOTS from an older install, if present — that key
  # is dead config now, repos are discovered by scanning $HOME live) — both
  # point at THIS install's chosen folders and are meaningless once orch
  # itself is gone. ORCH_FAVORITES/ORCH_HOOK_*/ORCH_SCOPE_SESSIONS_TO_REPO
  # are the user's own customization and shouldn't be touched by an
  # uninstall.
  sed -i.bak \
    -e '/^ORCH_CODE_ROOTS=/d' \
    -e '/^ORCH_WORKTREES_ROOTS=/d' \
    "$HOME/.orch.conf"
  rm -f "$HOME/.orch.conf.bak"
  echo "Left ~/.orch.conf in place (pass --purge-config to remove it too)"
  echo "but removed ORCH_WORKTREES_ROOTS (specific to this install; your"
  echo "favorites/hooks/other settings are untouched)."
else
  echo "No ~/.orch.conf found."
fi

# Remove the keybind block installed by keybind-install.sh, if any.
for cfg in "$HOME/.tmux.conf" \
           "$HOME/.config/ghostty/config" \
           "$HOME/.config/kitty/kitty.conf" \
           "$HOME/.config/alacritty/alacritty.toml"; do
  [[ -f "$cfg" ]] || continue
  grep -qF '# >>> orch keybind >>>' "$cfg" || continue

  # For tmux specifically: capture the bound key from the fenced block
  # BEFORE stripping it, so we can unbind it from any live server below —
  # `tmux source-file` only applies bindings present in the file, it does
  # NOT remove a key that's simply absent from the new content (confirmed
  # live), so without an explicit unbind the stale binding stays active
  # until the server restarts.
  if [[ "$cfg" == "$HOME/.tmux.conf" ]]; then
    tmux_key="$(sed -n '/^# >>> orch keybind >>>$/,/^# <<< orch keybind <<<$/p' "$cfg" | \
      grep -o '^bind-key [^ ]*' | awk '{print $2}')"
  fi

  tmp="$(mktemp)"
  awk '
    $0 == "# >>> orch keybind >>>" { skip = 1; next }
    $0 == "# <<< orch keybind <<<" { skip = 0; next }
    !skip
  ' "$cfg" > "$tmp" && mv "$tmp" "$cfg"
  echo "Removed orch keybind from $cfg"
done

# Unbind live, if a tmux server is running — see comment above for why
# source-file alone isn't enough.
if [[ -n "${tmux_key:-}" ]] && command -v tmux >/dev/null 2>&1 && tmux list-sessions >/dev/null 2>&1; then
  tmux unbind-key -T prefix "$tmux_key" 2>/dev/null || true
  if tmux source-file "$HOME/.tmux.conf" >/dev/null 2>&1; then
    echo "tmux: unbound prefix + $tmux_key and reloaded ~/.tmux.conf live."
  else
    echo "WARNING: unbound prefix + $tmux_key but 'tmux source-file ~/.tmux.conf' failed — reload manually." >&2
  fi
fi

rm -f /tmp/orch.log /tmp/orch-new-task.marker

echo
echo "Done. Restart your shell (or re-source your rc file) to pick up the change."
