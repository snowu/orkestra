# Source this from ~/.bashrc or ~/.zshrc. The `ork` binary can't change your
# shell's cwd by itself (it's a subprocess) — it prints the target directory
# as its last stdout line instead, and this wrapper cd's into it.
ork() {
  local out dir
  out="$(command ork "$@")" || return $?
  dir=$(printf '%s' "$out" | tail -n1)
  [[ -n "$dir" && -d "$dir" ]] && cd "$dir"
}
