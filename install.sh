#!/usr/bin/env bash
# Installs worktree-orch:
#   - orch, orch-helper.sh -> ~/.local/bin (real executable, on $PATH like mise/nvim)
#   - worktree-tasks.sh, orch.sh -> ~/scripts (sourced from your shell rc)
#   - ~/.orch.conf from the example, if you don't already have one
# Works with bash or zsh.
set -eu

KEYBIND=ask   # ask | yes | no
for arg in "$@"; do
  case "$arg" in
    --keybind)    KEYBIND=yes ;;
    --no-keybind) KEYBIND=no ;;
  esac
done

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_DEST="$HOME/.local/bin"
SCRIPTS_DEST="$HOME/scripts"

mkdir -p "$BIN_DEST" "$SCRIPTS_DEST"

cp "$DIR/orch" "$BIN_DEST/orch"
cp "$DIR/orch-helper.sh" "$BIN_DEST/orch-helper.sh"
chmod +x "$BIN_DEST/orch" "$BIN_DEST/orch-helper.sh"

cp "$DIR/worktree-tasks.sh" "$SCRIPTS_DEST/worktree-tasks.sh"
cp "$DIR/orch.sh" "$SCRIPTS_DEST/orch.sh"

if [[ ! -f "$HOME/.orch.conf" ]]; then
  cp "$DIR/orch.conf.example" "$HOME/.orch.conf"
  echo "Created ~/.orch.conf — edit it to set your favorite repos and per-repo hooks."
else
  echo "~/.orch.conf already exists, left untouched."
fi

case ":$PATH:" in
  *":$BIN_DEST:"*) ;;
  *) echo "NOTE: $BIN_DEST is not on your \$PATH — add: export PATH=\"$BIN_DEST:\$PATH\"" ;;
esac

for rc in "$HOME/.bashrc" "$HOME/.zshrc"; do
  [[ -f "$rc" ]] || continue
  if ! grep -q "source ~/scripts/worktree-tasks.sh" "$rc" 2>/dev/null; then
    echo "source ~/scripts/worktree-tasks.sh" >> "$rc"
    echo "Added 'source ~/scripts/worktree-tasks.sh' to $rc"
  fi
  if ! grep -q "source ~/scripts/orch.sh" "$rc" 2>/dev/null; then
    echo "source ~/scripts/orch.sh" >> "$rc"
    echo "Added 'source ~/scripts/orch.sh' to $rc"
  fi
done

if [[ "$KEYBIND" == "ask" ]]; then
  if [[ -t 0 ]]; then
    read -r -p "Install a terminal keybind for detected terminals (ghostty/kitty/alacritty)? [y/N] " reply || reply=n
    [[ "$reply" == [yY]* ]] && KEYBIND=yes || KEYBIND=no
  else
    KEYBIND=no
  fi
fi
if [[ "$KEYBIND" == "yes" ]]; then
  CHORD=ctrl+alt+o
  if [[ -t 0 ]]; then
    read -r -p "Keybind chord? [ctrl+alt+o]: " reply || reply=""
    [[ -n "$reply" ]] && CHORD="$reply"
  fi
  echo "Using chord: $CHORD"
  "$DIR/keybind-install.sh" "$CHORD"
fi

echo
echo "Done. Requires: tmux, fzf."
echo "If you already have your own new-task/end-task functions, remove the"
echo "worktree-tasks.sh source line from your shell rc to keep using yours."
echo "Restart your shell (or re-source your rc file), then run: orch"
