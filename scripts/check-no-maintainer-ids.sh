#!/usr/bin/env bash
# CI guard: exits 1 if maintainer-specific dev-machine paths, private email,
# or maintainer-owned hostnames appear in any SHIPPED (git-tracked) file.
# Checking git-tracked files only is deliberate — it ignores gitignored build
# artifacts (Xcode DerivedData, target/, node_modules/) and local-only trees
# that never ship.
#
# NOTE (2026-08-18): .claude/ is untracked and therefore genuinely never ships,
# but .opencode/ IS git-tracked (the SPORT master lists) and DOES ship. An
# earlier version of this comment claimed both were local-only, which would have
# left public files unscanned. Because this script scans git-tracked files,
# .opencode/ is in fact covered — the comment was the only thing that was wrong.
#
# Scanned directories include .forgejo/ so workflow files are also guarded.
#
# Public docs that show generic account-ID patterns (e.g. `claude-acc1` as a
# `{provider}-acc{N}` naming-convention example) are NOT private identifiers
# and are intentionally not in the denylist.
#
# Allowed exception: the canonical repo URL github.com/acamarata/cascade and
# the matching Cargo.toml repository field are permitted — they are public
# identity, not maintainer-private infrastructure.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# Fixed strings: checked via grep -F (no regex interpretation).
FIXED_DENYLIST=(
  '/Users/admin'
  '/Volumes/X9/Sites'
  'alisalaah@gmail.com'
)

# Regex patterns: checked via grep -E.
# Matches maintainer-owned domains. The github.com/acamarata repo URL is
# intentionally excluded via grep -v below (it is public identity, not infra).
REGEX_DENYLIST=(
  'ariccamarata\.com'
  'nself\.org'
  'unity\.dev'
  'ummat\.dev'
)

# Git-tracked text files, including .forgejo/; exclude this script itself.
mapfile -t FILES < <(git ls-files -- \
  '*.rs' '*.ts' '*.tsx' '*.js' '*.md' '*.json' '*.toml' '*.yaml' '*.yml' '*.sh' \
  ':!:scripts/check-no-maintainer-ids.sh')

FOUND=0

# --- Fixed-string checks ---
for pattern in "${FIXED_DENYLIST[@]}"; do
  if [ "${#FILES[@]}" -gt 0 ]; then
    MATCHES=$(grep -nF "$pattern" "${FILES[@]}" 2>/dev/null || true)
    if [ -n "$MATCHES" ]; then
      echo "FAIL: found denylist pattern '$pattern':"
      echo "$MATCHES"
      FOUND=1
    fi
  fi
done

# --- Regex domain checks (with allowed-exception filter) ---
# Allow: lines whose only ariccamarata.com hit is the canonical github.com repo URL
# (e.g. "repository = \"https://github.com/acamarata/cascade\"").
for pattern in "${REGEX_DENYLIST[@]}"; do
  if [ "${#FILES[@]}" -gt 0 ]; then
    MATCHES=$(grep -nE "$pattern" "${FILES[@]}" 2>/dev/null \
      | grep -v 'github\.com/acamarata' \
      || true)
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
