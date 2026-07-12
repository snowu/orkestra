#!/usr/bin/env bash
# Non-interactive helper for orch. Called directly by fzf bindings
# (no `source`, no `-i`) to avoid repeated dotfile-sourcing overhead and TTY
# job-control issues when invoked from fzf's execute()/reload().
set -u

rows() {
  for wt in "$HOME"/worktrees/*/*/; do
    wt="${wt%/}"
    [[ -d "$wt" ]] || continue
    repo=$(basename "$(dirname "$wt")")
    task=$(basename "$wt")
    branch=$(git -C "$wt" branch --show-current 2>/dev/null)

    pane_info=$(tmux list-panes -a -F "#{session_name}:#{window_index}.#{pane_index}	#{pane_current_path}	#{pane_current_command}" 2>/dev/null | \
      awk -F'\t' -v p="$wt" '$2==p{print; exit}')

    cmd="-" state="idle"
    if [[ -n "$pane_info" ]]; then
      cmd=$(echo "$pane_info" | cut -f3)
      state="live"
    fi

    local branch_col="$branch"
    [[ "$branch" == "$task" ]] && branch_col="="
    printf "%-16s %-30s %-14s %-8s %s\n" "$repo" "$task" "${branch_col:-none}" "$state" "$cmd"
  done
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

case "$1" in
  rows) rows ;;
  end-task) end_task "$2" "$3"; rows ;;
  confirm-end-task) confirm_end_task "$2" "$3" ;;
  *) echo "usage: $0 rows|end-task|confirm-end-task <repo> <task>" >&2; exit 1 ;;
esac
