#!/usr/bin/env bash
# Installs orkestra:
#   - ork (Go binary, built here) -> ~/.local/bin (on $PATH like mise/nvim)
#   - worktree-tasks.sh, ork.sh -> ~/scripts (sourced from your shell rc)
#   - ~/.ork.conf from the example, with your code/worktree roots filled in
# Works with bash or zsh.
set -eu

KEYBIND=ask   # ask | yes | no
for arg in "$@"; do
  case "$arg" in
    --keybind)    KEYBIND=yes ;;
    --no-keybind) KEYBIND=no ;;
  esac
done

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_DEST="$HOME/.local/bin"
SCRIPTS_DEST="$HOME/scripts"

# Resolves a python that actually RUNS, not merely one that's named on $PATH.
# `command -v python3` is not enough: version managers (asdf, pyenv, mise)
# install shims that exist unconditionally and fail at exec time when no
# version is pinned ("No version is set for command python3"). Such a shim
# passes a name check and then aborts the install under `set -e`. Trying
# each candidate settles it on any machine, with or without a version
# manager. PY is empty when nothing works; callers must handle that.
PY=""
for _py in python3 python; do
  if command -v "$_py" >/dev/null 2>&1 && "$_py" -c '' >/dev/null 2>&1; then
    PY="$_py"
    break
  fi
done

# Sets KEY=value in a conf file: replaces the existing line if the key is
# present, appends it otherwise. Used for both ORK_WORKTREES_ROOTS and
# ORK_MULTIPLEXER. Falls back to grep/sed when no python is available so
# config still gets written on a machine without one — these edits are
# plain key=value lines, unlike the JSON hook wiring, which genuinely
# needs a parser.
# Usage: set_conf_key <conf_path> <key> <full_line>
set_conf_key() {
  local conf_path="$1" key="$2" line="$3"
  if [[ -n "$PY" ]]; then
    "$PY" - "$conf_path" "$key" "$line" <<'PYEOF'
import re, sys
conf_path, key, line = sys.argv[1], sys.argv[2], sys.argv[3]
text = open(conf_path).read()
pattern = r'^' + re.escape(key) + r'=.*$'
if re.search(pattern, text, flags=re.M):
    # Escapes backslashes so a value containing them survives re.sub's
    # handling of the replacement string.
    text = re.sub(pattern, line.replace('\\', '\\\\'), text, count=1, flags=re.M)
else:
    if text and not text.endswith("\n"):
        text += "\n"
    text += line + "\n"
open(conf_path, "w").write(text)
PYEOF
  elif grep -q "^$key=" "$conf_path" 2>/dev/null; then
    # Uses a control char as the sed delimiter: the value holds paths with
    # slashes, and can hold the usual punctuation alternatives too.
    sed -i "s"$'\001'"^$key=.*"$'\001'"$line"$'\001' "$conf_path"
  else
    [[ -s "$conf_path" && -n "$(tail -c1 "$conf_path")" ]] && echo "" >> "$conf_path"
    echo "$line" >> "$conf_path"
  fi
}

# Same palette as ork-helper.sh, so the install experience matches the
# picker's own look. Disabled automatically when stdout isn't a terminal
# (piped install, CI) so logs don't fill up with escape codes.
if [[ -t 1 ]]; then
  RESET=$'\033[0m'
  BOLD=$'\033[1m'
  GREEN=$'\033[38;5;114m'
  YELLOW=$'\033[38;5;179m'
  CYAN=$'\033[38;5;80m'
  DIM=$'\033[38;5;244m'
else
  RESET="" BOLD="" GREEN="" YELLOW="" CYAN="" DIM=""
fi

