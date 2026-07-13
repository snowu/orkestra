#!/usr/bin/env bash
# Rebuilds/reinstalls ork's executables from this repo without touching
# config or asking any questions — for "I edited ork-helper.sh, now put it
# where the shell actually runs it from" during dev. Errors out if install.sh
# was never run (no point rebuilding a target that doesn't exist yet).
set -eu

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_DEST="$HOME/.local/bin"
SCRIPTS_DEST="$HOME/scripts"

if [[ ! -f "$BIN_DEST/ork" ]]; then
  echo "error: $BIN_DEST/ork not found — run ./install.sh first" >&2
  exit 1
fi

cp "$DIR/ork" "$BIN_DEST/ork"
cp "$DIR/ork-helper.sh" "$BIN_DEST/ork-helper.sh"
chmod +x "$BIN_DEST/ork" "$BIN_DEST/ork-helper.sh"

cp "$DIR/worktree-tasks.sh" "$SCRIPTS_DEST/worktree-tasks.sh"
cp "$DIR/ork.sh" "$SCRIPTS_DEST/ork.sh"

echo "ork rebuilt -> $BIN_DEST, worktree-tasks.sh/ork.sh -> $SCRIPTS_DEST"
