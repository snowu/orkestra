#!/usr/bin/env bash
# Rebuilds/reinstalls ork from this repo without touching config or asking
# any questions. Builds the Go binary (source of truth for the picker and
# worktree ops) and refreshes the shell shims. Errors out if install.sh was
# never run (no point rebuilding a target that doesn't exist yet).
set -eu

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_DEST="$HOME/.local/bin"
SCRIPTS_DEST="$HOME/scripts"

if [[ ! -f "$BIN_DEST/ork" ]]; then
  echo "error: $BIN_DEST/ork not found — run ./install.sh first" >&2
  exit 1
fi

if ! command -v go &>/dev/null; then
  echo "error: go not found — install Go (e.g. mise use -g go@latest)" >&2
  exit 1
fi

# Built to bin/ first: `go build -o $BIN_DEST/ork` directly would leave a
# half-written binary on a failed build.
(cd "$DIR" && go build -o bin/ork ./cmd/ork)
cp "$DIR/bin/ork" "$BIN_DEST/ork"
chmod +x "$BIN_DEST/ork"

cp "$DIR/worktree-tasks.sh" "$SCRIPTS_DEST/worktree-tasks.sh"
cp "$DIR/ork.sh" "$SCRIPTS_DEST/ork.sh"

echo "ork (Go) rebuilt -> $BIN_DEST/ork, worktree-tasks.sh/ork.sh -> $SCRIPTS_DEST"