section() { printf '\n%s%s%s\n' "${BOLD}${CYAN}" "== $1 ==" "$RESET"; }
subsection() { printf '%s%s%s\n' "${BOLD}" "-- $1 --" "$RESET"; }
note() { printf '%s%s%s\n' "$YELLOW" "$1" "$RESET"; }
dim() { printf '%s%s%s\n' "$DIM" "$1" "$RESET"; }
ok() { printf '%s%s%s\n' "$GREEN" "$1" "$RESET"; }
# Colored prompt for `read -r -p` call sites — printed to stderr like the
# rest of this file's prompt text, since a couple of call sites capture
# stdout from surrounding command substitutions.
prompt() { printf '%s%s%s' "$CYAN" "$1" "$RESET" >&2; }

# ask_yn <prompt ending in "[Y/n] " or "[y/N] "> <default: y|n> — strict
# y/Y/n/N/empty only, re-prompts on anything else. Prints result via
# $REPLY_YN (y or n). Appends an explicit "(Enter = ...)" after the
# [Y/n]-style hint so pressing Enter's effect is unambiguous, not just
# implied by which letter happens to be capitalized.
ask_yn() {
  local text="$1" default="$2" reply
  local default_word="Yes"
  [[ "$default" == n ]] && default_word="No"
  while true; do
    prompt "${text}(Enter = $default_word) "
    read -r reply || reply="$default"
    case "$reply" in
      y|Y) REPLY_YN=y; return ;;
      n|N) REPLY_YN=n; return ;;
      "")  REPLY_YN="$default"; return ;;
      *)   note "Please answer y or n." ;;
    esac
  done
}

section "orkestra install"

# ── 1. Build + install the executable ──────────────────────────────────
subsection "Building ork (Go)"
if ! command -v go &>/dev/null; then
  note "error: go not found — install Go (e.g. 'mise use -g go@latest'), or use the frozen bash version: legacy/build.sh" >&2
  exit 1
fi
# Built to bin/ first so a failed build can't leave a half-written binary
# on $PATH.
(cd "$DIR" && go build -o bin/ork ./cmd/ork)

mkdir -p "$BIN_DEST" "$SCRIPTS_DEST"
# `install` (not plain cp): cp writes into the existing inode and fails with
# "Text file busy" while an ork instance is running; install unlinks the
# target first, which works even mid-run.
install -m755 "$DIR/bin/ork" "$BIN_DEST/ork"
cp "$DIR/orc.cow" "$BIN_DEST/orc.cow"
# Stale from bash-era installs — the Go binary has no helper script.
rm -f "$BIN_DEST/ork-helper.sh"

cp "$DIR/worktree-tasks.sh" "$SCRIPTS_DEST/worktree-tasks.sh"
cp "$DIR/ork.sh" "$SCRIPTS_DEST/ork.sh"
ok "ork (Go) -> $BIN_DEST, worktree-tasks.sh/ork.sh -> $SCRIPTS_DEST"

case ":$PATH:" in
  *":$BIN_DEST:"*) ;;
  *) note "NOTE: $BIN_DEST is not on your \$PATH — add: export PATH=\"$BIN_DEST:\$PATH\"" ;;
esac

for rc in "$HOME/.bashrc" "$HOME/.zshrc"; do
  [[ -f "$rc" ]] || continue
  if ! grep -q "source ~/scripts/worktree-tasks.sh" "$rc" 2>/dev/null; then
    echo "source ~/scripts/worktree-tasks.sh" >> "$rc"
    ok "Added 'source ~/scripts/worktree-tasks.sh' to $rc"
  fi
  if ! grep -q "source ~/scripts/ork.sh" "$rc" 2>/dev/null; then
    echo "source ~/scripts/ork.sh" >> "$rc"
    ok "Added 'source ~/scripts/ork.sh' to $rc"
  fi
done

# ── 2. Install the Claude Code hook ──────────────────────────────────────
# The AGENT column (running/waiting/input) and live picker refresh are both
# driven by this hook — it's how Claude Code tells ork "I just started
# working" / "I just stopped and I'm waiting on you", pushed the instant it
# happens rather than polled. Skipping this step (which install.sh used to
# do, silently) is exactly what broke both features after the orch->ork
# rename: the hook file and its settings.json wiring were never touched by
# install.sh, so a stale copy under the OLD name kept firing into a cache
# directory nothing reads from anymore. Installing it here, every run,
# means a rename/reinstall can't strand it again.
subsection "Installing Claude Code hook"

