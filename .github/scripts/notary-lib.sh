# shellcheck shell=bash
# notary-lib.sh: the shared notary core of the release scripts. Source it from
# a script that has set -eu, defined die() and set JOURNAL (the journal file)
# and IDENTITY (the codesign identity). It runs nothing on its own.
#
#   journal TEXT       append a timestamped line to $JOURNAL
#   notary_require_real
#                      refuse unless IDENTITY is a real identity and the App
#                      Store Connect key env is set and names a readable file
#   notary ARGS...     xcrun notarytool ARGS plus the key arguments
#   redact TEXT        print TEXT to stderr with credentials withheld
#   json_field NAME TEXT, valid_id ID, confirmed_id, notary_last_state
#
# Env read: CASCADE_APPLE_NOTARY_KEY_ID, _ISSUER and _PATH. Credentials are
# passed only to notarytool; notarytool output is printed only through
# redact(); nothing here writes to any keychain.

journal() {
	printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >>"$JOURNAL"
}

# notary_require_real refuses before any signing when an identity or key is
# missing.
notary_require_real() {
	if [ -z "$IDENTITY" ] || [ "$IDENTITY" = "-" ]; then
		die "real mode needs a Developer ID identity in CASCADE_APPLE_SIGNING_IDENTITY (or CASCADE_NOTARY_SKIP=1)"
	fi
	[ -n "${CASCADE_APPLE_NOTARY_KEY_ID:-}" ] || die "CASCADE_APPLE_NOTARY_KEY_ID is required in real mode"
	[ -n "${CASCADE_APPLE_NOTARY_KEY_ISSUER:-}" ] || die "CASCADE_APPLE_NOTARY_KEY_ISSUER is required in real mode"
	[ -n "${CASCADE_APPLE_NOTARY_KEY_PATH:-}" ] || die "CASCADE_APPLE_NOTARY_KEY_PATH is required in real mode"
	if ! [ -f "$CASCADE_APPLE_NOTARY_KEY_PATH" ] || ! [ -r "$CASCADE_APPLE_NOTARY_KEY_PATH" ] || ! [ -s "$CASCADE_APPLE_NOTARY_KEY_PATH" ]; then
		die "CASCADE_APPLE_NOTARY_KEY_PATH does not name a readable, non-empty file"
	fi
}

notary() {
	xcrun notarytool "$@" --key "$CASCADE_APPLE_NOTARY_KEY_PATH" \
		--key-id "$CASCADE_APPLE_NOTARY_KEY_ID" --issuer "$CASCADE_APPLE_NOTARY_KEY_ISSUER"
}

# redact prints TEXT to stderr, replacing with "[line withheld]" any line that
# contains a line of the key file, a base64 run of 40 or more characters or
# "PRIVATE KEY", and with the key id and issuer replaced in any letter case
# (longer value first). If the key file cannot be read it withholds
# everything. awk reads the key path, id and issuer from its environment,
# never from argv.
redact() {
	printf '%s\n' "$1" | awk '
		function scrub(s, w, i, o) {
			if (w == "") return s
			o = ""
			while ((i = index(tolower(s), w)) > 0) { o = o substr(s, 1, i - 1) "[redacted]"; s = substr(s, i + length(w)) }
			return o s
		}
		function b64run(s) {
			while (match(s, /[A-Za-z0-9+\/=]+/)) { if (RLENGTH >= 40) return 1; s = substr(s, RSTART + RLENGTH) }
			return 0
		}
		BEGIN {
			id = tolower(ENVIRON["CASCADE_APPLE_NOTARY_KEY_ID"]); iss = tolower(ENVIRON["CASCADE_APPLE_NOTARY_KEY_ISSUER"])
			f = ENVIRON["CASCADE_APPLE_NOTARY_KEY_PATH"]
			while ((r = (getline l < f)) > 0) { sub(/\r$/, "", l); if (length(l) >= 4) key[l] = 1 }
			if (r < 0) nokey = 1
			close(f)
		}
		{
			sub(/\r$/, "")
			if (nokey) { if (!told++) print "[output withheld: key file unreadable]"; next }
			for (k in key) if (index($0, k)) { print "[line withheld]"; next }
			if (/PRIVATE KEY/ || b64run($0)) { print "[line withheld]"; next }
			print (length(id) > length(iss) ? scrub(scrub($0, id), iss) : scrub(scrub($0, iss), id))
		}' >&2
}

# json_field NAME TEXT prints the string value of "NAME" in TEXT (the last one
# on a line; notarytool prints flat JSON).
json_field() {
	printf '%s\n' "$2" | sed -n -E 's/.*"'"$1"'"[[:space:]]*:[[:space:]]*"([^"]*)".*/\1/p' | sed -n 1p
}

valid_id() {
	[[ "$1" =~ ^[0-9A-Fa-f]{8}(-[0-9A-Fa-f]{4}){3}-[0-9A-Fa-f]{12}$ ]]
}

# confirmed_id prints the submission id journaled after the last begin, intent
# or skip line, or nothing.
confirmed_id() {
	[ -f "$JOURNAL" ] || return 0
	awk '/^[^ ]+ (begin|intent|skip)( |$)/{id=""} /^[^ ]+ submitted id=/{id=$0; sub(/^[^ ]+ submitted id=/, "", id)} END{print id}' "$JOURNAL"
}

# notary_last_state prints the word of the last begin, intent or skip line
# ("begin", "intent" or "skip"), or nothing.
notary_last_state() {
	[ -f "$JOURNAL" ] || return 0
	awk '/^[^ ]+ (begin|intent|skip)( |$)/{s=$2} END{print s}' "$JOURNAL"
}
