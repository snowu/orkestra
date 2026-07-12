#!/usr/bin/env bash
# Wires a keybind that opens the `ork` picker, for whichever terminal(s)
# you name.
#
# Usage: keybind-install.sh <terminal>[,<terminal>...] [CHORD] [TMUX_KEY]
#   terminal: tmux | ghostty | kitty | alacritty
#   CHORD:    e.g. ctrl+alt+o, super+o (default: ctrl+alt+o) — used by
#             ghostty/kitty/alacritty.
#   TMUX_KEY: single key pressed after the tmux prefix, e.g. o (default: o)
#             — used only by tmux. Kept separate from CHORD so tmux's
#             prefix-table binding never shares (or collides with) the
#             standalone terminal-emulator chord.
#
# tmux: opens `ork` in a floating popup over whatever pane is focused, on
#   prefix + TMUX_KEY (tmux's own prefix system, e.g. ctrl-b then the key —
#   never fires unless you've pressed the prefix first, and never leaks
#   into the pane's own program). Recommended.
# Ghostty/kitty/Alacritty: injects `ork` + Enter into the focused shell
#   (existing ork() wrapper's cd-on-exit applies). Config must already
#   exist for that terminal, or this warns and fails for that terminal.
#
# Only terminals with their own built-in keybind engine are supported —
# gnome-terminal and macOS Terminal.app have no such mechanism (they rely
# on an external, desktop-environment-specific global-hotkey system, which
# this script can't reliably configure). Wire those by hand in your WM/DE.
#
# Idempotent: rerunning (same or different chord/key) replaces the previous
# binding for each terminal named, it never stacks duplicates.
# Other terminals: add the equivalent yourself, or open a PR.
set -eu

usage() {
  echo "Usage: keybind-install.sh <terminal>[,<terminal>...] [CHORD] [TMUX_KEY]" >&2
  echo "  terminal: tmux | ghostty | kitty | alacritty" >&2
  echo "  CHORD:    e.g. ctrl+alt+o (default: ctrl+alt+o) — non-tmux terminals" >&2
  echo "  TMUX_KEY: single key after tmux prefix, e.g. o (default: o) — tmux only" >&2
}

if [[ $# -lt 1 ]]; then
  usage
  exit 1
fi

TERMLIST="$1"
CHORD="${2:-ctrl+alt+o}"
TMUX_KEY="${3:-o}"
FENCE_OPEN='# >>> ork keybind >>>'
FENCE_CLOSE='# <<< ork keybind <<<'
ERRORS=0

# alacritty_mods_key <chord> — prints "Mod1|Mod2 KEY" (space-separated)
alacritty_mods_key() {
  local chord="$1" key mods="" part
  key="${chord##*+}"
  chord="${chord%+*}"
  while [[ "$chord" == *+* || -n "$chord" ]]; do
    part="${chord%%+*}"
    case "$part" in
      ctrl)  part="Control" ;;
      alt)   part="Alt" ;;
      shift) part="Shift" ;;
      super) part="Super" ;;
      *)     part="$(tr '[:lower:]' '[:upper:]' <<<"${part:0:1}")${part:1}" ;;
    esac
    mods="${mods:+$mods|}$part"
    [[ "$chord" == *+* ]] || break
    chord="${chord#*+}"
  done
  printf '%s %s\n' "$mods" "$(tr '[:lower:]' '[:upper:]' <<<"$key")"
}

# remove_fence <file> — strip any existing fenced block (in place)
remove_fence() {
  local f="$1" tmp
  grep -qF "$FENCE_OPEN" "$f" || return 0
  tmp="$(mktemp)"
  awk -v fo="$FENCE_OPEN" -v fc="$FENCE_CLOSE" '
    $0 == fo { skip = 1; next }
    $0 == fc { skip = 0; next }
    !skip
  ' "$f" > "$tmp" && mv "$tmp" "$f"
}

