#!/usr/bin/env bash
# provision-gfp-keys.sh — maximise the free Google (GF) pool by minting one
# API key per GCP project across every authed *human* gcloud account.
#
# Google's generativelanguage free tier is billed PER-PROJECT (not per-key), so
# N projects ≈ N× the free daily quota. For each authed gcloud account this
# script ensures up to $PROJECTS_PER_ACCOUNT projects exist, enables the
# generativelanguage API on each, and mints one API key restricted to that API.
#
# Fully-managed / self-healing behaviour:
#   • Enumerates ALL authed gcloud accounts, SKIPPING service accounts
#     (*.iam.gserviceaccount.com) — only real human accounts hold free quota.
#   • MERGES: never destroys the existing key set. Existing keys in
#     ~/.cascade/gfp-keys.env AND any GEMINI_FREE_KEY_* / GEMINI_KEY_* already
#     present in ~/.claude/vault.env are absorbed, deduped by key value, and
#     re-emitted alongside any newly minted keys. A partial/failed run can only
#     ADD keys, never lose them.
#   • Idempotent + resumable: existing cascade-gfp-* projects/keys are reused.
#
# Usage:
#   scripts/provision-gfp-keys.sh [PROJECTS_PER_ACCOUNT] [ACCOUNT_EMAIL...]
#   (default 4 projects/account, all human `gcloud auth list` accounts)
#
# Output: rewrites ~/.cascade/gfp-keys.env with the full deduped key set
#   (GEMINI_FREE_KEY_<n>=<key>) and prints a per-account summary to stderr.
#
# Requires: gcloud (authed accounts). jq optional.
set -uo pipefail

PPA="${1:-4}"; shift || true
ACCOUNTS=("$@")
if [ "${#ACCOUNTS[@]}" -eq 0 ]; then
  # All human accounts — skip GCP service accounts (they carry no free quota
  # and cannot create consumer projects).
  mapfile -t ACCOUNTS < <(gcloud auth list --format="value(account)" 2>/dev/null \
    | grep -v '\.iam\.gserviceaccount\.com$')
fi

OUT="${HOME}/.cascade/gfp-keys.env"
VAULT="${HOME}/.claude/vault.env"
mkdir -p "$(dirname "$OUT")"

# ── Seed the key set from every source we already have ──────────────────────
# Collect valid AIza… keys from the existing pool file and the vault, so a
# re-run never regresses the pool. Keys are deduped by value.
declare -A SEEN
seed_from() {
  local f="$1"; [ -f "$f" ] || return 0
  # Match GEMINI_FREE_KEY_*, GEMINI_KEY_*, GEMINI_API_KEY* assignments.
  while IFS= read -r val; do
    [ -n "$val" ] && SEEN["$val"]=1
  done < <(grep -oE '(GEMINI_FREE_KEY_[0-9]+|GEMINI_KEY_[A-Za-z0-9]+|GEMINI_API_KEY[A-Za-z0-9_]*)=("?)AIza[0-9A-Za-z_-]+' "$f" \
             | sed -E 's/.*=("?)//; s/"$//')
}
seed_from "$OUT"
seed_from "$VAULT"
echo "── seeded ${#SEEN[@]} existing key(s) from pool + vault ──" >&2

slug() { echo "$1" | tr '@.' '--' | tr -cd 'a-z0-9-' | cut -c1-16; }

minted_total=0
for acc in "${ACCOUNTS[@]}"; do
  [ -z "$acc" ] && continue
  echo "── account: $acc ──" >&2
  s="$(slug "$acc")"; acc_minted=0
  for i in $(seq 1 "$PPA"); do
    proj="cascade-gfp-${s}-${i}"
    if ! gcloud projects describe "$proj" --account="$acc" >/dev/null 2>&1; then
      if ! gcloud projects create "$proj" --account="$acc" --quiet >/dev/null 2>&1; then
        proj="${proj}-$(head -c2 /dev/urandom | od -An -tx1 | tr -d ' \n')"
        gcloud projects create "$proj" --account="$acc" --quiet >/dev/null 2>&1 \
          || { echo "  ✗ project create failed ($proj) — likely project quota; skipping" >&2; continue; }
      fi
      echo "  + created project $proj" >&2
    fi
    gcloud services enable generativelanguage.googleapis.com \
      --project="$proj" --account="$acc" --quiet >/dev/null 2>&1 \
      || { echo "  ✗ enable API failed ($proj)" >&2; continue; }
    key="$(gcloud services api-keys create \
      --project="$proj" --account="$acc" \
      --display-name="cascade-gfp" \
      --api-target=service=generativelanguage.googleapis.com \
      --format="value(response.keyString)" --quiet 2>/dev/null)"
    if [ -n "$key" ] && [ -z "${SEEN[$key]:-}" ]; then
      SEEN["$key"]=1; acc_minted=$((acc_minted+1)); minted_total=$((minted_total+1))
      echo "  ✓ minted new key for $proj" >&2
    elif [ -n "$key" ]; then
      echo "  = key for $proj already in pool (deduped)" >&2
    else
      echo "  ✗ key mint failed ($proj)" >&2
    fi
  done
  echo "  account total: +${acc_minted} new" >&2
done

# ── Rewrite the pool file from the full deduped set (never smaller) ─────────
: > "${OUT}.new"
n=0
for k in "${!SEEN[@]}"; do
  n=$((n+1)); echo "GEMINI_FREE_KEY_${n}=${k}" >> "${OUT}.new"
done
mv "${OUT}.new" "$OUT"
echo "── done: ${n} total keys (+${minted_total} new this run) → ${OUT} ──" >&2
echo "Reload the pool: cascade daemon restart  (or the daemon absorbs ${OUT} on its next refresh)" >&2
