#!/usr/bin/env bash
# Reverses install.sh: removes ork/ork-helper.sh from ~/.local/bin,
# worktree-tasks.sh/ork.sh from ~/scripts, and the matching `source` lines
# from ~/.bashrc and ~/.zshrc. Leaves ~/.ork.conf alone unless --purge-config
# is passed (it's your data — favorites/hooks you may have customized).
set -eu

BIN_DEST="$HOME/.local/bin"
SCRIPTS_DEST="$HOME/scripts"
PURGE_CONFIG=0

for arg in "$@"; do
  [[ "$arg" == "--purge-config" ]] && PURGE_CONFIG=1
done

rm -f "$BIN_DEST/ork" "$BIN_DEST/ork-helper.sh" "$BIN_DEST/orc.cow"
rm -f "$SCRIPTS_DEST/worktree-tasks.sh" "$SCRIPTS_DEST/ork.sh"
echo "Removed ork, ork-helper.sh, worktree-tasks.sh, ork.sh"

# Reverses install.sh's hook step: remove the hook file itself, and strip
# only the entries in settings.json whose command references it — matched
# the same way install.sh added them (by event+state pair), so any OTHER
# hooks a user has configured on the same events (their own
# UserPromptSubmit hook, say) are left completely untouched.
CLAUDE_HOOKS_DIR="$HOME/.claude/hooks"
CLAUDE_SETTINGS="$HOME/.claude/settings.json"
rm -f "$CLAUDE_HOOKS_DIR/ork-agent-state.sh" "$CLAUDE_HOOKS_DIR/orch-agent-state.sh"
echo "Removed ork-agent-state.sh from $CLAUDE_HOOKS_DIR"

if [[ -f "$CLAUDE_SETTINGS" ]] && command -v python3 >/dev/null 2>&1; then
  python3 - "$CLAUDE_SETTINGS" <<'PYEOF'
import json, sys

settings_path = sys.argv[1]

with open(settings_path) as f:
    settings = json.load(f)

hooks = settings.get("hooks")
if hooks:
    changed = False
    for event in list(hooks.keys()):
        entries = hooks[event]
        kept = [
            e for e in entries
            if not any(
                "ork-agent-state.sh" in h.get("command", "") or "orch-agent-state.sh" in h.get("command", "")
                for h in e.get("hooks", [])
            )
        ]
        if len(kept) != len(entries):
            changed = True
        if kept:
            hooks[event] = kept
        else:
            del hooks[event]
    if changed:
        with open(settings_path, "w") as f:
            json.dump(settings, f, indent=2)
            f.write("\n")
PYEOF
  echo "Removed ork's hook entries from $CLAUDE_SETTINGS (other hooks left untouched)"
fi

for rc in "$HOME/.bashrc" "$HOME/.zshrc"; do
  [[ -f "$rc" ]] || continue
  sed -i.bak \
    -e '/^source ~\/scripts\/worktree-tasks\.sh$/d' \
    -e '/^source ~\/scripts\/ork\.sh$/d' \
    "$rc"
  rm -f "$rc.bak"
  echo "Cleaned source lines from $rc"
done

if [[ "$PURGE_CONFIG" -eq 1 ]]; then
  rm -f "$HOME/.ork.conf" "$HOME/.config/ork/hooks.json"
  echo "Removed ~/.ork.conf, ~/.config/ork/hooks.json"
elif [[ -f "$HOME/.ork.conf" ]]; then
  # Strip only the ORK_WORKTREES_ROOTS line install.sh wrote (plus a
  # leftover ORK_CODE_ROOTS from an older install, if present — that key
  # is dead config now, repos are discovered by scanning $HOME live) — both
  # point at THIS install's chosen folders and are meaningless once ork
  # itself is gone. ORK_FAVORITES/ORK_HOOK_*/ORK_SCOPE_SESSIONS_TO_REPO
  # are the user's own customization and shouldn't be touched by an
  # uninstall.
  sed -i.bak \
    -e '/^ORK_CODE_ROOTS=/d' \
    -e '/^ORK_WORKTREES_ROOTS=/d' \
    "$HOME/.ork.conf"
  rm -f "$HOME/.ork.conf.bak"
  echo "Left ~/.ork.conf in place (pass --purge-config to remove it too)"
  echo "but removed ORK_WORKTREES_ROOTS (specific to this install; your"
  echo "favorites/hooks/other settings are untouched)."
else
  echo "No ~/.ork.conf found."
fi

# Remove the keybind block installed by keybind-install.sh, if any.
for cfg in "$HOME/.tmux.conf" \
           "$HOME/.config/ghostty/config" \
           "$HOME/.config/kitty/kitty.conf" \
           "$HOME/.config/alacritty/alacritty.toml"; do
  [[ -f "$cfg" ]] || continue
  grep -qF '# >>> ork keybind >>>' "$cfg" || continue

  # For tmux specifically: capture the bound key from the fenced block
  # BEFORE stripping it, so we can unbind it from any live server below —
  # `tmux source-file` only applies bindings present in the file, it does
  # NOT remove a key that's simply absent from the new content (confirmed
  # live), so without an explicit unbind the stale binding stays active
  # until the server restarts.
  if [[ "$cfg" == "$HOME/.tmux.conf" ]]; then
    tmux_key="$(sed -n '/^# >>> ork keybind >>>$/,/^# <<< ork keybind <<<$/p' "$cfg" | \
      grep -o '^bind-key [^ ]*' | awk '{print $2}')"
  fi

  tmp="$(mktemp)"
  awk '
    $0 == "# >>> ork keybind >>>" { skip = 1; next }
    $0 == "# <<< ork keybind <<<" { skip = 0; next }
    !skip
  ' "$cfg" > "$tmp" && mv "$tmp" "$cfg"
  echo "Removed ork keybind from $cfg"
done

# Unbind live, if a tmux server is running — see comment above for why
# source-file alone isn't enough.
if [[ -n "${tmux_key:-}" ]] && command -v tmux >/dev/null 2>&1 && tmux list-sessions >/dev/null 2>&1; then
  tmux unbind-key -T prefix "$tmux_key" 2>/dev/null || true
  if tmux source-file "$HOME/.tmux.conf" >/dev/null 2>&1; then
    echo "tmux: unbound prefix + $tmux_key and reloaded ~/.tmux.conf live."
  else
    echo "WARNING: unbound prefix + $tmux_key but 'tmux source-file ~/.tmux.conf' failed — reload manually." >&2
  fi
fi

rm -f /tmp/ork.log /tmp/ork-new-task.marker

# Cache dirs (agent-state, fzf-ports, repo-scan, access) — all disposable,
# safe to nuke unconditionally regardless of --purge-config (this is
# scratch state ork rebuilds on its own, not user customization like
# ~/.ork.conf is).
if [[ -d "$HOME/.cache/ork" ]]; then
  rm -rf "$HOME/.cache/ork"
  echo "Removed ~/.cache/ork (agent-state, fzf-ports, repo-scan cache)"
fi

echo
echo "Done. Restart your shell (or re-source your rc file) to pick up the change."