# add_inject_keybind <config-dir> <config-file> <name> <payload...>
# config-dir "" means "no directory prerequisite" (used for tmux, whose
# config file lives directly in $HOME, which always exists).
add_inject_keybind() {
  local dir="$1" file="$2" name="$3"; shift 3
  if [[ -n "$dir" && ! -d "$dir" ]]; then
    echo "WARNING: $name config dir not found ($dir) — skipping $name" >&2
    ERRORS=1; return 0
  fi
  if ! { [[ -f "$file" ]] || : > "$file"; } 2>/dev/null; then
    echo "WARNING: cannot create $file — skipped $name" >&2
    ERRORS=1; return 0
  fi
  if ! { remove_fence "$file" && {
          [[ -s "$file" && $(tail -c1 "$file") != "" ]] && printf '\n'
          printf '%s\n' "$FENCE_OPEN"
          printf '%s\n' "$@"
          printf '%s\n' "$FENCE_CLOSE"
        } >> "$file"; } 2>/dev/null; then
    echo "WARNING: cannot write $file — skipped $name" >&2
    ERRORS=1; return 0
  fi
  echo "installed: $name ($file)"
}

add_tmux() {
  if ! command -v tmux >/dev/null 2>&1; then
    echo "WARNING: tmux not found on \$PATH — skipping tmux" >&2
    ERRORS=1; return 0
  fi
  # A new tmux WINDOW fills the client with zero border — unlike
  # display-popup, which in tmux 3.2a always draws a 1-cell frame around
  # itself no matter the -w/-h size (confirmed live, and there's no
  # border-removal flag until tmux 3.3+). "When the shell command
  # completes, the window closes" (tmux(1)) — no manual cleanup needed;
  # ork exiting (after ENTER/ctrl-x) closes this window automatically.
  # A window is a real part of the session, not a popup's ephemeral
  # overlay, so switch-client from inside it resolves to the real client
  # on its own — no ORK_TMUX_CLIENT targeting workaround needed here.
  add_inject_keybind "" "$HOME/.tmux.conf" tmux \
    "bind-key ${TMUX_KEY} new-window -n ork ork"
  # Reload live, if a tmux server is already running — `source-file` (unlike
  # `source`, a shell builtin) is a tmux command; it applies the new binding
  # to every attached client immediately, no restart needed.
  if tmux list-sessions >/dev/null 2>&1; then
    if tmux source-file "$HOME/.tmux.conf" >/dev/null 2>&1; then
      echo "tmux: reloaded ~/.tmux.conf — the new binding is live now."
    else
      echo "WARNING: tmux is running but 'tmux source-file ~/.tmux.conf' failed — reload manually." >&2
    fi
  fi
}

add_ghostty() {
  add_inject_keybind "$HOME/.config/ghostty" "$HOME/.config/ghostty/config" ghostty \
    "keybind = ${CHORD}=text:ork\\n"
}

add_kitty() {
  add_inject_keybind "$HOME/.config/kitty" "$HOME/.config/kitty/kitty.conf" kitty \
    "map ${CHORD} send_text all ork\\r"
}

add_alacritty() {
  local mods key
  read -r mods key <<<"$(alacritty_mods_key "$CHORD")"
  add_inject_keybind "$HOME/.config/alacritty" "$HOME/.config/alacritty/alacritty.toml" alacritty \
    '[[keyboard.bindings]]' \
    "key = \"${key}\"" \
    "mods = \"${mods}\"" \
    'chars = "ork\r"'
}

IFS=',' read -r -a TERMS <<<"$TERMLIST"
for term in "${TERMS[@]}"; do
  case "$term" in
    tmux)            add_tmux ;;
    ghostty)         add_ghostty ;;
    kitty)           add_kitty ;;
    alacritty)       add_alacritty ;;
    *)
      echo "WARNING: unknown terminal '$term' (expected: tmux, ghostty, kitty, alacritty)" >&2
      ERRORS=1
      ;;
  esac
done

if [[ "$TERMLIST" == *tmux* ]]; then
  echo "tmux key: prefix + ${TMUX_KEY}"
fi
if [[ "$TERMLIST" =~ (ghostty|kitty|alacritty) ]]; then
  echo "Chord: $CHORD"
fi
if [[ "$TERMLIST" == *ghostty* ]]; then
  echo "Ghostty: press ctrl+shift+, INSIDE Ghostty (that's the reload_config keybind, not a shell command) or restart Ghostty to pick up the change."
fi
if [[ "$TERMLIST" == *tmux* ]] && ! command -v tmux >/dev/null 2>&1; then
  : # already warned above
elif [[ "$TERMLIST" == *tmux* ]] && ! tmux list-sessions >/dev/null 2>&1; then
  echo "tmux: no server running yet — the binding will apply next time you start tmux."
fi
exit "$ERRORS"
