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
else
  echo "Left ~/.orch.conf in place (pass --purge-config to remove it too)."
fi

# Remove the keybind block installed by keybind-install.sh, if any.
for cfg in "$HOME/.config/ghostty/config" \
           "$HOME/.config/kitty/kitty.conf" \
           "$HOME/.config/alacritty/alacritty.toml"; do
  [[ -f "$cfg" ]] || continue
  grep -qF '# >>> orch keybind >>>' "$cfg" || continue
  tmp="$(mktemp)"
  awk '
    $0 == "# >>> orch keybind >>>" { skip = 1; next }
    $0 == "# <<< orch keybind <<<" { skip = 0; next }
    !skip
  ' "$cfg" > "$tmp" && mv "$tmp" "$cfg"
  echo "Removed orch keybind from $cfg"
done

rm -f /tmp/orch.log /tmp/orch-new-task.marker

echo
echo "Done. Restart your shell (or re-source your rc file) to pick up the change."
