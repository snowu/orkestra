# Agent state column

`ork` shows a live per-worktree agent state (running/waiting/needs
input) in its picker, colored green/cyan/yellow. This is driven entirely by
Claude Code hooks pushing state to `~/.cache/ork/agent-state/<tmux-session>`
— `ork` itself just reads that file (see the `rows()` case in
`ork-helper.sh` around line 262).

This is **not wired up by the installer on purpose** — it edits your global
`~/.claude/settings.json`, which we don't want to touch automatically for
other people's machines. Set it up manually if you want it; takes a couple
minutes.

If you're already on a recent-ish Claude Code version, the `ork-helper.sh`
side of this (reading the state file, coloring the column) works out of the
box with no setup — it's just a no-op until something writes to
`~/.cache/ork/agent-state/<session>`. The manual step below is only needed
to make Claude Code itself start writing that state.

## Setup

1. `hooks/ork-agent-state.sh` in this repo is the script. Symlink or copy
   it somewhere stable, e.g.:
   ```sh
   ln -s "$(pwd)/hooks/ork-agent-state.sh" ~/.claude/hooks/ork-agent-state.sh
   ```

2. Add these hook entries to your `~/.claude/settings.json` (merge into
   your existing `"hooks"` block — don't overwrite it):

   ```json
   "hooks": {
     "UserPromptSubmit": [{ "hooks": [{ "type": "command", "command": "~/.claude/hooks/ork-agent-state.sh running" }] }],
     "PreToolUse":       [{ "hooks": [{ "type": "command", "command": "~/.claude/hooks/ork-agent-state.sh running" }] }],
     "PostToolUse":      [{ "hooks": [{ "type": "command", "command": "~/.claude/hooks/ork-agent-state.sh running" }] }],
     "Stop":             [{ "hooks": [{ "type": "command", "command": "~/.claude/hooks/ork-agent-state.sh waiting" }] }],
     "Notification":     [{ "hooks": [{ "type": "command", "command": "~/.claude/hooks/ork-agent-state.sh input" }] }],
     "PermissionRequest":[{ "hooks": [{ "type": "command", "command": "~/.claude/hooks/ork-agent-state.sh input" }] }]
   }
   ```

3. Fully restart Claude Code (not `--resume`) — new hook event types
   (`PermissionRequest`, `PostToolUse`) only load on a fresh session start.

## Why both `PermissionRequest`+`Notification`, and `PostToolUse`+`PreToolUse`

- `Notification` (`permission_prompt`) fires ~6s late — it's gated behind
  Claude Code's OS-notification debounce, not configurable. `PermissionRequest`
  fires the instant the dialog appears, so it's the fast path; `Notification`
  is kept as a harmless redundant backup (same end state, just slower if the
  other one didn't fire for whatever reason).
- Without `PostToolUse`, after you answer a permission prompt or an
  `AskUserQuestion`, the state stays on "needs input" until the *next* turn's
  `PreToolUse`/`UserPromptSubmit` — visibly stuck for a beat. `PostToolUse`
  fires the instant the gated tool call completes, so `running` reappears
  immediately.

All four states (`running`/`waiting`/`input` transitions) are now
synchronous — no debounce, no stale lag in either direction.

## Uninstall

Just remove the six hook entries from `~/.claude/settings.json` and delete
the symlink. `ork` degrades gracefully — the STATE column just won't
update; nothing else depends on it.
