#!/usr/bin/env bash
# Installs worktree-orch:
#   - orch, orch-helper.sh -> ~/.local/bin (real executable, on $PATH like mise/nvim)
#   - worktree-tasks.sh, orch.sh -> ~/scripts (sourced from your shell rc)
#   - ~/.orch.conf from the example, with your code/worktree roots filled in
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

# Same palette as orch-helper.sh, so the install experience matches the
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

section "worktree-orch install"

# ── 1. Install the executables ─────────────────────────────────────────
subsection "Installing scripts"
mkdir -p "$BIN_DEST" "$SCRIPTS_DEST"

cp "$DIR/orch" "$BIN_DEST/orch"
cp "$DIR/orch-helper.sh" "$BIN_DEST/orch-helper.sh"
chmod +x "$BIN_DEST/orch" "$BIN_DEST/orch-helper.sh"

cp "$DIR/worktree-tasks.sh" "$SCRIPTS_DEST/worktree-tasks.sh"
cp "$DIR/orch.sh" "$SCRIPTS_DEST/orch.sh"
ok "orch -> $BIN_DEST, worktree-tasks.sh/orch.sh -> $SCRIPTS_DEST"

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
  if ! grep -q "source ~/scripts/orch.sh" "$rc" 2>/dev/null; then
    echo "source ~/scripts/orch.sh" >> "$rc"
    ok "Added 'source ~/scripts/orch.sh' to $rc"
  fi
done

# ── 2. Configure where your worktrees live ──────────────────────────────
subsection "Configuring ~/.orch.conf"

CONF="$HOME/.orch.conf"
CONF_EXISTED=0
[[ -f "$CONF" ]] && CONF_EXISTED=1

if [[ "$CONF_EXISTED" -eq 0 ]]; then
  cp "$DIR/orch.conf.example" "$CONF"
elif grep -q '^ORCH_CODE_ROOTS=' "$CONF" 2>/dev/null; then
  # Repos are no longer configured — orch scans $HOME live instead — so a
  # leftover ORCH_CODE_ROOTS from an older install is now dead config.
  # Strip it so it doesn't sit there implying it still does something.
  sed -i.bak '/^ORCH_CODE_ROOTS=/d' "$CONF"
  rm -f "$CONF.bak"
  note "Removed ORCH_CODE_ROOTS from $CONF — repos are now discovered by"
  note "scanning \$HOME live, no config needed for that anymore."
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
  # ended up as bogus entries in ORCH_CODE_ROOTS).
  printf '%s%s%s\n' "$CYAN" "$label" "$RESET" >&2

  if command -v fzf >/dev/null 2>&1 && fzf --help 2>&1 | grep -q -- '--walker'; then
    dim "tab/space: select, enter: confirm, type to filter or type a brand-new path (default: $default)" >&2
    local out
    # fzf's built-in --walker has no depth limit of its own — an unbounded
    # walk of all of $HOME can take several seconds on a big/slow disk
    # (same latency issue orch's own repo scan hit — see orch's
    # all_repo_dirs comment). Feed it an explicit depth-bounded `find` via
    # FZF_DEFAULT_COMMAND instead of using --walker directly, capped to the
    # same ORCH_SCAN_MAXDEPTH default (3) the rest of orch uses.
    out=$(FZF_DEFAULT_COMMAND="find '$HOME' -maxdepth ${ORCH_SCAN_MAXDEPTH:-3} -type d 2>/dev/null" \
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
    # partial query got created and saved into ORCH_WORKTREES_ROOTS instead
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
  existing_wt=$(grep '^ORCH_WORKTREES_ROOTS=' "$CONF" 2>/dev/null || true)
  echo
  note "~/.orch.conf already exists."
  [[ -n "$existing_wt" ]] && dim "  $existing_wt"
  if [[ -t 0 ]]; then
    ask_yn "Reconfigure ORCH_WORKTREES_ROOTS now? [y/N] " n
    [[ "$REPLY_YN" == y ]] || reconfigure_roots=0
  else
    reconfigure_roots=0
  fi
  [[ "$reconfigure_roots" -eq 0 ]] && note "Leaving it as-is. Edit $CONF directly to change it."
fi

if [[ "$reconfigure_roots" -eq 1 && -t 0 ]]; then
  echo
  dim "orch needs to know where to put new task worktrees (repos themselves"
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

  wt_line="ORCH_WORKTREES_ROOTS=$(fmt_array "${wt_roots[@]}")"

  # Substitutes the line in-place if the key already exists (works for both
  # a fresh copy of orch.conf.example AND a pre-existing conf that already
  # has the key, e.g. from a previous run of this same prompt); otherwise
  # appends it — covers a pre-existing ~/.orch.conf from before this
  # feature existed, which has neither key yet.
  python3 - "$CONF" "$wt_line" <<'PYEOF'
import re, sys
conf_path, wt_line = sys.argv[1], sys.argv[2]
text = open(conf_path).read()
pattern = r'^ORCH_WORKTREES_ROOTS=.*$'
if re.search(pattern, text, flags=re.M):
    text = re.sub(pattern, wt_line, text, count=1, flags=re.M)
else:
    if not text.endswith("\n"):
        text += "\n"
    text += wt_line + "\n"
open(conf_path, "w").write(text)
PYEOF

  mkdir -p "${wt_roots[@]}" 2>/dev/null || true

  echo
  ok "Wrote to $CONF:"
  dim "  $wt_line"
elif [[ "$reconfigure_roots" -eq 1 ]]; then
  note "Non-interactive — created $CONF with default ORCH_WORKTREES_ROOTS (~/worktrees)."
  note "Edit ORCH_WORKTREES_ROOTS in $CONF to change it."
fi

echo
dim "Review $CONF for ORCH_FAVORITES and per-repo ORCH_HOOK_<repo> setup hooks."

# ── 3. Keybinds ─────────────────────────────────────────────────────────
subsection "Keybinds"

if [[ "$KEYBIND" != "no" && -t 0 ]]; then
  if command -v tmux >/dev/null 2>&1; then
    ask_yn "Add a tmux keybind for orch (prefix + key opens it in a pane on top of current pan, same rules as other tmux commands)? [Y/n] " y
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
dim "Requires: tmux, fzf."
dim "If you already have your own new-task/end-task functions, remove the"
dim "worktree-tasks.sh source line from your shell rc to keep using yours."
printf '%sRestart your shell (or re-source your rc file), then run: %s%sorch%s\n' "$DIM" "$RESET" "$BOLD$GREEN" "$RESET"
