#!/usr/bin/env bash
# Pushes agent state directly from Claude Code hook events, keyed by tmux
# session name — replaces polling/scraping tmux pane text (unreliable: the
# "tokens)" status line is only present for the ~100ms Claude Code is
# actively repainting it, so capture-pane races it and mostly loses).
set -u
STATE_DIR="$HOME/.cache/ork/agent-state"
FZF_PORTS_DIR="$HOME/.cache/ork/fzf-ports"
state=$1
[[ -n "${TMUX_PANE:-}" ]] || exit 0
sess=$(tmux display-message -p -t "$TMUX_PANE" '#S' 2>/dev/null) || exit 0
[[ -n "$sess" ]] || exit 0
mkdir -p "$STATE_DIR"
printf '%s' "$state" > "$STATE_DIR/$sess"

# Nudge every currently-open ork picker to reload — each running `ork`
# instance drops its fzf --listen port here on start, removes it on exit.
# A stale/dead port (tab closed uncleanly) just gets a failed curl, harmless.
f=
for f in "$FZF_PORTS_DIR"/*; do
  [[ -f "$f" ]] || continue
  port=$(<"$f")
  [[ -n "$port" ]] && curl -s -X POST "http://localhost:$port" -d "reload($HOME/.local/bin/ork-helper.sh rows)" >/dev/null 2>&1 &
done
