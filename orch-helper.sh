#!/usr/bin/env bash
# Non-interactive helper for orch. Called directly by fzf bindings
# (no `source`, no `-i`) to avoid repeated dotfile-sourcing overhead and TTY
# job-control issues when invoked from fzf's execute()/reload().
set -u

[[ -f "$HOME/.orch.conf" ]] && source "$HOME/.orch.conf"

# ORCH_WORKTREES_ROOTS: where per-task worktrees get created/found. Defaults
# to the original hardcoded ~/worktrees for backward compat — override in
# ~/.orch.conf as an array to use a different location (or several).
#
# Repos themselves are NOT configured — find_repo_root/all_repo_dirs below
# scan live under $HOME instead (0.6s on a normal dev machine, fast enough
# to run on every invocation), so there's no ORCH_CODE_ROOTS to keep in
# sync with wherever you actually clone things.
ORCH_WORKTREES_ROOTS=("${ORCH_WORKTREES_ROOTS[@]:-$HOME/worktrees}")

# Directory names never worth descending into while scanning for repos —
# dependency trees and build output can contain thousands of nested dirs
# (and occasionally their own vendored .git dirs), so pruning them isn't
# just an optimization, it avoids surfacing repos you didn't actually clone
# yourself.
_ORCH_SCAN_PRUNE=(node_modules .cache vendor dist build target .venv venv __pycache__ .terraform)

# Full-depth unbounded scans ranged 0.7s-17s depending on what else was
# hitting disk (confirmed live) — bounding depth and caching the result
# fixes both the worst-case latency AND the "looks frozen" UX problem this
# caused in orch's ctrl-n repo picker. Keep in sync with the identical
# cache/maxdepth logic in `orch` (this file can't source that one — see
# the comment there for why).
_ORCH_REPO_CACHE="$HOME/.cache/orch/repo-scan"
_ORCH_REPO_CACHE_TTL=60

# Lists every repo's checkout dir under $HOME, one per line — a repo is any
# directory containing a .git (worktrees' .git is a file, not a dir, so
# `git worktree add`-created worktrees are naturally excluded; only real
# clones show up here).
all_repo_dirs() {
  mkdir -p "$(dirname "$_ORCH_REPO_CACHE")"
  local age=999999
  if [[ -f "$_ORCH_REPO_CACHE" ]]; then
    age=$(( $(date +%s) - $(stat -c '%Y' "$_ORCH_REPO_CACHE" 2>/dev/null || echo 0) ))
  fi
  if (( age <= _ORCH_REPO_CACHE_TTL )); then
    cat "$_ORCH_REPO_CACHE"
    return
  fi

  local prune_expr=(-name "${_ORCH_SCAN_PRUNE[0]}") name
  for name in "${_ORCH_SCAN_PRUNE[@]:1}"; do
    prune_expr+=(-o -name "$name")
  done
  # .git itself must NOT be in the prune list — pruning it would stop find
  # from ever printing it (confirmed live: with .git in the -prune set, the
  # scan silently found zero repos). Prune only the noisy dependency/build
  # dirs; .git dirs get matched and printed on the second clause instead.
  find "$HOME" -maxdepth "${ORCH_SCAN_MAXDEPTH:-3}" \( -type d \( "${prune_expr[@]}" \) -prune \) -o -type d -name .git -print 2>/dev/null | \
    sed 's|/\.git$||' | tee "$_ORCH_REPO_CACHE"
}

# Finds a repo's checkout dir by basename (first match wins if more than
# one repo on disk shares that name — same ambiguity a plain ORCH_CODE_ROOTS
# array had, just implicit now instead of resolved by array order).
find_repo_root() {
  local repo=$1 dir
  while IFS= read -r dir; do
    [[ "$(basename "$dir")" == "$repo" ]] && { printf '%s' "$dir"; return; }
  done < <(all_repo_dirs)
}

