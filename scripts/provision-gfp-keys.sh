#!/usr/bin/env bash
# provision-gfp-keys.sh — maximise the free Gemini (GFP) pool by minting one
# API key per GCP project across every authed gcloud account.
#
# Google's generativelanguage free tier is billed PER-PROJECT (not per-key), so
# N projects ≈ N× the free daily quota. This script, for each authed gcloud
# account, ensures up to $PROJECTS_PER_ACCOUNT projects exist, enables the
# generativelanguage API on each, and mints one API key restricted to that API.
# Idempotent + resumable: existing cascade-gfp-* projects/keys are reused.
#
# Usage:
#   scripts/provision-gfp-keys.sh [PROJECTS_PER_ACCOUNT] [ACCOUNT_EMAIL...]
#   (default 4 projects/account, all `gcloud auth list` accounts)
#
# Output: appends `GEMINI_FREE_KEY_<n>=<key>` lines to stdout AND writes them to
#   ~/.cascade/gfp-keys.env (which the daemon/CLI load into the pool).
#
# Requires: gcloud (authed accounts), jq.
set -uo pipefail

PPA="${1:-4}"; shift || true
ACCOUNTS=("$@")
if [ "${#ACCOUNTS[@]}" -eq 0 ]; then
  mapfile -t ACCOUNTS < <(gcloud auth list --format="value(account)" 2>/dev/null)
fi

OUT="${HOME}/.cascade/gfp-keys.env"
mkdir -p "$(dirname "$OUT")"
: > "${OUT}.new"
n=0

slug() { echo "$1" | tr '@.' '--' | tr -cd 'a-z0-9-' | cut -c1-16; }

for acc in "${ACCOUNTS[@]}"; do
  [ -z "$acc" ] && continue
  echo "── account: $acc ──" >&2
  s="$(slug "$acc")"
  for i in $(seq 1 "$PPA"); do
    proj="cascade-gfp-${s}-${i}"
    # Create the project if absent (project IDs are globally unique; append a
    # random suffix on collision).
    if ! gcloud projects describe "$proj" --account="$acc" >/dev/null 2>&1; then
      if ! gcloud projects create "$proj" --account="$acc" --quiet >/dev/null 2>&1; then
        proj="${proj}-$(head -c2 /dev/urandom | od -An -tx1 | tr -d ' \n')"
        gcloud projects create "$proj" --account="$acc" --quiet >/dev/null 2>&1 \
          || { echo "  ✗ project create failed ($proj) — likely project quota; skipping" >&2; continue; }
      fi
      echo "  + created project $proj" >&2
    fi
    # Enable the generativelanguage API (idempotent).
    gcloud services enable generativelanguage.googleapis.com \
      --project="$proj" --account="$acc" --quiet >/dev/null 2>&1 \
      || { echo "  ✗ enable API failed ($proj)" >&2; continue; }
    # Mint an API key restricted to generativelanguage.
    key="$(gcloud services api-keys create \
      --project="$proj" --account="$acc" \
      --display-name="cascade-gfp" \
      --api-target=service=generativelanguage.googleapis.com \
      --format="value(response.keyString)" --quiet 2>/dev/null)"
    if [ -n "$key" ]; then
      n=$((n+1)); echo "GEMINI_FREE_KEY_${n}=${key}" >> "${OUT}.new"
      echo "  ✓ key minted for $proj (GEMINI_FREE_KEY_${n})" >&2
    else
      echo "  ✗ key mint failed ($proj)" >&2
    fi
  done
done

mv "${OUT}.new" "$OUT"
echo "── done: ${n} keys written to ${OUT} ──" >&2
echo "Reload the pool: cascade daemon restart  (or the CLI/daemon reads ${OUT} on next poll)" >&2