CLAUDE_HOOKS_DIR="$HOME/.claude/hooks"
CLAUDE_SETTINGS="$HOME/.claude/settings.json"

if command -v claude >/dev/null 2>&1 || [[ -d "$HOME/.claude" ]]; then
  mkdir -p "$CLAUDE_HOOKS_DIR"
  cp "$DIR/hooks/ork-agent-state.sh" "$CLAUDE_HOOKS_DIR/ork-agent-state.sh"
  chmod +x "$CLAUDE_HOOKS_DIR/ork-agent-state.sh"
  ok "ork-agent-state.sh -> $CLAUDE_HOOKS_DIR"

  # A previous install under the pre-rename name would have left a stale
  # copy here, still pointing at the old cache paths — remove it so it
  # can't sit around double-firing or confusing a future `grep`.
  [[ -f "$CLAUDE_HOOKS_DIR/orch-agent-state.sh" ]] && \
    rm -f "$CLAUDE_HOOKS_DIR/orch-agent-state.sh" && \
    note "Removed stale $CLAUDE_HOOKS_DIR/orch-agent-state.sh from a pre-rename install."

  if [[ -n "$PY" ]]; then
    [[ -f "$CLAUDE_SETTINGS" ]] || echo '{}' > "$CLAUDE_SETTINGS"
    "$PY" - "$CLAUDE_SETTINGS" "$CLAUDE_HOOKS_DIR/ork-agent-state.sh" <<'PYEOF'
import json, sys

settings_path, hook_path = sys.argv[1], sys.argv[2]
# Matches by event name AND state argument (the last word of the command),
# not just "is this an ork-agent-state.sh call" — Notification/PermissionRequest
# both call it with "input" while UserPromptSubmit/PreToolUse/PostToolUse call
# it with "running", so collapsing on event name alone would let one overwrite
# the other's entry instead of both coexisting.
DESIRED = {
    "UserPromptSubmit": "running",
    "PreToolUse": "running",
    "PostToolUse": "running",
    "Stop": "waiting",
    "Notification": "input",
    "PermissionRequest": "input",
}

with open(settings_path) as f:
    settings = json.load(f)

hooks = settings.setdefault("hooks", {})

def is_ork_entry(entry, state):
    for h in entry.get("hooks", []):
        cmd = h.get("command", "")
        if "ork-agent-state.sh" in cmd or "orch-agent-state.sh" in cmd:
            if cmd.strip().endswith(state):
                return True
    return False

for event, state in DESIRED.items():
    entries = hooks.setdefault(event, [])
    # Drop any existing ork/orch-agent-state.sh entry for this exact
    # event+state pair before re-adding — makes this idempotent across
    # reinstalls/renames instead of accumulating duplicates, and picks up
    # a path change (e.g. hook moved) automatically.
    entries[:] = [e for e in entries if not is_ork_entry(e, state)]
    entries.append({"hooks": [{"type": "command", "command": f"{hook_path} {state}"}]})

with open(settings_path, "w") as f:
    json.dump(settings, f, indent=2)
    f.write("\n")
PYEOF
    ok "Wired hook into $CLAUDE_SETTINGS (UserPromptSubmit/PreToolUse/PostToolUse->running, Stop->waiting, Notification/PermissionRequest->input)"
  else
    note "No working python3 found — couldn't wire $CLAUDE_SETTINGS automatically."
    note "Add these hook entries yourself (see README): UserPromptSubmit/PreToolUse/PostToolUse -> \"$CLAUDE_HOOKS_DIR/ork-agent-state.sh running\", Stop -> \"...waiting\", Notification/PermissionRequest -> \"...input\"."
  fi
else
  note "No ~/.claude directory or 'claude' command found — skipping Claude Code"
  note "hook install. The AGENT column and live picker refresh need Claude Code's"
  note "hooks system; re-run install.sh after installing Claude Code to enable them."
