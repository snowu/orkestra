# Thin shims over the ork binary's new-task/end-task subcommands — kept as
# shell functions only because a subprocess can't cd the caller's shell.
# The actual worktree/branch logic lives in Go (internal/worktree/ops.go).

new-task() {
  if [ -z "$1" ]; then
    echo "Usage: new-task <task-name>"
    return 1
  fi
  local wt
  wt=$(command ork new-task "$1") || return 1
  [ -d "$wt" ] && cd "$wt"
}

end-task() {
  local dest
  dest=$(command ork end-task "$@") || return 1
  # ork prints the main checkout to land in when the removed worktree was
  # the current dir.
  [ -n "$dest" ] && [ -d "$dest" ] && cd "$dest"
}
