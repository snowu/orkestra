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

    # repo/task (fields 1,2) are used verbatim by fzf's {1}/{2} for
    # end-task/jump/full-row lookups — never truncate those, only the
    # cosmetic columns after them.
    printf "%s\t%-16s %-32s %-14s %-8s %-16s %-9s %s\n" "$mtime" \
      "$repo" "$task" "$(trunc "${branch_col:-none}" 14)" \
      "$state" "$(trunc "$session" 16)" "$last_used" "$(trunc "$cmd" 12)"
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

# Untruncated detail for the currently selected row, shown in the preview
# pane so long repo/task/branch/session names are never fully hidden — they
# just get cut with "..." in the list itself to keep columns aligned.
full_row() {
  local repo=$1 task=$2
  local wt="$HOME/worktrees/$repo/$task"
  local branch=$(git -C "$wt" branch --show-current 2>/dev/null)

  echo "repo:    $repo"
  echo "task:    $task"
  echo "branch:  ${branch:-none}"
  echo "path:    $wt"

  # Same live rule as rows(): match by cwd, or by a session named after this
  # task (sessions are shared by task name by default across repos).
  local pane_info
  pane_info=$(tmux list-panes -a -F "#{session_name}:#{window_index}.#{pane_index}	#{pane_current_path}	#{pane_current_command}	#{pane_pid}" 2>/dev/null | \
    awk -F'\t' -v p="$wt" '$2==p{print; exit}')

  if [[ -z "$pane_info" ]] && tmux has-session -t "=$task" 2>/dev/null; then
    pane_info=$(tmux list-panes -t "=$task" -F "#{session_name}:#{window_index}.#{pane_index}	#{pane_current_path}	#{pane_current_command}	#{pane_pid}" 2>/dev/null | head -1)
  fi

  if [[ -n "$pane_info" ]]; then
    local sess win_cmd pid pane_cwd
    sess=$(echo "$pane_info" | cut -f1)
    pane_cwd=$(echo "$pane_info" | cut -f2)
    win_cmd=$(echo "$pane_info" | cut -f3)
    pid=$(echo "$pane_info" | cut -f4)
    local nwin nclients
    nwin=$(tmux list-windows -t "${sess%%:*}" 2>/dev/null | wc -l)
    nclients=$(tmux list-clients -t "${sess%%:*}" 2>/dev/null | wc -l)
    echo
    echo "tmux session: ${sess%%:*}"
    echo "  pane:      $sess (pid $pid, running: $win_cmd)"
    [[ "$pane_cwd" != "$wt" ]] && echo "  pane cwd:  $pane_cwd (shared session, different repo's worktree)"
    echo "  windows:   $nwin"
    echo "  attached:  $([[ $nclients -gt 0 ]] && echo "yes ($nclients client(s))" || echo "no")"
  else
    echo
    echo "tmux session: none"
  fi

  echo
  tail -n 5 /tmp/orch.log 2>/dev/null
}

touch_access() {
  local repo=$1 task=$2
  mkdir -p "$ACCESS_DIR"
  touch "$(access_file "$repo" "$task")"
}

case "$1" in
  rows) rows ;;
  end-task) end_task "$2" "$3"; rows ;;
  confirm-end-task) confirm_end_task "$2" "$3" ;;
  full-row) full_row "$2" "$3" ;;
  touch-access) touch_access "$2" "$3" ;;
  *) echo "usage: $0 rows|end-task|confirm-end-task|full-row|touch-access <repo> <task>" >&2; exit 1 ;;
esac
