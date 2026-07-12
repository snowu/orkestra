#!/usr/bin/env bash
# Adds a keybind (default ctrl+alt+o) that types `orch` + Enter into the
# focused shell, for each supported terminal whose config dir already
# exists:
#   Ghostty   ~/.config/ghostty/config
#   kitty     ~/.config/kitty/kitty.conf
#   Alacritty ~/.config/alacritty/alacritty.toml
# Usage: keybind-install.sh [CHORD]   (CHORD like "ctrl+alt+o", "super+o")
# Idempotent: an existing "# >>> orch keybind >>>" block is replaced.
# Other terminals: add the equivalent yourself, or open a PR.
set -eu

CHORD="${1:-ctrl+alt+o}"
FENCE_OPEN='# >>> orch keybind >>>'
FENCE_CLOSE='# <<< orch keybind <<<'
ERRORS=0
INSTALLED=""

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

# add_keybind <config-dir> <config-file> <name> <payload...>
add_keybind() {
  local dir="$1" file="$2" name="$3"; shift 3
  if [[ ! -d "$dir" ]]; then
    echo "skip: $name (no $dir)"
    return 0
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
  INSTALLED=1
}

read -r ALA_MODS ALA_KEY <<<"$(alacritty_mods_key "$CHORD")"

add_keybind "$HOME/.config/ghostty" "$HOME/.config/ghostty/config" ghostty \
  "keybind = ${CHORD}=text:orch\\n"

add_keybind "$HOME/.config/kitty" "$HOME/.config/kitty/kitty.conf" kitty \
  "map ${CHORD} send_text all orch\\r"

add_keybind "$HOME/.config/alacritty" "$HOME/.config/alacritty/alacritty.toml" alacritty \
  '[[keyboard.bindings]]' \
  "key = \"${ALA_KEY}\"" \
  "mods = \"${ALA_MODS}\"" \
  'chars = "orch\r"'

echo "Chord: $CHORD"
if [[ -z "$INSTALLED" && "$ERRORS" -eq 0 ]]; then
  echo "No supported terminal config found (ghostty/kitty/alacritty) — nothing to do."
fi
[[ -n "$INSTALLED" ]] && echo "Reload/restart your terminal to pick up the keybind."
exit "$ERRORS"
