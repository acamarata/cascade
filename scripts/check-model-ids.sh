#!/usr/bin/env bash
# CI guard: exits 1 if one of the 8 canonical fleet model-ID string literals
# (as currently defined in crates/cascade-types/src/model_ids.rs) is
# hardcoded anywhere in crates/**/*.rs, apps/cascade-app/src/**/*.{ts,tsx},
# or src/widget/macos/**/*.swift instead of importing/using the constant.
#
# crates/cascade-types/src/model_ids.rs is the single source of truth for
# these 8 literals (MODEL_CLAUDE_OPUS, MODEL_CLAUDE_SONNET, MODEL_CLAUDE_HAIKU,
# MODEL_CLAUDE_FABLE, MODEL_GPT, MODEL_GEMINI_PRO, MODEL_GEMINI_FLASH,
# MODEL_GLM). cascade-types is the workspace's leaf crate (DAG root), so every
# other crate — including cascade-providers and cascade-mcp, which do not
# depend on cascade-core — can import these constants directly, making the
# doctrine enforceable workspace-wide. Every other call site that needs the
# CURRENT fleet model for one of these roles must import the constant instead
# of re-typing the literal — otherwise a fleet model swap requires a
# grep-and-pray sweep instead of a one-line const edit.
#
# crates/cascade-core/src/model_ids.rs re-exports these same constants
# (`pub use cascade_types::model_ids::*;`) for backward compatibility with
# existing `cascade_core::model_ids::X` call sites. It has no literals of its
# own, so it needs no separate exclusion from this script.
#
# Deliberately narrow scope: this does NOT flag every provider-prefixed
# model-ID-shaped string in the codebase. Provider adapters (anthropic.rs,
# openai/*.rs, gemini/*.rs, cost.rs, opencode.rs, etc.) and test fixtures
# legitimately reference many OTHER model IDs (dated snapshots like
# claude-3-5-sonnet-20241022, other-tier models like gpt-4o, account-id
# strings like claude2/claude-pooled-1) that are not duplicates of the 8
# doctrine constants — see model_ids.rs's own header note on this. Flagging
# those would be all noise. This script only guards re-typing of the exact
# 8 CURRENT doctrine literals.
#
# Exclusions (legitimate re-mentions, not doctrine drift):
#   - crates/cascade-types/src/model_ids.rs itself (the doctrine file)
#   - any path containing /tests/
#   - any file named *_test.rs or test_*.rs
#   - lines that are doc comments (///, //!) or where the match only
#     appears after a `//` comment marker earlier on the line
#
# Usage: scripts/check-model-ids.sh
# Exit 0: no violations. Exit 1: one or more violations (printed as
# file:line: <content>, plus a summary count).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

DOCTRINE_FILE="crates/cascade-types/src/model_ids.rs"

if [ ! -f "$DOCTRINE_FILE" ]; then
  echo "check-model-ids: ERROR — doctrine file not found at ${DOCTRINE_FILE}"
  exit 1
fi

# Extract the 8 canonical literal values from `pub const MODEL_*: &str = "...";`
# lines. Deliberately excludes DEFAULT_HARNESS_MODEL, which aliases another
# const (`= MODEL_CLAUDE_SONNET;`) rather than declaring a new literal.
# Portable read loop instead of `mapfile` (bash 4+ only) — macOS ships bash 3.2
# as /bin/bash and callers may invoke this script via a bare `bash` that
# resolves to it rather than a newer homebrew bash.
LITERALS=()
while IFS= read -r lit; do
  [ -n "$lit" ] && LITERALS+=("$lit")
done < <(
  grep -E '^pub const MODEL_[A-Z_]+: &str = "[^"]+";' "$DOCTRINE_FILE" \
    | sed -E 's/^pub const MODEL_[A-Z_]+: &str = "([^"]+)";.*/\1/'
)

if [ "${#LITERALS[@]}" -eq 0 ]; then
  echo "check-model-ids: ERROR — no MODEL_* literal constants found in ${DOCTRINE_FILE}"
  exit 1
fi

# Build a single alternation regex, escaping regex-special chars in each
# literal (only '.' appears in practice, e.g. "gpt-5.5", "glm-5.2").
ESCAPED=()
for lit in "${LITERALS[@]}"; do
  ESCAPED+=("$(printf '%s' "$lit" | sed -E 's/[.]/\\./g')")
done
IFS='|'
ALT="${ESCAPED[*]}"
unset IFS
PATTERN="\"(${ALT})\""

# Collect candidate source files: Rust under crates/, TS/TSX under apps/cascade-app/src/,
# and Swift under src/widget/macos/. Exclude the doctrine file, test paths, and test files.
FILES=()
while IFS= read -r f; do
  [ -n "$f" ] && FILES+=("$f")
done < <(
  {
    find crates -type f -name '*.rs' \
      ! -path "*/tests/*" \
      ! -name '*_test.rs' \
      ! -name 'test_*.rs'
    find apps/cascade-app/src -type f \( -name '*.ts' -o -name '*.tsx' \) \
      ! -path '*/__tests__/*' \
      ! -name '*.test.ts' \
      ! -name '*.test.tsx'
    find src/widget/macos -type f -name '*.swift' 2>/dev/null || true
  }
)

VIOLATIONS=()

for file in "${FILES[@]}"; do
  rel="${file#./}"
  if [ "$rel" = "$DOCTRINE_FILE" ]; then
    continue
  fi

  while IFS= read -r line; do
    [ -z "$line" ] && continue
    lineno="${line%%:*}"
    content="${line#*:}"

    trimmed="${content#"${content%%[![:space:]]*}"}"
    case "$trimmed" in
      '///'*|'//!'*|'*'*)
        # Rust doc comments (///, //!) and block-comment lines (* ...) in TS/Swift/Rust
        continue
        ;;
    esac

    # If `//` appears in the line, only count it as a violation when the
    # pattern still matches the portion of the line BEFORE the `//`
    # (otherwise the literal is inside an inline comment).
    before_comment="${content%%//*}"
    if [ "$before_comment" != "$content" ]; then
      if ! echo "$before_comment" | grep -qE "$PATTERN"; then
        continue
      fi
    fi

    VIOLATIONS+=("${file}:${lineno}: ${trimmed}")
  done < <(grep -nE "$PATTERN" "$file" || true)
done

if [ "${#VIOLATIONS[@]}" -eq 0 ]; then
  echo "check-model-ids: PASSED — no hardcoded canonical model-ID literals outside ${DOCTRINE_FILE}."
  exit 0
fi

echo "check-model-ids: FAILED — hardcoded canonical model-ID literal(s) found outside ${DOCTRINE_FILE}:"
for v in "${VIOLATIONS[@]}"; do
  echo "  ${v}"
done
echo ""
echo "Summary: ${#VIOLATIONS[@]} violation(s). Import the constant from ${DOCTRINE_FILE} instead of hardcoding the literal."
exit 1
