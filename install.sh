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

# ask_yn <prompt> <default: y|n> — strict y/Y/n/N/empty only, re-prompts
# on anything else. Prints result via $REPLY_YN (y or n).
ask_yn() {
  local prompt="$1" default="$2" reply
  while true; do
    read -r -p "$prompt" reply || reply="$default"
    case "$reply" in
      y|Y) REPLY_YN=y; return ;;
      n|N) REPLY_YN=n; return ;;
      "")  REPLY_YN="$default"; return ;;
      *)   echo "Please answer y or n." ;;
    esac
  done
}

if [[ "$KEYBIND" != "no" && -t 0 ]]; then
  if command -v tmux >/dev/null 2>&1; then
    ask_yn "Add a tmux keybind for orch (prefix + key opens it in a pane on top of current pan, same rules as other tmux commands)? [Y/n] " y
    if [[ "$REPLY_YN" == y ]]; then
      TMUX_KEY=o
      read -r -p "tmux key (after prefix)? [o]: " reply || reply=""
      [[ -n "$reply" ]] && TMUX_KEY="$reply"
      echo "Using tmux key: prefix + $TMUX_KEY"
      "$DIR/keybind-install.sh" tmux ctrl+alt+o "$TMUX_KEY"
    fi
  fi

  ask_yn "Install a terminal-emulator keybind too? [Y/n] " y
  if [[ "$REPLY_YN" == y ]]; then
    picks=""
    if command -v fzf >/dev/null 2>&1; then
      while [[ -z "$picks" ]]; do
        picks="$(printf 'Ghostty\nkitty\nAlacritty\n' | fzf --multi \
          --bind 'space:toggle+down' \
          --header 'space: toggle selection, enter: confirm (pick at least one)' \
          || true)"
      done
    else
      echo "fzf not found — falling back to comma-separated numbers."
      echo "Which terminal(s)? (comma-separated numbers)"
      echo "  1) Ghostty"
      echo "  2) kitty"
      echo "  3) Alacritty"
      picknums=""
      while [[ -z "$picknums" ]]; do
        read -r -p "> " picknums || picknums=""
      done
      IFS=',' read -r -a PICK_NUMS <<<"$picknums"
      for n in "${PICK_NUMS[@]}"; do
        n="$(echo "$n" | tr -d '[:space:]')"
        case "$n" in
          1) t=Ghostty ;;
          2) t=kitty ;;
          3) t=Alacritty ;;
          *) t="" ;;
        esac
        [[ -n "$t" ]] && picks="${picks:+$picks
}$t"
      done
    fi

    TERMLIST=""
    while IFS= read -r pick; do
      case "$pick" in
        Ghostty)   t=ghostty ;;
        kitty)     t=kitty ;;
        Alacritty) t=alacritty ;;
        *)         t="" ;;
      esac
      [[ -n "$t" ]] && TERMLIST="${TERMLIST:+$TERMLIST,}$t"
    done <<< "$picks"

    if [[ -z "$TERMLIST" ]]; then
      echo "No valid terminal selected — skipping terminal-emulator keybind install."
    else
      CHORD=ctrl+alt+o
      read -r -p "Keybind chord? [ctrl+alt+o]: " reply || reply=""
      [[ -n "$reply" ]] && CHORD="$reply"
      echo "Using chord: $CHORD"
      "$DIR/keybind-install.sh" "$TERMLIST" "$CHORD"
    fi
  fi
elif [[ "$KEYBIND" != "no" ]]; then
  echo "NOTE: no terminal to prompt for (non-interactive) — skipping keybind install."
fi

echo
echo "Done. Requires: tmux, fzf."
echo "If you already have your own new-task/end-task functions, remove the"
echo "worktree-tasks.sh source line from your shell rc to keep using yours."
echo "Restart your shell (or re-source your rc file), then run: orch"