fi

# ── 3. Configure where your worktrees live ──────────────────────────────
subsection "Configuring ~/.ork.conf"

CONF="$HOME/.ork.conf"
CONF_EXISTED=0
[[ -f "$CONF" ]] && CONF_EXISTED=1

if [[ "$CONF_EXISTED" -eq 0 ]]; then
  cp "$DIR/ork.conf.example" "$CONF"
elif grep -q '^ORK_CODE_ROOTS=' "$CONF" 2>/dev/null; then
  # Repos are no longer configured — ork scans $HOME live instead — so a
  # leftover ORK_CODE_ROOTS from an older install is now dead config.
  # Strip it so it doesn't sit there implying it still does something.
  sed -i.bak '/^ORK_CODE_ROOTS=/d' "$CONF"
  rm -f "$CONF.bak"
  note "Removed ORK_CODE_ROOTS from $CONF — repos are now discovered by"
  note "scanning \$HOME live, no config needed for that anymore."
fi

# ORK_HOOK_<repo> bash functions are no longer read by ork — replaced by
# a JSON file (see ork.conf.example) so a hooks file can't execute
# anything beyond the one command string it declares. Flag any leftover
# functions from an older install so they don't just silently stop firing.
if [[ "$CONF_EXISTED" -eq 1 ]] && grep -q '^ORK_HOOK_[a-zA-Z0-9_]*() {' "$CONF" 2>/dev/null; then
  echo
  note "Found ORK_HOOK_<repo>() functions in $CONF — ork no longer reads"
  note "these; they've stopped firing. Move them to ~/.config/ork/hooks.json"
  note "(plain JSON, one command string per repo — see ork.conf.example for"
  note "the format), then delete the old functions from $CONF."
fi