# Finds a task's worktree dir by repo+task across all configured worktree
# roots (a worktree root may itself contain one folder-per-repo, matching
# worktree-tasks.sh's ~/worktrees/<repo>/<task> layout). Echoes the first
# match; empty if none exist yet (e.g. about to be created).
find_worktree() {
  local repo=$1 task=$2 root
  for root in "${ORCH_WORKTREES_ROOTS[@]}"; do
    [[ -d "$root/$repo/$task" ]] && { printf '%s' "$root/$repo/$task"; return; }
  done
}

# Lists every <root>/<repo>/<task>/ dir across ALL configured worktree
# roots, one per line — the multi-root equivalent of a single `for wt in
# "$HOME"/worktrees/*/*/` glob. Callers that used to loop over one
# hardcoded root now loop over this instead.
all_worktree_dirs() {
  local root
  for root in "${ORCH_WORKTREES_ROOTS[@]}"; do
    for wt in "$root"/*/*/; do
      [[ -d "$wt" ]] || continue
      printf '%s\n' "${wt%/}"
    done
  done
}

ACCESS_DIR="$HOME/.cache/orch/access"
AGENT_STATE_DIR="$HOME/.cache/orch/agent-state"
# Push-based, not polled: ~/.claude/hooks/orch-agent-state.sh writes
# running/waiting into this dir directly off Claude Code's own hook events
# (UserPromptSubmit+PreToolUse -> running, Stop+Notification -> waiting).
# Scraping tmux pane text for a "tokens)" substring was a race against
# Claude Code's own repaint (that suffix is only on-screen for the ~100ms
# it's mid-render) — confirmed live: it showed "waiting" almost always
# because the running window was too narrow for a 3s poll to ever land in
# it. A generous staleness window is still needed here, but only to catch
# sessions that died without ever firing Stop (killed pane, crashed
# process) — NOT as the primary freshness mechanism.
AGENT_STATE_STALE_SECS=600

# Path of the access-marker file for a repo/task pair.
access_file() {
  printf '%s/%s__%s' "$ACCESS_DIR" "$1" "$2"
}

# Path of the agent-state cache file for a tmux session name. Kept by
# session name (not repo/task) since hooks resolve TMUX_PANE -> session name
# themselves, and the same session can be shared across repos.
agent_state_file() {
  printf '%s/%s' "$AGENT_STATE_DIR" "$1"
}

# Reads the cached state for a session — treated as unknown (empty) if the
# cache is missing or older than AGENT_STATE_STALE_SECS (session likely dead
# without ever firing a Stop hook).
cached_agent_state() {
  local sess=$1
  local f
  f=$(agent_state_file "$sess")
  [[ -f "$f" ]] || return
  local age=$(( $(date +%s) - $(stat -c '%Y' "$f" 2>/dev/null || echo 0) ))
  (( age > AGENT_STATE_STALE_SECS )) && return
  cat "$f"
}

