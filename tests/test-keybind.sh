#!/usr/bin/env bash
# Tests for keybind-install.sh and uninstall.sh keybind removal.
# Run: bash tests/test-keybind.sh
set -u

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FAILS=0

assert() { # assert <desc> <cmd...>
  local desc="$1"; shift
  if "$@"; then echo "ok   - $desc"; else echo "FAIL - $desc"; FAILS=$((FAILS+1)); fi
}

fresh_home() {
  TESTHOME="$(mktemp -d)"
  mkdir -p "$TESTHOME/.config"
}

count_fences() { grep -c '^# >>> ork keybind >>>$' "$1" 2>/dev/null || true; }

# --- case 1: tmux + all three inject terminals, explicit list -----------
fresh_home
mkdir -p "$TESTHOME/.config/ghostty" "$TESTHOME/.config/kitty" "$TESTHOME/.config/alacritty"
echo 'font-size = 14' > "$TESTHOME/.config/ghostty/config"
echo 'font_size 14'   > "$TESTHOME/.config/kitty/kitty.conf"
printf '[font]\nsize = 14\n' > "$TESTHOME/.config/alacritty/alacritty.toml"

HOME="$TESTHOME" bash "$DIR/keybind-install.sh" tmux,ghostty,kitty,alacritty >/dev/null
assert "installer exits 0 with all requested and present" test $? -eq 0

assert "tmux keybind line present" \
  grep -q '^bind-key o new-window -n ork ork$' "$TESTHOME/.tmux.conf"
assert "tmux keybind is prefix-table, not root (no -n flag)" \
  bash -c "! grep -q '^bind-key -n' '$TESTHOME/.tmux.conf'"
assert "ghostty keybind line present" \
  grep -q '^keybind = ctrl+alt+o=text:ork\\n$' "$TESTHOME/.config/ghostty/config"
assert "kitty send_text line present" \
  grep -q '^map ctrl+alt+o send_text all ork\\r$' "$TESTHOME/.config/kitty/kitty.conf"
assert "alacritty binding table present" \
  grep -q '^\[\[keyboard.bindings\]\]$' "$TESTHOME/.config/alacritty/alacritty.toml"
assert "alacritty mods present" \
  grep -q '^mods = "Control|Alt"$' "$TESTHOME/.config/alacritty/alacritty.toml"
assert "alacritty key present" \
  grep -q '^key = "O"$' "$TESTHOME/.config/alacritty/alacritty.toml"
assert "ghostty existing config preserved" \
  grep -q '^font-size = 14$' "$TESTHOME/.config/ghostty/config"

# --- case 2: requesting only one terminal doesn't touch the others ------
fresh_home
mkdir -p "$TESTHOME/.config/ghostty" "$TESTHOME/.config/kitty"
HOME="$TESTHOME" bash "$DIR/keybind-install.sh" ghostty >/dev/null
assert "ghostty wired when requested alone" \
  grep -q 'ork keybind' "$TESTHOME/.config/ghostty/config"
assert "kitty untouched when not requested" \
  bash -c "! grep -q 'ork keybind' '$TESTHOME/.config/kitty/kitty.conf' 2>/dev/null"
assert "tmux.conf not created when not requested" \
  test ! -e "$TESTHOME/.tmux.conf"

# --- case 3: idempotency (rerun same terminal+chord), tmux included -----
fresh_home
HOME="$TESTHOME" bash "$DIR/keybind-install.sh" tmux >/dev/null
HOME="$TESTHOME" bash "$DIR/keybind-install.sh" tmux >/dev/null
assert "tmux: one fence after rerun" test "$(count_fences "$TESTHOME/.tmux.conf")" = 1