# Prompts for one or more existing directories. Uses fzf's own directory
# walker (multi-select: tab/space, enter to confirm) — 0.36+ has --walker,
# and this repo already requires 0.74 (see README/fzf pin). --print-query
# means whatever you've typed into the query box is captured too, so a
# path that doesn't exist yet (a scratch dir you haven't created) still
# works, matched literally against the typed text rather than requiring it
# to already be walkable. Falls back to the old type-one-per-line loop if
# fzf is missing or too old to have --walker. Falls back to $2 (the
# original default) if the user selects/types nothing at all.
prompt_dirs() {
  local label="$1" default="$2" dirs=() reply
  # Prompt text goes to stderr, not stdout — this function's stdout is
  # captured by `mapfile < <(prompt_dirs ...)` at the call site, so
  # anything printed here on stdout besides the final path list would leak
  # straight into the resulting array (confirmed live: the label/hint text
  # ended up as bogus entries in ORK_CODE_ROOTS).
  printf '%s%s%s\n' "$CYAN" "$label" "$RESET" >&2

  if command -v fzf >/dev/null 2>&1 && fzf --help 2>&1 | grep -q -- '--walker'; then
    dim "tab/space: select, enter: confirm, type to filter or type a brand-new path (default: $default)" >&2
    local out
    # fzf's built-in --walker has no depth limit of its own — an unbounded
    # walk of all of $HOME can take several seconds on a big/slow disk
    # (same latency issue ork's own repo scan hit — see ork's
    # all_repo_dirs comment). Feed it an explicit depth-bounded `find` via
    # FZF_DEFAULT_COMMAND instead of using --walker directly, capped to the
    # same ORK_SCAN_MAXDEPTH default (3) the rest of ork uses.
    out=$(FZF_DEFAULT_COMMAND="find '$HOME' -maxdepth ${ORK_SCAN_MAXDEPTH:-3} -type d 2>/dev/null" \
      fzf --multi --print-query \
      --bind 'tab:toggle+down,space:toggle+down' \
      --header "$label (tab: multi-select, enter: confirm)" \
      </dev/tty || true)
    mapfile -t dirs <<< "$out"
    # --print-query's line 1 is ALWAYS the raw typed query text, whether or
    # not it's empty and whether or not you went on to actually select a
    # real entry with arrow keys + Enter — confirmed live: typing a partial
    # path, arrow-ing down to the real match, and hitting Enter still left
    # that partial query non-empty on line 1, and the old "only drop line 1
    # if it's blank" check kept it as the answer, silently ignoring the
    # actual selection underneath (a folder named after the leftover
    # partial query got created and saved into ORK_WORKTREES_ROOTS instead
    # of the folder actually picked). Line 1 is ONLY the real answer when
    # there's NOTHING selected below it — i.e. you typed a brand-new path
    # and hit Enter with no match highlighted/selected.
    if [[ ${#dirs[@]} -gt 1 ]]; then
      dirs=("${dirs[@]:1}")
    elif [[ ${#dirs[@]} -eq 1 && -z "${dirs[0]}" ]]; then
      dirs=("$default")
    fi
    [[ ${#dirs[@]} -eq 0 ]] && dirs=("$default")
  else
    dim "(one path per line, ~ ok, empty line to finish; default: $default)" >&2
    local first=1
    while true; do
      printf '> ' >&2
      read -r reply || reply=""
      if [[ -z "$reply" ]]; then
        [[ "$first" -eq 1 ]] && dirs=("$default")
        break
      fi
      first=0
      dirs+=("$reply")
    done
  fi

  local d
  for d in "${dirs[@]}"; do
    printf '%s\n' "${d/#\~/$HOME}"
  done
}

reconfigure_roots=1
if [[ "$CONF_EXISTED" -eq 1 ]]; then
  existing_wt=$(grep '^ORK_WORKTREES_ROOTS=' "$CONF" 2>/dev/null || true)
  echo
  note "~/.ork.conf already exists."
  [[ -n "$existing_wt" ]] && dim "  $existing_wt"
  if [[ -t 0 ]]; then
    ask_yn "Reconfigure ORK_WORKTREES_ROOTS now? [y/N] " n
    [[ "$REPLY_YN" == y ]] || reconfigure_roots=0
  else
    reconfigure_roots=0
  fi
  [[ "$reconfigure_roots" -eq 0 ]] && note "Leaving it as-is. Edit $CONF directly to change it."
fi

if [[ "$reconfigure_roots" -eq 1 && -t 0 ]]; then
  echo
  dim "ork needs to know where to put new task worktrees (repos themselves"
  dim "are found automatically — no config needed for those). This doesn't"
  dim "have to be ~/worktrees — pick whatever layout you already use. You"
  dim "can list more than one; the FIRST entry is where new ones get created."
  echo
  mapfile -t wt_roots < <(prompt_dirs "Folder(s) to create/find task worktrees in:" "$HOME/worktrees")

  fmt_array() {
    local out="(" first=1 d
    for d in "$@"; do
      [[ "$first" -eq 1 ]] || out+=" "
      out+="\"$d\""
      first=0
    done
    out+=")"
    printf '%s' "$out"
  }

  wt_line="ORK_WORKTREES_ROOTS=$(fmt_array "${wt_roots[@]}")"

  # Substitutes the line in-place if the key already exists (works for both
  # a fresh copy of ork.conf.example AND a pre-existing conf that already
  # has the key, e.g. from a previous run of this same prompt); otherwise
  # appends it — covers a pre-existing ~/.ork.conf from before this
  # feature existed, which has neither key yet.
  set_conf_key "$CONF" ORK_WORKTREES_ROOTS "$wt_line"

  mkdir -p "${wt_roots[@]}" 2>/dev/null || true

  echo
  ok "Wrote to $CONF:"
  dim "  $wt_line"
elif [[ "$reconfigure_roots" -eq 1 ]]; then
  note "Non-interactive — created $CONF with default ORK_WORKTREES_ROOTS (~/worktrees)."
  note "Edit ORK_WORKTREES_ROOTS in $CONF to change it."
fi

echo
dim "Review $CONF for ORK_FAVORITES."
dim "Per-repo setup hooks go in ~/.config/ork/hooks.json — see"
dim "ork.conf.example for the format."

# ── 3b. Session backend (tmux or herdr) ─────────────────────────────────
subsection "Session backend"

# ork drives exactly ONE multiplexer — the choice here is forced via
# ORK_MULTIPLEXER even when both tmux and herdr are installed.
HAVE_TMUX=0; command -v tmux >/dev/null 2>&1 && HAVE_TMUX=1
HAVE_HERDR=0; command -v herdr >/dev/null 2>&1 && HAVE_HERDR=1

MUX_DEFAULT=tmux
[[ "$HAVE_TMUX" -eq 0 && "$HAVE_HERDR" -eq 1 ]] && MUX_DEFAULT=herdr
existing_mux=$(grep '^ORK_MULTIPLEXER=' "$CONF" 2>/dev/null | head -1 | cut -d= -f2 | tr -d '"' || true)
[[ "$existing_mux" == tmux || "$existing_mux" == herdr ]] && MUX_DEFAULT="$existing_mux"

ORK_MUX="$MUX_DEFAULT"
if [[ -t 0 ]]; then
  dim "ork runs task sessions inside a terminal multiplexer. Pick one:"
  dim "  tmux  — classic, prefix-key driven ($( [[ "$HAVE_TMUX" -eq 1 ]] && echo installed || echo NOT installed ))"
  dim "  herdr — mouse-first, agent-aware (https://herdr.dev) ($( [[ "$HAVE_HERDR" -eq 1 ]] && echo installed || echo NOT installed ))"
  dim "ork will use ONLY the one you pick, even if both are installed."
  while true; do
    prompt "Session backend? tmux/herdr [$MUX_DEFAULT]: "
    read -r reply || reply=""
    [[ -z "$reply" ]] && reply="$MUX_DEFAULT"
    case "$reply" in
      tmux|herdr) ORK_MUX="$reply"; break ;;
      *) note "'$reply' — type tmux or herdr." ;;
    esac
  done
else
  note "Non-interactive — using $ORK_MUX."
fi

if { [[ "$ORK_MUX" == tmux && "$HAVE_TMUX" -eq 0 ]]; } || { [[ "$ORK_MUX" == herdr && "$HAVE_HERDR" -eq 0 ]]; }; then
  note "WARNING: $ORK_MUX is not installed — ork will refuse to start until it is."
  [[ "$ORK_MUX" == herdr ]] && dim "Install with: curl -fsSL https://herdr.dev/install.sh | sh"
fi

# Same substitute-or-append mechanism as ORK_WORKTREES_ROOTS above.
set_conf_key "$CONF" ORK_MULTIPLEXER "ORK_MULTIPLEXER=$ORK_MUX"
ok "Wrote to $CONF: ORK_MULTIPLEXER=$ORK_MUX"

# ── 4. Keybinds ─────────────────────────────────────────────────────────
subsection "Keybinds"

if [[ "$KEYBIND" != "no" && -t 0 ]]; then
  if [[ "$ORK_MUX" == herdr ]] && command -v herdr >/dev/null 2>&1; then
    ask_yn "Add herdr keybinds for ork (prefix+o opens ork in a popup; ctrl+alt+shift+arrows cycle tabs/workspaces)? [Y/n] " y
    if [[ "$REPLY_YN" == y ]]; then
      "$DIR/keybind-install.sh" herdr
    fi
  fi

  if [[ "$ORK_MUX" == tmux ]] && command -v tmux >/dev/null 2>&1; then
    ask_yn "Add a tmux keybind for ork (prefix + key opens it in a pane on top of current pan, same rules as other tmux commands)? [Y/n] " y
    if [[ "$REPLY_YN" == y ]]; then
      TMUX_KEY=o
      dim "tmux uses ITS OWN prefix (ctrl-b by default) — this is just the single"
      dim "key pressed AFTER that prefix, e.g. 'o' means ctrl-b then o. It never"
      dim "fires on its own, so a single letter here is correct and expected"
      dim "(unlike the terminal-emulator chord below, which needs modifiers)."
      prompt "tmux key to press after the prefix? [o]: "
      read -r reply || reply=""
      [[ -n "$reply" ]] && TMUX_KEY="$reply"
      ok "Using tmux key: prefix + $TMUX_KEY"
      "$DIR/keybind-install.sh" tmux ctrl+alt+o "$TMUX_KEY"
    fi
  fi

  ask_yn "Install a terminal-emulator keybind too? [Y/n] " y
  if [[ "$REPLY_YN" == y ]]; then
    picks=""
    if command -v fzf >/dev/null 2>&1; then
      while [[ -z "$picks" ]]; do
        picks="$(printf 'Ghostty\nkitty\nAlacritty\n' | fzf --multi \
          --bind 'space:toggle+down' \
          --header 'space: toggle selection, enter: confirm (pick at least one)' \
          || true)"
      done
    else
      note "fzf not found — falling back to comma-separated numbers."
      printf '%sWhich terminal(s)? (comma-separated numbers)%s\n' "$CYAN" "$RESET"
      printf '%s  1) Ghostty%s\n' "$DIM" "$RESET"
      printf '%s  2) kitty%s\n' "$DIM" "$RESET"
      printf '%s  3) Alacritty%s\n' "$DIM" "$RESET"
      picknums=""
      while [[ -z "$picknums" ]]; do
        prompt "> "
        read -r picknums || picknums=""
      done
      IFS=',' read -r -a PICK_NUMS <<<"$picknums"
      for n in "${PICK_NUMS[@]}"; do
        n="$(echo "$n" | tr -d '[:space:]')"
        case "$n" in
          1) t=Ghostty ;;
          2) t=kitty ;;
          3) t=Alacritty ;;
          *) t="" ;;
        esac
        [[ -n "$t" ]] && picks="${picks:+$picks
}$t"
      done
    fi

    TERMLIST=""
    while IFS= read -r pick; do
      case "$pick" in
        Ghostty)   t=ghostty ;;
        kitty)     t=kitty ;;
        Alacritty) t=alacritty ;;
        *)         t="" ;;
      esac
      [[ -n "$t" ]] && TERMLIST="${TERMLIST:+$TERMLIST,}$t"
    done <<< "$picks"

    if [[ -z "$TERMLIST" ]]; then
      note "No valid terminal selected — skipping terminal-emulator keybind install."
    else
      CHORD=ctrl+alt+o
      dim "Unlike the tmux key above, this terminal has NO prefix of its own —"
      dim "whatever you enter here fires immediately, globally, in that terminal."
      dim "Enter the WHOLE chord with its modifier(s), e.g. ctrl+alt+o or super+o —"
      dim "a bare letter (e.g. just 'y') would rebind that key by itself and steal"
      dim "it from every other program running in that terminal."
      while true; do
        prompt "Keybind chord? [ctrl+alt+o]: "
        read -r reply || reply=""
        [[ -z "$reply" ]] && break
        if [[ "$reply" != *+* ]]; then
          note "'$reply' has no modifier (+) — did you mean to type a full chord like ctrl+alt+$reply?"
          ask_yn "Use '$reply' as-is anyway? [y/N] " n
          [[ "$REPLY_YN" == y ]] && { CHORD="$reply"; break; }
          continue
        fi
        CHORD="$reply"
        break
      done
      ok "Using chord: $CHORD"
      "$DIR/keybind-install.sh" "$TERMLIST" "$CHORD"
    fi
  fi
elif [[ "$KEYBIND" != "no" ]]; then
  note "NOTE: no terminal to prompt for (non-interactive) — skipping keybind install."
fi

section "Done"
dim "Requires: $ORK_MUX, fzf."
dim "If you already have your own new-task/end-task functions, remove the"
dim "worktree-tasks.sh source line from your shell rc to keep using yours."
printf '%sRestart your shell (or re-source your rc file), then run: %s%sork%s\n' "$DIM" "$RESET" "$BOLD$GREEN" "$RESET"
