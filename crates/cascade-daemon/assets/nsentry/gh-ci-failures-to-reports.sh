#!/usr/bin/env bash
# gh-ci-failures-to-reports — bridge GitHub-hosted Actions failures into nSentry MD reports.
# Lists failed runs across an org's repos, dedups to one report per (repo, workflow, sha),
# and writes timestamped MD reports to --out (default /opt/nself-ops/errors). Run on the
# sentry box (gh authed via a token) on a cron, or locally. Idempotent via a seen-manifest.
#
# Age cutoff: a run's createdAt must be within --max-age-days (default 3) of "now" or it is
# skipped, even if unseen. Without this, a repo's first-ever scan (or a seen-manifest that
# lags behind a repo being added to --repos, or a freshly-touched empty .gh-seen after a
# redeploy) would backfill-alert on every historical failure `gh run list --status failure`
# returns, however old — a real incident on 2026-07-06 fired on a run from 2025-08-06 this way
# (acamarata/ali, "Generate Khutbah PDFs", run 16791093606).
#
# Usage: gh-ci-failures-to-reports.sh [--org nself-org] [--out DIR] [--per-repo N] [--repos "a b c"] [--max-age-days N]
set -euo pipefail
ORG="${GH_ORG:-}"
OUT="${NSENTRY_REMOTE_DIR:-/opt/nself-ops/errors}"
PER=10
REPOS=""
MAX_AGE_DAYS="${NSENTRY_MAX_AGE_DAYS:-3}"
while [ $# -gt 0 ]; do case "$1" in
  --org) ORG="$2"; shift 2;; --out) OUT="$2"; shift 2;;
  --per-repo) PER="$2"; shift 2;; --repos) REPOS="$2"; shift 2;;
  --max-age-days) MAX_AGE_DAYS="$2"; shift 2;; *) shift;; esac; done
mkdir -p "$OUT"; SEEN="$OUT/.gh-seen"; touch "$SEEN"
[ -n "$REPOS" ] || REPOS=$(gh repo list "$ORG" --no-archived --limit 100 --json name -q '.[].name')
ts(){ date -u +%Y%m%d-%H%M%S; }
now_epoch=$(date -u +%s)
cutoff_epoch=$((now_epoch - MAX_AGE_DAYS * 86400))
# Portable ISO-8601 → epoch: macOS (date -j -f) first, then GNU/Linux (date -d) on the sentry box.
to_epoch(){ date -u -j -f '%Y-%m-%dT%H:%M:%SZ' "$1" +%s 2>/dev/null || date -u -d "$1" +%s 2>/dev/null; }
n=0
for r in $REPOS; do
  # one entry per workflow (latest failed run), so we don't spam per-run
  gh run list --repo "$ORG/$r" --status failure --limit "$PER" \
    --json workflowName,headSha,displayTitle,url,createdAt,event \
    -q '.[] | [.workflowName,.headSha,.displayTitle,.url,.createdAt,.event] | @tsv' 2>/dev/null | \
  awk -F'\t' '!seen[$1]++' | while IFS=$'\t' read -r wf sha title url created event; do
    [ -z "$wf" ] && continue
    run_epoch=$(to_epoch "$created")
    if [ -n "$run_epoch" ] && [ "$run_epoch" -lt "$cutoff_epoch" ]; then
      echo "SKIP stale run (>${MAX_AGE_DAYS}d old): $r/$wf @ $created"
      continue
    fi
    key="ghci:$r:$wf:${sha:0:8}"
    grep -qxF "$key" "$SEEN" && continue
    h=$(printf '%s' "$key" | md5sum 2>/dev/null | cut -c1-6 || printf '%s' "$key" | md5 | cut -c1-6)
    f="$OUT/$(ts)-$h-ci-$r.md"
    { echo "---"; echo "id: $key"; echo "created_at: $(date -u +%FT%TZ)";
      echo "title: \"CI failed: $ORG/$r — $wf\""; echo "severity: high"; echo "source: github-actions";
      echo "repo: $ORG/$r"; echo "workflow: \"$wf\""; echo "sha: $sha"; echo "---"; echo;
      echo "# CI failed: $ORG/$r — $wf"; echo;
      echo "- **Commit:** \`${sha:0:8}\` · **Event:** $event · **When:** $created";
      echo "- **Title:** $title"; echo "- **Run:** $url"; echo;
      echo "Routed by nSentry (GitHub-Actions bridge) → your .claude/inbox. Fix or migrate this workflow to the self-hosted runner."; } > "$f"
    echo "$key" >> "$SEEN"; echo "WROTE $f"
  done
done
echo "gh-ci bridge done → $OUT (new reports listed above; deduped via .gh-seen)"
