#!/usr/bin/env bash
set -euo pipefail

repo_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' EXIT
mkdir -p "$test_dir/bin" "$test_dir/destination"
cat > "$test_dir/bin/ork" <<'SH'
#!/usr/bin/env bash
case "$1" in
  check) echo 'Configuration OK' >&2 ;;
  fail) exit 7 ;;
  cd) printf '%s\n' "$2" ;;
esac
SH
chmod +x "$test_dir/bin/ork"
export PATH="$test_dir/bin:$PATH"
source "$repo_dir/ork.sh"
original_dir=$PWD
ork check
[[ "$PWD" == "$original_dir" ]]
status=0
ork fail || status=$?
[[ "$status" == 7 ]]
ork cd "$test_dir/destination"
[[ "$PWD" == "$test_dir/destination" ]]
echo 'Wrapper checks passed'
