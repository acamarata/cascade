#!/bin/sh
# Capture the codex App Server schema and one session for the driver's
# test data. Run it by hand on a host with codex installed and signed in.
# It uses a throwaway CODEX_HOME whose auth.json links to the profile's
# auth.json in place, so it never copies a credential.
#   usage: capture.sh <profile-auth-dir> <out-dir>
set -eu
auth_dir=${1:?profile auth dir}
out=${2:?out dir}
command -v codex >/dev/null || { echo "codex not installed" >&2; exit 3; }
echo "codex: $(codex --version)"
echo "date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
mkdir -p "$out/schema"
# Flag name per `codex app-server generate-json-schema --help` on the captured version.
codex app-server generate-json-schema --out "$out/schema"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/codex-home" "$work/worktree"
ln -s "$auth_dir/auth.json" "$work/codex-home/auth.json"
# The capture client (cascade's driver in record mode) writes one line per
# frame in the session.jsonl format described in README.md.
echo "schema written to $out/schema; record the session with CODEX_HOME=$work/codex-home"