# --- case 4: reinstall with a different chord/key replaces, not stacks --
fresh_home
HOME="$TESTHOME" bash "$DIR/keybind-install.sh" tmux ctrl+alt+o o >/dev/null
HOME="$TESTHOME" bash "$DIR/keybind-install.sh" tmux ctrl+alt+o g >/dev/null
assert "one fence after tmux key change" test "$(count_fences "$TESTHOME/.tmux.conf")" = 1
assert "old tmux key gone" bash -c "! grep -q '^bind-key o new-window' '$TESTHOME/.tmux.conf'"
assert "new tmux key present" \
  grep -q '^bind-key g new-window -n ork ork$' "$TESTHOME/.tmux.conf"

fresh_home
mkdir -p "$TESTHOME/.config/ghostty"
HOME="$TESTHOME" bash "$DIR/keybind-install.sh" ghostty ctrl+alt+o >/dev/null
HOME="$TESTHOME" bash "$DIR/keybind-install.sh" ghostty ctrl+shift+k >/dev/null
assert "one fence after ghostty chord change" \
  test "$(count_fences "$TESTHOME/.config/ghostty/config")" = 1
assert "new ghostty chord present" \
  grep -q '^keybind = ctrl+shift+k=text:ork\\n$' "$TESTHOME/.config/ghostty/config"

# --- case 5: requested terminal's prerequisite absent -> warn + non-zero -
fresh_home
HOME="$TESTHOME" bash "$DIR/keybind-install.sh" ghostty >/dev/null 2>/tmp/ork-test-stderr
rc=$?
assert "exit non-zero when requested config dir absent" test "$rc" -ne 0
assert "warning mentions ghostty" grep -qi ghostty /tmp/ork-test-stderr
assert "nothing created for absent config" test ! -e "$TESTHOME/.config/ghostty"
rm -f /tmp/ork-test-stderr

# --- case 6: no terminal argument -> usage + non-zero exit ---------------
fresh_home
HOME="$TESTHOME" bash "$DIR/keybind-install.sh" >/dev/null 2>/tmp/ork-test-stderr
assert "exit non-zero with no terminal argument" test $? -ne 0
assert "usage message printed" grep -qi usage /tmp/ork-test-stderr
rm -f /tmp/ork-test-stderr

# --- case 7: unknown terminal name -> warn + non-zero exit ---------------
fresh_home
HOME="$TESTHOME" bash "$DIR/keybind-install.sh" not-a-real-terminal >/dev/null 2>/tmp/ork-test-stderr
assert "exit non-zero on unknown terminal name" test $? -ne 0
rm -f /tmp/ork-test-stderr

# --- case 8: uninstall removes fence (ghostty + tmux), keeps rest -------
fresh_home
mkdir -p "$TESTHOME/.config/ghostty"
echo 'font-size = 14' > "$TESTHOME/.config/ghostty/config"
echo '# my tmux stuff' > "$TESTHOME/.tmux.conf"
HOME="$TESTHOME" bash "$DIR/keybind-install.sh" ghostty,tmux >/dev/null
HOME="$TESTHOME" bash "$DIR/uninstall.sh" >/dev/null
assert "uninstall removes ghostty fence" \
  test "$(count_fences "$TESTHOME/.config/ghostty/config")" = 0
assert "uninstall removes tmux fence" \
  test "$(count_fences "$TESTHOME/.tmux.conf")" = 0
assert "uninstall keeps ghostty user config" \
  grep -q '^font-size = 14$' "$TESTHOME/.config/ghostty/config"
assert "uninstall keeps tmux user config" \
  grep -q '^# my tmux stuff$' "$TESTHOME/.tmux.conf"
assert "uninstall removes ghostty keybind line" \
  bash -c "! grep -q 'text:ork' '$TESTHOME/.config/ghostty/config'"
assert "uninstall removes tmux keybind line" \
  bash -c "! grep -q 'new-window -n ork' '$TESTHOME/.tmux.conf'"

echo
if [[ "$FAILS" -eq 0 ]]; then echo "ALL PASS"; else echo "$FAILS FAILURES"; exit 1; fi
