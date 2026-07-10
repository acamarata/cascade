#!/usr/bin/env bash
# Daily models/ freshness + drift check. Writes a report to the dev inbox so the
# models/ reference (github.com/acamarata/cascade/models) is kept current.
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
REPORT="$HOME/.cascade/models-freshness-$(date +%Y%m%d 2>/dev/null || echo today).md"
{
  echo "# models/ freshness check"
  bash scripts/check-models-consistency.sh 2>&1
  echo
  echo "model_ids.rs constants:"; grep -oE '= "[^"]+"' crates/cascade-types/src/model_ids.rs 2>/dev/null
} > "$REPORT" 2>&1
echo "wrote $REPORT"
