#!/usr/bin/env bash
# Non-interactive helper for orch. Called directly by fzf bindings
# (no `source`, no `-i`) to avoid repeated dotfile-sourcing overhead and TTY
# job-control issues when invoked from fzf's execute()/reload().
set -u

ACCESS_DIR="$HOME/.cache/orch/access"

# Path of the access-marker file for a repo/task pair.
access_file() {
  printf '%s/%s__%s' "$ACCESS_DIR" "$1" "$2"
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
  for wt in "$HOME"/worktrees/*/*/; do
    wt="${wt%/}"
    [[ -d "$wt" ]] || continue
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

    # Live if either: a pane's cwd is exactly this worktree, OR a tmux
    # session is named after this task — sessions are shared by task name by
    # default (one agent spanning multiple repos), so a BE worktree should
    # show live too once the FE worktree's alt-enter started "task-x", even
    # though no pane's cwd points at the BE folder.
    pane_info=$(tmux list-panes -a -F "#{session_name}:#{window_index}.#{pane_index}	#{pane_current_path}	#{pane_current_command}" 2>/dev/null | \
      awk -F'\t' -v p="$wt" '$2==p{print; exit}')

    session="-" cmd="-" state="idle"
    if [[ -n "$pane_info" ]]; then
      session=$(echo "$pane_info" | cut -f1 | cut -d: -f1)
      cmd=$(echo "$pane_info" | cut -f3)
      state="live"
    elif tmux has-session -t "=$task" 2>/dev/null; then
      session="$task"
      cmd=$(tmux list-panes -t "=$task" -F "#{pane_current_command}" 2>/dev/null | head -1)
      state="live"
    fi

    local branch_col="$branch"
    [[ "$branch" == "$task" ]] && branch_col="="

    local state_color="$YELLOW"
    [[ "$state" == "live" ]] && state_color="$GREEN"

    # repo/task (fields 1,2) are used verbatim by fzf's {1}/{2} for
    # end-task/jump/full-row lookups — color codes are safe here since fzf
    # (with --ansi) strips them before splitting into positional fields, but
    # the *padding width* must still be computed on the plain text, or the
    # invisible escape bytes would throw off column alignment.
    local repo_padded task_padded state_padded
    repo_padded=$(printf '%-16s' "$repo")
    task_padded=$(printf '%-32s' "$task")
    state_padded=$(printf '%-8s' "$state")

    printf "%s\t$(repo_color "$repo")%s${RESET} %-32s %-14s ${state_color}%s${RESET} %-16s %-9s %s\n" "$mtime" \
      "$repo_padded" "$task_padded" "$(trunc "${branch_col:-none}" 14)" \
      "$state_padded" "$(trunc "$session" 16)" "$last_used" "$(trunc "$cmd" 12)"
  done | sort -t$'\t' -k1,1rn | cut -f2-
}

end_task() {
  local repo=$1 task=$2
  local wt="$HOME/worktrees/$repo/$task"

  local target
  target=$(tmux list-panes -a -F "#{session_name}:#{window_index}.#{pane_index}	#{pane_current_path}" 2>/dev/null | \
    awk -F'\t' -v p="$wt" '$2==p{print $1; exit}')
  [[ -n "$target" ]] && tmux kill-session -t "${target%%:*}" 2>/dev/null

  (
    cd "$HOME/code/$repo" 2>/dev/null || exit 1
    git worktree remove "$wt" --force 2>>/tmp/orch.log
    git worktree prune
    git branch -D "$task" 2>>/tmp/orch.log
    git push origin --delete "$task" 2>>/tmp/orch.log
  )
  [[ -d "$wt" ]] && rm -rf "$wt"
  rm -f "$(access_file "$repo" "$task")"
  echo "$(date '+%H:%M:%S') removed $repo/$task (worktree+branch, local+origin)" >> /tmp/orch.log
}

# Runs with terminal access (called via fzf's execute(), not reload()) so it
# can prompt interactively before deleting anything.
confirm_end_task() {
  local repo=$1 task=$2
  printf "Delete worktree + branch (local & origin) for %s/%s? [y/N] " "$repo" "$task" > /dev/tty
  local ans
  read -r ans < /dev/tty
  if [[ "$ans" =~ ^[Yy]$ ]]; then
    end_task "$repo" "$task"
  else
    echo "$(date '+%H:%M:%S') skipped $repo/$task (not confirmed)" >> /tmp/orch.log
  fi
}

# Resolve the live tmux pane (if any) for a repo/task, same rule as rows():
# match by cwd, or by a session named after this task (sessions are shared
# by task name across repos by default). Prints tab-separated pane_info or
# nothing if there's no live pane.
_orch_resolve_pane() {
  local repo=$1 task=$2
  local wt="$HOME/worktrees/$repo/$task"
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
  local wt="$HOME/worktrees/$repo/$task"
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
  local wt="$HOME/worktrees/$repo/$task"
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

case "$1" in
  rows) rows ;;
  end-task) end_task "$2" "$3"; rows ;;
  confirm-end-task) confirm_end_task "$2" "$3" ;;
  split-preview) split_preview "$2" "$3" ;;
  touch-access) touch_access "$2" "$3" ;;
  *) echo "usage: $0 rows|end-task|confirm-end-task|split-preview|touch-access <repo> <task>" >&2; exit 1 ;;
esac