# Truncates $1 to $2 chars, replacing the tail with "..." if it overflows.
trunc() {
  local s=$1 w=$2
  if [[ ${#s} -gt $w ]]; then
    printf '%s...' "${s:0:$((w - 3))}"
  else
    printf '%s' "$s"
  fi
}

# Deterministic ANSI 256-color for a repo name — same repo always gets the
# same color across runs/reloads (hash of the string, not random per-call),
# so it's usable as a visual grouping cue in the list.
REPO_COLOR_PALETTE=(39 208 178 141 71 203 74 209 135 214 84 168 45 220 111)
repo_color() {
  local name=$1
  # cksum mixes bits far better than a hand-rolled per-char hash — with only
  # 15 palette slots, a weak hash collided on the two most common repos
  # (cr-managament and cr-frontend landed on the same color).
  local hash=$(echo -n "$name" | cksum | cut -d' ' -f1)
  local idx=$(( hash % ${#REPO_COLOR_PALETTE[@]} ))
  printf '\033[38;5;%sm' "${REPO_COLOR_PALETTE[$idx]}"
}

# $'...' (ANSI-C quoting) is required here so \033 becomes the actual ESC
# byte at assignment time — plain '...' stores the literal 4 characters
# "\033" instead, which only renders correctly when later passed through
# printf's own format-string interpretation (%b or a literal in the format
# string), and silently prints as garbage text everywhere else (e.g. awk -v,
# bash string substitution).
RESET=$'\033[0m'
GREEN=$'\033[38;5;114m'
YELLOW=$'\033[38;5;179m'
CYAN=$'\033[38;5;80m'
DIM=$'\033[38;5;244m'
BOLD_WHITE=$'\033[1;38;5;254m'

# Human-readable "time ago" for a unix timestamp, e.g. "5m ago", "3h ago".
ago() {
  local now=$1 then=$2
  local diff=$(( now - then ))
  if   (( diff < 60 ));    then printf '%ds ago' "$diff"
  elif (( diff < 3600 ));  then printf '%dm ago' $((diff / 60))
  elif (( diff < 86400 )); then printf '%dh ago' $((diff / 3600))
  else                          printf '%dd ago' $((diff / 86400))
  fi
}

rows() {
  # Sorted by last-accessed-via-orch, most recent first. Worktrees never
  # opened through orch have no such signal — folder mtime is not a
  # substitute (it moves on file edits, not on cd/attach, so it's just
  # wrong) — those show "never" and sort last.
  local now=$(date +%s)

  # Session resolution used to run independently per worktree row: each row
  # only looked for a pane whose cwd matched ITS OWN folder, falling back to
  # a bare task-named session only when that failed. Two sibling worktrees
  # sharing a task (BE+FE under the same task name) could then resolve to
  # different sessions — whichever one had the exact-cwd pane got the real
  # session, the other silently fell back and, on any naming mismatch (e.g.
  # ORCH_SCOPE_SESSIONS_TO_REPO), disagreed entirely — the exact "waiting
  # shows in one folder but not the other" bug. Resolve once per TASK here,
  # up front, from a single tmux snapshot, so every sibling worktree of the
  # same task shares one session and therefore one cached agent state.
  local all_panes
  all_panes=$(tmux list-panes -a -F "#{session_name}:#{window_index}.#{pane_index}	#{pane_current_path}	#{pane_current_command}" 2>/dev/null)

  declare -A task_session task_cmd task_agent
  while IFS= read -r wt; do
    local t=$(basename "$wt")
    [[ -n "${task_session[$t]+x}" ]] && continue

    local pane_info
    pane_info=$(printf '%s\n' "$all_panes" | awk -F'\t' -v p="$wt" '$2==p{print; exit}')
    if [[ -n "$pane_info" ]]; then
      task_session[$t]=$(echo "$pane_info" | cut -f1 | cut -d: -f1)
      task_cmd[$t]=$(echo "$pane_info" | cut -f3)
    elif tmux has-session -t "=$t" 2>/dev/null; then
      task_session[$t]="$t"
      task_cmd[$t]=$(tmux list-panes -t "=$t" -F "#{pane_current_command}" 2>/dev/null | head -1)
    fi

    # Read the agent-state cache HERE, once per task, in the same pass as
    # session resolution — reading it again per-row later (once per sibling
    # worktree) let a hook write land in between two rows() iterations
    # (each iteration also does a git call, real wall-clock time), so two
    # rows sharing the identical session could observe two different
    # values within a single rows() invocation. One read per task closes
    # that window entirely.
    [[ -n "${task_session[$t]:-}" ]] && task_agent[$t]=$(cached_agent_state "${task_session[$t]}")
  done < <(all_worktree_dirs)

  while IFS= read -r wt; do
    repo=$(basename "$(dirname "$wt")")
    task=$(basename "$wt")
    branch=$(git -C "$wt" branch --show-current 2>/dev/null)

    af=$(access_file "$repo" "$task")
    if [[ -f "$af" ]]; then
      mtime=$(stat -c '%Y' "$af" 2>/dev/null || echo 0)
      last_used=$(ago "$now" "$mtime")
    else
      mtime=0
      last_used="-"
    fi

    # Shared across every sibling worktree of this task — see resolution
    # pass above.
    session="-" cmd="-" state="idle"
    if [[ -n "${task_session[$task]:-}" ]]; then
      session="${task_session[$task]}"
      cmd="${task_cmd[$task]}"
      state="live"
    fi

    local branch_col="$branch"
    [[ "$branch" == "$task" ]] && branch_col="="

    local state_color="$YELLOW"
    [[ "$state" == "live" ]] && state_color="$GREEN"

    # Resolved once per task in the pass above — every sibling worktree of
    # this task reads the identical value here, no re-fetch.
    local agent="${task_agent[$task]:-}" agent_color="$DIM"
    case "$agent" in
      running) agent_color="$GREEN" ;;
      waiting) agent_color="$CYAN" ;;
      input) agent_color="$YELLOW" ;;
    esac

    # repo/task (fields 1,2) are used verbatim by fzf's {1}/{2} for
    # end-task/jump/full-row lookups — color codes are safe here since fzf
    # (with --ansi) strips them before splitting into positional fields, but
    # the *padding width* must still be computed on the plain text, or the
    # invisible escape bytes would throw off column alignment.
    local repo_padded task_padded state_padded agent_padded
    repo_padded=$(printf '%-16s' "$repo")
    task_padded=$(printf '%-32s' "$task")
    state_padded=$(printf '%-8s' "$state")
    agent_padded=$(printf '%-8s' "${agent:--}")

    printf "%s\t$(repo_color "$repo")%s${RESET} %-32s %-14s ${state_color}%s${RESET} ${agent_color}%s${RESET} %-16s %-9s %s\n" "$mtime" \
      "$repo_padded" "$task_padded" "$(trunc "${branch_col:-none}" 14)" \
      "$state_padded" "$agent_padded" "$(trunc "$session" 16)" "$last_used" "$(trunc "$cmd" 12)"
  done < <(all_worktree_dirs) | sort -t$'\t' -k1,1rn | cut -f2- | fortune_sidebar
}

# Cosmetic filler for the dead space to the right of the worktree rows —
# appends one cowsay/fortune line per row, so the cow appears to sit beside
# the table rather than the row text wrapping into it. No-ops silently if
# either binary is missing.
fortune_sidebar() {
  if ! command -v fortune &>/dev/null || ! command -v cowsay &>/dev/null; then
    cat
    return
  fi
  local row_width=103
  local gap=44
  local fortune_block
  fortune_block=$(fortune -s | fold -s -w 35 | cowsay -n 2>/dev/null)
  local fortune_lines=()
  IFS=$'\n' read -rd '' -a fortune_lines <<<"$fortune_block"$'\0'

  local i=0 line
  while IFS= read -r line; do
    local plain
    plain=$(printf '%s' "$line" | sed -r 's/\x1B\[[0-9;]*[a-zA-Z]//g')
    local pad=$(( row_width + gap - ${#plain} ))
    (( pad < 1 )) && pad=1
    printf '%s%*s%s\n' "$line" "$pad" "" "${fortune_lines[$i]:-}"
    ((i++))
  done
}

# Kill whatever tmux session(s) belong to a worktree, without touching the
# worktree/branch itself. Shared by end_task (as part of full cleanup) and
# kill_task (session-only, ctrl-k in the picker).
#
# Kills any session whose pane cwd matches this worktree (covers sessions
# started under a different name than the task, e.g. cross-repo sharing),
# AND the repo-scoped session name (used when ORCH_SCOPE_SESSIONS_TO_REPO=1
# — see named_session() in orch) — a session can exist under that name with
# no pane ever having this exact cwd if it was created but the process cd'd
# away. The plain task-named session (default naming) may be shared across
# repos, so it's only killed if no OTHER repo's worktree under this same
# task name still exists — otherwise this would yank the session out from
# under a still-active sibling.
kill_session_for() {
  local repo=$1 task=$2
  local wt; wt=$(find_worktree "$repo" "$task"); [[ -n "$wt" ]] || wt="${ORCH_WORKTREES_ROOTS[0]}/$repo/$task"

  local target
  target=$(tmux list-panes -a -F "#{session_name}:#{window_index}.#{pane_index}	#{pane_current_path}" 2>/dev/null | \
    awk -F'\t' -v p="$wt" '$2==p{print $1; exit}')
  [[ -n "$target" ]] && tmux kill-session -t "${target%%:*}" 2>/dev/null

  local repo_session="${repo}__${task}"
  tmux has-session -t "=$repo_session" 2>/dev/null && tmux kill-session -t "=$repo_session" 2>/dev/null

  if tmux has-session -t "=$task" 2>/dev/null; then
    local d has_sibling=0 root
    for root in "${ORCH_WORKTREES_ROOTS[@]}"; do
      for d in "$root"/*/"$task"; do
        [[ -d "$d" ]] || continue
        [[ "$d" == "$wt" ]] && continue
        has_sibling=1
        break 2
      done
    done
    [[ "$has_sibling" -eq 0 ]] && tmux kill-session -t "=$task" 2>/dev/null
  fi
}

# ctrl-k in the picker: kill the session(s) for a worktree, leave the
# worktree/branch untouched — for when you want to free up the tmux session
# without ending the task. No confirmation prompt: cheap/reversible, ENTER
# just recreates the session.
kill_task() {
  local repo=$1 task=$2
  kill_session_for "$repo" "$task"
  echo "$(date '+%H:%M:%S') killed session for $repo/$task (worktree+branch untouched)" >> /tmp/orch.log
}

end_task() {
  local repo=$1 task=$2
  local wt; wt=$(find_worktree "$repo" "$task"); [[ -n "$wt" ]] || wt="${ORCH_WORKTREES_ROOTS[0]}/$repo/$task"

  (
    repo_root=$(find_repo_root "$repo")
    cd "${repo_root:-$HOME/code/$repo}" 2>/dev/null || exit 1
    git worktree remove "$wt" --force 2>>/tmp/orch.log
    git worktree prune
    git branch -D "$task" 2>>/tmp/orch.log
    git push origin --delete "$task" 2>>/tmp/orch.log
  )
  [[ -d "$wt" ]] && rm -rf "$wt"
  rm -f "$(access_file "$repo" "$task")"
  echo "$(date '+%H:%M:%S') removed $repo/$task (worktree+branch, local+origin)" >> /tmp/orch.log

  # Kill the session LAST: when orch itself runs inside the session being
  # ended (tmux-keybind window, or plain attach to the same task session),
  # killing it also kills orch and this very helper process — with the kill
  # first, everything below it silently never ran (confirmed live: first
  # ctrl-x killed the session and dropped the client back out, leaving
  # branch+folder behind; a second ctrl-x from a fresh orch finished the
  # job). Killing last means the cleanup is already done if we die here.
  kill_session_for "$repo" "$task"
}

# Full-screen fzf yes/no dialog, styled like the picker itself so a
# confirmation doesn't LOOK like orch exited (the old raw `read` off
# /dev/tty dropped back to a bare shell-style prompt line — jarring). Runs
# with terminal access (called via fzf's execute(), not reload()); fzf
# draws its UI on /dev/tty directly, stdout is just the picked line. "no"
# is first so a reflexive ENTER (or esc) is always the safe answer.
# Returns 0 only on an explicit "yes".
confirm_dialog() {
  local header=$1
  local choice
  choice=$(printf 'no  — cancel, do nothing\nyes — %s\n' "$header" | \
    fzf --layout=reverse --no-info --prompt="confirm> " \
        --header="$header — pick one" --bind "esc:abort")
  [[ "$choice" == yes* ]]
}

confirm_end_task() {
  local repo=$1 task=$2
  if confirm_dialog "DELETE worktree + branch (local & origin) for $repo/$task"; then
    end_task "$repo" "$task"
  else
    echo "$(date '+%H:%M:%S') skipped $repo/$task (not confirmed)" >> /tmp/orch.log
  fi
}

confirm_kill_task() {
  local repo=$1 task=$2
  if confirm_dialog "kill tmux session for $repo/$task (worktree+branch untouched)"; then
    kill_task "$repo" "$task"
  else
    echo "$(date '+%H:%M:%S') skipped kill-session for $repo/$task (not confirmed)" >> /tmp/orch.log
  fi
}

# Resolve the live tmux pane (if any) for a repo/task, same rule as rows():
# match by cwd, or by a session named after this task (sessions are shared
# by task name across repos by default). Prints tab-separated pane_info or
# nothing if there's no live pane.
_orch_resolve_pane() {
  local repo=$1 task=$2
  local wt; wt=$(find_worktree "$repo" "$task"); [[ -n "$wt" ]] || wt="${ORCH_WORKTREES_ROOTS[0]}/$repo/$task"
  local pane_info
  pane_info=$(tmux list-panes -a -F "#{session_name}:#{window_index}.#{pane_index}	#{pane_current_path}	#{pane_current_command}	#{pane_pid}" 2>/dev/null | \
    awk -F'\t' -v p="$wt" '$2==p{print; exit}')
  if [[ -z "$pane_info" ]] && tmux has-session -t "=$task" 2>/dev/null; then
    pane_info=$(tmux list-panes -t "=$task" -F "#{session_name}:#{window_index}.#{pane_index}	#{pane_current_path}	#{pane_current_command}	#{pane_pid}" 2>/dev/null | head -1)
  fi
  printf '%s' "$pane_info"
}

# Compact one-line info summary (branch, state, session, last-used) —
# repo/task are already visible as the selected row itself, no need to
# repeat them here. Shown in the left half of the preview split.
info_panel() {
  local repo=$1 task=$2
  local wt; wt=$(find_worktree "$repo" "$task"); [[ -n "$wt" ]] || wt="${ORCH_WORKTREES_ROOTS[0]}/$repo/$task"
  # Always the real branch name — never collapse to "=" even if it happens
  # to match the task/folder name, since that's misleading here (the row
  # list already does the "=" shorthand, this panel is meant to be exact).
  local branch=$(git -C "$wt" branch --show-current 2>/dev/null)

  echo " branch: ${branch:-none}"
  echo " path:   ${wt/#$HOME/\~}"
  echo
  tail -n 5 /tmp/orch.log 2>/dev/null
}

# Tmux session summary, shown as the TMUX column's header (session name,
# window count, attached state, running command) — split across 2 lines so
# it lines up row-for-row with info_panel's own 2 content lines (branch,
# path). Tmux details live here, not in info_panel, so they sit next to the
# pane content they describe instead of off to the side.
tmux_summary_line1() {
  local repo=$1 task=$2
  local pane_info
  pane_info=$(_orch_resolve_pane "$repo" "$task")
  [[ -z "$pane_info" ]] && { echo " session: none"; return; }

  local sess=$(echo "$pane_info" | cut -f1)
  local nwin nclients
  nwin=$(tmux list-windows -t "${sess%%:*}" 2>/dev/null | wc -l)
  nclients=$(tmux list-clients -t "${sess%%:*}" 2>/dev/null | wc -l)
  printf ' session: %s | windows: %s | %s' \
    "${sess%%:*}" "$nwin" "$([[ $nclients -gt 0 ]] && echo attached || echo detached)"
}

tmux_summary_line2() {
  local repo=$1 task=$2
  local wt; wt=$(find_worktree "$repo" "$task"); [[ -n "$wt" ]] || wt="${ORCH_WORKTREES_ROOTS[0]}/$repo/$task"
  local pane_info
  pane_info=$(_orch_resolve_pane "$repo" "$task")
  [[ -z "$pane_info" ]] && return

  local pane_cwd win_cmd pid
  pane_cwd=$(echo "$pane_info" | cut -f2)
  win_cmd=$(echo "$pane_info" | cut -f3)
  pid=$(echo "$pane_info" | cut -f4)

  local note=""
  [[ "$pane_cwd" != "$wt" ]] && note="  (shared, cwd: ${pane_cwd/#$HOME/\~})"
  printf " running: %s (pid %s)%s" "$win_cmd" "$pid" "$note"
}

# Right half of the preview split: the live tmux pane's actual content, if
# any session exists for this worktree/task. Plain text (no ANSI colors) —
# color codes don't survive being clipped to a column width for the
# side-by-side paste layout, so this trades color for reliability.
# $3 = how many of the pane's bottommost lines to show (must be resolved by
# the caller to whatever actually fits, otherwise a later `head` on top of a
# fixed "last 40" tail just shows an arbitrary earlier slice, not the true
# bottom of the pane).
# No rewrapping needed: with the preview window's border removed
# (border-none), its width matches the real pane width exactly, so tmux's
# own wrapping is already correct — re-wrapping was only a workaround for
# the border eating a couple of columns.
pane_preview() {
  local repo=$1 task=$2 want=${3:-40}
  local pane_info
  pane_info=$(_orch_resolve_pane "$repo" "$task")
  if [[ -z "$pane_info" ]]; then
    echo "(no live tmux session)"
    return
  fi
  local sess=$(echo "$pane_info" | cut -f1)

  # Occasionally empty on the first try (tmux socket contention when many
  # panes/clients are active) — one retry is enough to make this reliable.
  local out
  out=$(tmux capture-pane -pet "$sess" 2>/dev/null)
  if [[ -z "$out" ]]; then
    sleep 0.05
    out=$(tmux capture-pane -pet "$sess" 2>/dev/null)
  fi
  printf '%s\n' "$out" | tail -n "$want"
}

touch_access() {
  local repo=$1 task=$2
  mkdir -p "$ACCESS_DIR"
  touch "$(access_file "$repo" "$task")"
}

# fzf 0.29 has only one preview slot — render info (left) and the live pane
# (right) side by side manually. `pr -m -t` merges two files into columns,
# padding whichever side has fewer lines — a hand-rolled `paste` would
# misalign rows the moment line counts differ, which is what made this
# "inconsistent" before.
split_preview() {
  local repo=$1 task=$2
  local cols=${FZF_PREVIEW_COLUMNS:-160}
  local lines=${FZF_PREVIEW_LINES:-20}

  # border-none removed all visual separation between the list above and
  # this preview pane — draw one line of our own as the boundary.
  printf '%s\n' "$(printf '%*s' "$cols" '' | tr ' ' '-')"

  # INFO gets 40% of the width, TMUX header gets the remaining 60% — the "-1"
  # reserves exactly one column for the "|" separator so left_w + 1 +
  # right_w == cols precisely (a mismatch here is what caused the header's
  # "|" to drift out of line with the divider below it).
  local left_w=$(( cols * 4 / 10 ))
  (( left_w < 20 )) && left_w=20
  local right_w=$(( cols - left_w - 1 ))

  local info_text
  info_text=$(info_panel "$repo" "$task" | cut -c1-"$left_w")
  local info_line_count
  info_line_count=$(printf '%s\n' "$info_text" | wc -l)

  # Top block: INFO on the left, tmux summary on the right — its two lines
  # (session/windows/attached, running cmd) line up with info_panel's first
  # two content lines (branch, path) instead of one long line spanning past
  # where INFO's content ends.
  local tmux_l1 tmux_l2
  tmux_l1=$(tmux_summary_line1 "$repo" "$task" | cut -c1-"$right_w")
  tmux_l2=$(tmux_summary_line2 "$repo" "$task" | cut -c1-"$right_w")

  printf "${BOLD_WHITE}%-*s${RESET}|${BOLD_WHITE}%s${RESET}\n" "$left_w" " INFO" " TMUX"
  printf '%s+%s\n' "$(printf '%*s' "$left_w" '' | tr ' ' '-')" "$(printf '%*s' "$right_w" '' | tr ' ' '-')"

  # Pad to fixed width on PLAIN text first (escape codes would corrupt the
  # width math if counted), THEN colorize by splitting each line on its
  # known "label: value" shape and re-joining with color codes — plain bash
  # substring ops instead of sed, since sed backreferences kept fighting
  # with the literal escape bytes in the color variables.
  local info_l1_plain info_l2_plain
  info_l1_plain=$(printf '%s\n' "$info_text" | sed -n '1p' | awk -v w="$left_w" '{printf "%-"w"."w"s", $0}')
  info_l2_plain=$(printf '%s\n' "$info_text" | sed -n '2p' | awk -v w="$left_w" '{printf "%-"w"."w"s", $0}')
  local info_rest
  info_rest=$(printf '%s\n' "$info_text" | tail -n +3 | grep -v '^$' | awk -v w="$left_w" '{printf "%-"w"."w"s|\n", $0}')

  colorize_label() {
    local line=$1 label=$2 value_color=$3
    local prefix="${line%%:*}:"
    local rest="${line#*:}"
    printf '%b%s%b%b%s%b' "$CYAN" "$prefix" "$RESET" "$value_color" "$rest" "$RESET"
  }

  local info_l1 info_l2
  info_l1=$(colorize_label "$info_l1_plain" branch "$BOLD_WHITE")
  info_l2=$(colorize_label "$info_l2_plain" path "$DIM")

  local tmux_l1_colored tmux_l2_colored
  if [[ "$tmux_l1" == \ session:* ]]; then
    tmux_l1_colored=$(colorize_label "$tmux_l1" session "$BOLD_WHITE")
    local status_color status_word
    if [[ "$tmux_l1" == *attached* ]]; then
      status_color="$GREEN" status_word="attached"
    else
      status_color="$DIM" status_word="detached"
    fi
    tmux_l1_colored=$(printf '%s' "$tmux_l1_colored" | awk -v w="$status_word" -v c="$status_color" -v r="$RESET" \
      '{gsub(w, c w r); print}')
  else
    tmux_l1_colored="$tmux_l1"
  fi
  if [[ "$tmux_l2" == \ running:* ]]; then
    tmux_l2_colored=$(colorize_label "$tmux_l2" running "$GREEN")
  else
    tmux_l2_colored="$tmux_l2"
  fi

  paste -d'|' \
    <(printf '%s\n%s\n' "$info_l1" "$info_l2") \
    <(printf '%s\n%s\n' "$tmux_l1_colored" "$tmux_l2_colored")
  [[ -n "$info_rest" ]] && printf '%s\n' "$info_rest"

  # Full-width divider (no blank line before it), then the live pane content
  # spans the entire width.
  printf '%s\n' "$(printf '%*s' "$cols" '' | tr ' ' '-')"

  local n=$(( lines > info_line_count + 4 ? lines - info_line_count - 4 : 1 ))
  pane_preview "$repo" "$task" "$n"
}

# ctrl-s preview: `git status` for the selected worktree, in place of the
# usual info+tmux split. Deliberately just `git status` (not --short or a
# diff) — the ask was to check status without spawning a session, i.e. the
# same output you'd see cd'ing in and running it by hand.
git_status_preview() {
  local repo=$1 task=$2
  local wt; wt=$(find_worktree "$repo" "$task"); [[ -n "$wt" ]] || wt="${ORCH_WORKTREES_ROOTS[0]}/$repo/$task"
  [[ -d "$wt" ]] || { echo "(worktree not found: $wt)"; return; }
  git -C "$wt" status 2>&1
}

case "$1" in
  rows) rows ;;
  end-task) end_task "$2" "$3"; rows ;;
  confirm-end-task) confirm_end_task "$2" "$3" ;;
  kill-task) kill_task "$2" "$3"; rows ;;
  confirm-kill-task) confirm_kill_task "$2" "$3" ;;
  split-preview) split_preview "$2" "$3" ;;
  git-status-preview) git_status_preview "$2" "$3" ;;
  touch-access) touch_access "$2" "$3" ;;
  *) echo "usage: $0 rows|end-task|confirm-end-task|kill-task|confirm-kill-task|split-preview|git-status-preview|touch-access <repo> <task>" >&2; exit 1 ;;
esac
