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

count_fences() { grep -c '^# >>> orch keybind >>>$' "$1" 2>/dev/null || true; }

# --- case 1: all three terminals present, default chord -----------------
fresh_home
mkdir -p "$TESTHOME/.config/ghostty" "$TESTHOME/.config/kitty" "$TESTHOME/.config/alacritty"
echo 'font-size = 14' > "$TESTHOME/.config/ghostty/config"
echo 'font_size 14'   > "$TESTHOME/.config/kitty/kitty.conf"
printf '[font]\nsize = 14\n' > "$TESTHOME/.config/alacritty/alacritty.toml"

HOME="$TESTHOME" bash "$DIR/keybind-install.sh" >/dev/null
assert "installer exits 0 with terminals present" test $? -eq 0

assert "ghostty keybind line present" \
  grep -q '^keybind = ctrl+alt+o=text:orch\\n$' "$TESTHOME/.config/ghostty/config"
assert "kitty send_text line present" \
  grep -q '^map ctrl+alt+o send_text all orch\\r$' "$TESTHOME/.config/kitty/kitty.conf"
assert "alacritty binding table present" \
  grep -q '^\[\[keyboard.bindings\]\]$' "$TESTHOME/.config/alacritty/alacritty.toml"
assert "alacritty mods present" \
  grep -q '^mods = "Control|Alt"$' "$TESTHOME/.config/alacritty/alacritty.toml"
assert "alacritty key present" \
  grep -q '^key = "O"$' "$TESTHOME/.config/alacritty/alacritty.toml"
assert "alacritty chars present" \
  grep -q 'chars = "orch\\r"' "$TESTHOME/.config/alacritty/alacritty.toml"
assert "ghostty existing config preserved" \
  grep -q '^font-size = 14$' "$TESTHOME/.config/ghostty/config"

# --- case 2: idempotency ------------------------------------------------
HOME="$TESTHOME" bash "$DIR/keybind-install.sh" >/dev/null
assert "ghostty: one fence after rerun"    test "$(count_fences "$TESTHOME/.config/ghostty/config")" = 1
assert "kitty: one fence after rerun"      test "$(count_fences "$TESTHOME/.config/kitty/kitty.conf")" = 1
assert "alacritty: one fence after rerun"  test "$(count_fences "$TESTHOME/.config/alacritty/alacritty.toml")" = 1

# --- case 3: config dir exists but file missing -> file created ---------
fresh_home
mkdir -p "$TESTHOME/.config/ghostty"
HOME="$TESTHOME" bash "$DIR/keybind-install.sh" >/dev/null
assert "ghostty config created when dir exists" test -f "$TESTHOME/.config/ghostty/config"
assert "kitty untouched (dir absent)" test ! -e "$TESTHOME/.config/kitty/kitty.conf"

# --- case 4: no terminals -> exit 0, nothing created --------------------
fresh_home
HOME="$TESTHOME" bash "$DIR/keybind-install.sh" >/dev/null
assert "no-terminal case exits 0" test $? -eq 0
assert "nothing created" test ! -e "$TESTHOME/.config/ghostty"

# --- case 5: uninstall removes fence, keeps rest ------------------------
fresh_home
mkdir -p "$TESTHOME/.config/ghostty"
echo 'font-size = 14' > "$TESTHOME/.config/ghostty/config"
HOME="$TESTHOME" bash "$DIR/keybind-install.sh" >/dev/null
HOME="$TESTHOME" bash "$DIR/uninstall.sh" >/dev/null
assert "uninstall removes fence" \
  test "$(count_fences "$TESTHOME/.config/ghostty/config")" = 0
assert "uninstall keeps user config" \
  grep -q '^font-size = 14$' "$TESTHOME/.config/ghostty/config"
assert "uninstall removes keybind line" \
  bash -c "! grep -q 'text:orch' '$TESTHOME/.config/ghostty/config'"

# --- case 6: reinstall with a different chord replaces, not stacks ------
fresh_home
mkdir -p "$TESTHOME/.config/ghostty"
HOME="$TESTHOME" bash "$DIR/keybind-install.sh" ctrl+alt+o >/dev/null
HOME="$TESTHOME" bash "$DIR/keybind-install.sh" ctrl+shift+k >/dev/null
assert "one fence after chord change" \
  test "$(count_fences "$TESTHOME/.config/ghostty/config")" = 1
assert "old chord gone after chord change" \
  bash -c "! grep -q 'ctrl+alt+o=text:orch' '$TESTHOME/.config/ghostty/config'"
assert "new chord present after chord change" \
  grep -q '^keybind = ctrl+shift+k=text:orch\\n$' "$TESTHOME/.config/ghostty/config"

echo
if [[ "$FAILS" -eq 0 ]]; then echo "ALL PASS"; else echo "$FAILS FAILURES"; exit 1; fi
