#!/usr/bin/env bash
# Rebuilds/reinstalls orch's executables from this repo without touching
# config or asking any questions — for "I edited orch-helper.sh, now put it
# where the shell actually runs it from" during dev. Errors out if install.sh
# was never run (no point rebuilding a target that doesn't exist yet).
set -eu

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_DEST="$HOME/.local/bin"
SCRIPTS_DEST="$HOME/scripts"

if [[ ! -f "$BIN_DEST/orch" ]]; then
  echo "error: $BIN_DEST/orch not found — run ./install.sh first" >&2
  exit 1
fi

cp "$DIR/orch" "$BIN_DEST/orch"
cp "$DIR/orch-helper.sh" "$BIN_DEST/orch-helper.sh"
chmod +x "$BIN_DEST/orch" "$BIN_DEST/orch-helper.sh"

cp "$DIR/worktree-tasks.sh" "$SCRIPTS_DEST/worktree-tasks.sh"
cp "$DIR/orch.sh" "$SCRIPTS_DEST/orch.sh"

echo "orch rebuilt -> $BIN_DEST, worktree-tasks.sh/orch.sh -> $SCRIPTS_DEST"
