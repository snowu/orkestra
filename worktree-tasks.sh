# Minimal worktree lifecycle helpers used by agent-orch.zsh's new-task/end-task
# flows.

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

  # A hardcoded "master" broke on any repo whose default branch is "main"
  # (or anything else) — confirmed live: `git worktree add ... master`
  # failed with "invalid reference: master" on a repo whose default is
  # main. origin/HEAD's symbolic ref is git's own record of the remote's
  # default branch; fall back to whatever branch is currently checked out
  # if there's no remote (e.g. a fresh local-only repo).
  local BASE_BRANCH
  BASE_BRANCH=$(git -C "$REPO_ROOT" symbolic-ref --short refs/remotes/origin/HEAD 2>/dev/null)
  BASE_BRANCH="${BASE_BRANCH#origin/}"
  [[ -z "$BASE_BRANCH" ]] && BASE_BRANCH=$(git -C "$REPO_ROOT" branch --show-current 2>/dev/null)
  if [[ -z "$BASE_BRANCH" ]]; then
    echo "Error: couldn't determine a base branch (no origin/HEAD, not on a branch)"
    return 1
  fi

  mkdir -p "$WORKTREE_BASE"
  git worktree add "$WORKTREE_BASE/$TASK_NAME" -b "$TASK_NAME" "$BASE_BRANCH"
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
    # REPO_ROOT (from `git rev-parse --show-toplevel` above) is the
    # WORKTREE's own root when run from inside one, not the main checkout —
    # cd'ing there would just land back in the directory we're about to
    # remove. The main checkout is always the first entry `git worktree
    # list` prints, regardless of where ORCH_CODE_ROOTS/ORCH_WORKTREES_ROOTS
    # or any other config says repos/worktrees live.
    local main_checkout
    main_checkout=$(git -C "$REPO_ROOT" worktree list --porcelain 2>/dev/null | \
      awk '/^worktree /{print $2; exit}')
    cd "${main_checkout:-$HOME}" 2>/dev/null || cd ~
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
