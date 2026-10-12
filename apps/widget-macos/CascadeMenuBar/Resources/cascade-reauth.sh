#!/bin/sh
# Purpose: run `cascade provider reauth <ref>` for the menu-bar widget, after the widget's
#   confirmation view, from a per-launch command file (ReauthCommandFile.swift).
# Inputs: $1 = absolute path of the installer-recorded cascade CLI, $2 = the provider ref.
# Outputs: the exit status of the CLI; every refusal exits non-zero before the CLI runs.
# Constraints: argv only, no eval, no PATH lookup of cascade. The ref guard repeats the one
#   in ReauthCommandFile.swift: 1-64 characters of A-Z a-z 0-9 . _ -, never a leading dash.

LC_ALL=C
export LC_ALL

fail() {
	printf 'cascade-reauth: %s\n' "$1" >&2
	exit 2
}

[ "$#" -eq 2 ] || fail 'expected exactly two arguments: cli path and provider ref'

cli=$1
ref=$2

case "$cli" in
/*) ;;
*) fail 'the cascade CLI path must be absolute' ;;
esac
case "$cli" in
*[[:cntrl:]]*) fail 'the cascade CLI path has a control character' ;;
esac
[ -f "$cli" ] && [ -x "$cli" ] || fail 'the cascade CLI is not an executable file'

case "$ref" in
'') fail 'the provider ref is empty' ;;
-*) fail 'the provider ref must not start with a dash' ;;
*[!A-Za-z0-9._-]*) fail 'the provider ref has a character outside A-Z a-z 0-9 . _ -' ;;
esac
[ "${#ref}" -le 64 ] || fail 'the provider ref is longer than 64 characters'

exec "$cli" provider reauth "$ref"
