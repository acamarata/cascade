#!/usr/bin/env bash
# gh-dependabot-to-reports — bridge GitHub Dependabot security alerts into nSentry MD reports.
# Lists OPEN dependabot alerts across an org, dedups to one report per (repo, ghsa_id),
# and writes timestamped MD reports to --out (default /opt/nself-ops/errors). Run on the
# sentry box or locally on a cron. Idempotent via a seen-manifest. Complements
# gh-ci-failures-to-reports.sh (which covers Actions runs; this covers Dependabot alerts).
#
# Usage: gh-dependabot-to-reports.sh [--org nself-org] [--out DIR]
# Needs: a gh token with security_events (read) scope.
# NB: -e is intentionally omitted. The org-level Dependabot ALERTS endpoint 404s
# for a token without the org's Dependabot-read grant; with -e that would abort
# the whole script before the version-update PR stream runs. Empty results are
# already handled per-line (`[ -z "$repo" ] && continue`), so degrading gracefully
# is correct — the stream still delivers whatever it could read.
set -uo pipefail
ORG="${GH_ORG:-}"
OUT="${NSENTRY_REMOTE_DIR:-/opt/nself-ops/errors}"
while [ $# -gt 0 ]; do case "$1" in
  --org) ORG="$2"; shift 2;; --out) OUT="$2"; shift 2;; *) shift;; esac; done
mkdir -p "$OUT"; SEEN="$OUT/.dependabot-seen"; touch "$SEEN"
ts(){ date -u +%Y%m%d-%H%M%S; }

# Pull every open alert org-wide; emit one TSV line per alert.
gh api "/orgs/$ORG/dependabot/alerts" -X GET -f state=open --paginate \
  -q '.[] | [.repository.name, .security_advisory.ghsa_id, .security_advisory.severity, .dependency.package.name, (.security_advisory.summary | gsub("[\t\n]";" ")), .html_url] | @tsv' 2>/dev/null | \
while IFS=$'\t' read -r repo ghsa sev pkg summary url; do
  [ -z "$repo" ] && continue
  key="dependabot:$repo:$ghsa"
  grep -qxF "$key" "$SEEN" && continue
  h=$(printf '%s' "$key" | md5sum 2>/dev/null | cut -c1-6 || printf '%s' "$key" | md5 | cut -c1-6)
  # map severity → report severity
  case "$sev" in critical) rs=critical;; high) rs=high;; medium) rs=medium;; *) rs=low;; esac
  f="$OUT/$(ts)-$h-dependabot-$repo.md"
  { echo "---"; echo "id: $key"; echo "created_at: $(date -u +%FT%TZ)";
    echo "title: \"Dependabot $sev: $ORG/$repo — $pkg\""; echo "severity: $rs"; echo "source: dependabot";
    echo "repo: $ORG/$repo"; echo "package: \"$pkg\""; echo "ghsa: $ghsa"; echo "---"; echo;
    echo "# Dependabot $sev: $ORG/$repo — $pkg"; echo;
    echo "- **Package:** \`$pkg\` · **Severity:** $sev · **Advisory:** $ghsa";
    echo "- **Summary:** $summary"; echo "- **Alert:** $url"; echo;
    echo "Routed by nSentry (Dependabot bridge) → your .claude/inbox. Review + bump or dismiss the alert."; } > "$f"
  echo "$key" >> "$SEEN"; echo "WROTE $f"
done

# --- Dependabot VERSION-UPDATE PRs (a separate stream from security alerts) ---
# These generate PR/watch emails, not alert emails, and are NOT in /dependabot/alerts.
# Capture open dependabot PRs org-wide so they land in the inbox too.
gh search prs --owner "$ORG" --author app/dependabot --state open --limit 200 \
  --json repository,number,title,url,createdAt \
  -q '.[] | [.repository.name, (.number|tostring), .title, .url, .createdAt] | @tsv' 2>/dev/null | \
while IFS=$'\t' read -r repo num title url created; do
  [ -z "$repo" ] && continue
  key="dependabot-pr:$repo:$num"
  grep -qxF "$key" "$SEEN" && continue
  h=$(printf '%s' "$key" | md5sum 2>/dev/null | cut -c1-6 || printf '%s' "$key" | md5 | cut -c1-6)
  f="$OUT/$(ts)-$h-dependabot-pr-$repo.md"
  { echo "---"; echo "id: $key"; echo "created_at: $(date -u +%FT%TZ)";
    echo "title: \"Dependabot PR: $ORG/$repo #$num\""; echo "severity: low"; echo "source: dependabot-pr";
    echo "repo: $ORG/$repo"; echo "pr: $num"; echo "---"; echo;
    echo "# Dependabot PR: $ORG/$repo #$num"; echo;
    echo "- **$title**"; echo "- **PR:** $url · **Opened:** $created"; echo;
    echo "Routed by nSentry (Dependabot PR bridge) → your .claude/inbox. Review + merge or close. Patch/minor with green CI is usually safe to merge."; } > "$f"
  echo "$key" >> "$SEEN"; echo "WROTE $f"
done

echo "dependabot bridge done → $OUT (deduped via .dependabot-seen)"
