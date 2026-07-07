#!/usr/bin/env bash
# CI guard: verifies the 8 canonical model-ID literals in
# crates/cascade-types/src/model_ids.rs are all present as `id:` entries in
# models/models.yaml (the human/agent-facing reference doc), and warns if
# models.yaml has not been touched in over 90 days (stale-doc smell).
#
# (a) Extract the 8 literal values from model_ids.rs `pub const MODEL_*`
#     lines (excluding DEFAULT_HARNESS_MODEL, which aliases another const
#     rather than declaring a new literal).
# (b) Extract every `id:` value from models/models.yaml. The file's actual
#     shape is a nested provider/models list with entries shaped:
#       models:
#         - id: claude-opus-4-8
#     so a plain `- id: <value>` line match is sufficient — no full YAML
#     parser needed.
# (c) FAIL (exit 1) if any model_ids.rs literal is missing from the
#     models.yaml `id:` set.
# (d) WARN (does not affect exit code) if models/models.yaml's last commit
#     is older than 90 days.
#
# Usage: scripts/check-models-consistency.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

DOCTRINE_FILE="crates/cascade-types/src/model_ids.rs"
YAML_FILE="models/models.yaml"

if [ ! -f "$DOCTRINE_FILE" ]; then
  echo "check-models-consistency: ERROR — doctrine file not found at ${DOCTRINE_FILE}"
  exit 1
fi

if [ ! -f "$YAML_FILE" ]; then
  echo "check-models-consistency: ERROR — reference file not found at ${YAML_FILE}"
  exit 1
fi

# --- (a) Extract the 8 canonical literals from model_ids.rs ---
mapfile -t RS_LITERALS < <(
  grep -E '^pub const MODEL_[A-Z_]+: &str = "[^"]+";' "$DOCTRINE_FILE" \
    | sed -E 's/^pub const MODEL_[A-Z_]+: &str = "([^"]+)";.*/\1/'
)

if [ "${#RS_LITERALS[@]}" -eq 0 ]; then
  echo "check-models-consistency: ERROR — no MODEL_* literal constants found in ${DOCTRINE_FILE}"
  exit 1
fi

# --- (b) Extract every `id:` value from models.yaml ---
# Matches lines shaped `      - id: some-model-id` (list-item id under a
# `models:` block). Strips leading `- id:` / `id:` and surrounding
# whitespace/quotes.
mapfile -t YAML_IDS < <(
  grep -E '^[[:space:]]*-?[[:space:]]*id:[[:space:]]*' "$YAML_FILE" \
    | sed -E 's/^[[:space:]]*-?[[:space:]]*id:[[:space:]]*//' \
    | sed -E 's/^"(.*)"$/\1/' \
    | sed -E "s/^'(.*)'\$/\1/" \
    | sed -E 's/[[:space:]]+$//'
)

# --- (c) Verify every model_ids.rs literal appears in the yaml id set ---
MISSING=()
for lit in "${RS_LITERALS[@]}"; do
  found=0
  for yid in "${YAML_IDS[@]}"; do
    if [ "$lit" = "$yid" ]; then
      found=1
      break
    fi
  done
  if [ "$found" -eq 0 ]; then
    MISSING+=("$lit")
  fi
done

EXIT_CODE=0

if [ "${#MISSING[@]}" -eq 0 ]; then
  echo "check-models-consistency: all ${#RS_LITERALS[@]} model_ids.rs constants present as id: entries in ${YAML_FILE}."
else
  echo "check-models-consistency: FAILED — model_ids.rs constant(s) missing from ${YAML_FILE}:"
  for m in "${MISSING[@]}"; do
    echo "  ${m}"
  done
  echo ""
  echo "Summary: ${#MISSING[@]} of ${#RS_LITERALS[@]} constant(s) missing."
  EXIT_CODE=1
fi

# --- (d) Staleness check (WARN only, never affects exit code on its own) ---
LAST_COMMIT_TS="$(git log -1 --format=%ct -- "$YAML_FILE" 2>/dev/null || true)"
if [ -n "$LAST_COMMIT_TS" ]; then
  NOW_TS="$(date +%s)"
  AGE_DAYS=$(( (NOW_TS - LAST_COMMIT_TS) / 86400 ))
  if [ "$AGE_DAYS" -gt 90 ]; then
    echo "WARN: ${YAML_FILE} last changed ${AGE_DAYS} days ago (>90) — re-verify against live provider docs."
  else
    echo "check-models-consistency: ${YAML_FILE} last changed ${AGE_DAYS} day(s) ago (within 90-day freshness window)."
  fi
else
  echo "WARN: could not determine last commit date for ${YAML_FILE} (not tracked yet?)."
fi

exit "$EXIT_CODE"
