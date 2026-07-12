# Minimal worktree lifecycle helpers used by agent-orch.zsh's new-task/end-task
# flows. Adjust the default base branch (currently "master") to match your repo.

new-task() {
  local TASK_NAME=$1

  if [ -z "$TASK_NAME" ]; then
    echo "Usage: new-task <task-name>"
    return 1
  fi

  local REPO_ROOT=$(git rev-parse --show-toplevel 2>/dev/null)
  if [ -z "$REPO_ROOT" ]; then
    echo "Error: Not inside a git repository"
    return 1
  fi

  git worktree prune

  local REPO_NAME=$(basename "$REPO_ROOT")
  [[ -f "$HOME/.orch.conf" ]] && source "$HOME/.orch.conf"
  local WORKTREE_ROOT="${ORCH_WORKTREES_ROOTS[0]:-$HOME/worktrees}"
  local WORKTREE_BASE="$WORKTREE_ROOT/$REPO_NAME"

  mkdir -p "$WORKTREE_BASE"
  git worktree add "$WORKTREE_BASE/$TASK_NAME" -b "$TASK_NAME" master
  cd "$WORKTREE_BASE/$TASK_NAME"

  if [ -f "$REPO_ROOT/.env.local" ]; then
    cp "$REPO_ROOT/.env.local" .env.local
    echo "Copied .env.local"
  fi

  echo "Worktree ready at $WORKTREE_BASE/$TASK_NAME"
}

end-task() {
  local TASK_NAME=$1

  [[ -f "$HOME/.orch.conf" ]] && source "$HOME/.orch.conf"
  if [ -z "$TASK_NAME" ]; then
    local root in_worktree=0
    for root in "${ORCH_WORKTREES_ROOTS[@]:-$HOME/worktrees}"; do
      [[ "$PWD" == "$root/"* ]] && { in_worktree=1; break; }
    done
    if [[ "$in_worktree" -eq 1 ]]; then
      TASK_NAME=$(basename "$PWD")
    else
      echo "Usage: end-task <task-name>"
      echo "Or run from inside a worktree directory"
      return 1
    fi
  fi

  local REPO_ROOT=$(git rev-parse --show-toplevel 2>/dev/null)
  if [ -z "$REPO_ROOT" ]; then
    echo "Error: Not inside a git repository"
    return 1
  fi

  local REPO_NAME=$(basename "$REPO_ROOT")
  [[ -f "$HOME/.orch.conf" ]] && source "$HOME/.orch.conf"
  local WORKTREE_ROOT="${ORCH_WORKTREES_ROOTS[0]:-$HOME/worktrees}"
  local WORKTREE_PATH="$WORKTREE_ROOT/$REPO_NAME/$TASK_NAME"

  if [[ "$PWD" == "$WORKTREE_PATH"* ]]; then
    cd "${ORCH_CODE_ROOTS[0]:-$HOME/code}/$REPO_NAME" 2>/dev/null || cd ~
  fi

  git worktree remove "$WORKTREE_PATH" --force 2>/dev/null
  git worktree prune

  echo "Delete branch '$TASK_NAME'? (y/n)"
  read -r response
  if [[ "$response" =~ ^[Yy]$ ]]; then
    git branch -D "$TASK_NAME" 2>/dev/null
    echo "Branch deleted"
  fi

  echo "Worktree '$TASK_NAME' cleaned up"
}
