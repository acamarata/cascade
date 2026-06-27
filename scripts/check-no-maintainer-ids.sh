#!/usr/bin/env bash
# CI guard: exits 1 if maintainer-specific dev-machine paths or a private email
# appear in any SHIPPED (git-tracked) file. Checking git-tracked files only is
# deliberate — it ignores gitignored build artifacts (Xcode DerivedData, target/,
# node_modules/) and local-only trees (.claude/, .opencode/) that never ship.
#
# Public docs that show generic account-ID patterns (e.g. `claude-acc1` as a
# `{provider}-acc{N}` naming-convention example) are NOT private identifiers and
# are intentionally not in the denylist.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

DENYLIST=(
  '/Users/admin'
  '/Volumes/X9/Sites'
  'alisalaah@gmail.com'
)

# Git-tracked text files only; exclude this script (it contains the patterns).
mapfile -t FILES < <(git ls-files -- \
  '*.rs' '*.ts' '*.tsx' '*.js' '*.md' '*.json' '*.toml' '*.yaml' '*.yml' '*.sh' \
  ':!:scripts/check-no-maintainer-ids.sh')

FOUND=0
for pattern in "${DENYLIST[@]}"; do
  if [ "${#FILES[@]}" -gt 0 ]; then
    MATCHES=$(grep -nF "$pattern" "${FILES[@]}" 2>/dev/null || true)
    if [ -n "$MATCHES" ]; then
      echo "FAIL: found denylist pattern '$pattern':"
      echo "$MATCHES"
      FOUND=1
    fi
  fi
done

if [ "$FOUND" -eq 1 ]; then
  echo ""
  echo "check-no-maintainer-ids: FAILED — remove all maintainer-specific identifiers before shipping."
  exit 1
fi

echo "check-no-maintainer-ids: PASSED — no maintainer identifiers in tracked files."
exit 0
