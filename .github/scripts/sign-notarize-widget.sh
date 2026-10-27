#!/usr/bin/env bash
# sign-notarize-widget.sh submit|finish: sign, notarize and package the
# CascadeMenuBar.app that build-widget-release.sh left in $CASCADE_WIDGET_OUT.
#
#   submit  journal "begin", codesign (hardened runtime, secure timestamp),
#           zip, journal an intent line, `notarytool submit` without --wait,
#           then journal the submission id. Prints the id.
#   finish  one `notarytool info` poll for the id confirmed after the last
#           begin, intent or skip line (a rebuild journals "begin build", a
#           skip-mode submit "skip"). Accepted: staple, validate, write
#           CascadeMenuBar.zip and CascadeMenuBar.dmg and add their sha256
#           lines to checksums.txt. In Progress: exit 75 (call finish again).
#           Invalid or Rejected: fetch notary-log.json, exit 1, add nothing.
#
# Env: CASCADE_WIDGET_OUT (required); CASCADE_APPLE_SIGNING_IDENTITY (a
# Developer ID identity); CASCADE_APPLE_NOTARY_KEY_ID, _ISSUER and _PATH (App
# Store Connect API key). CASCADE_NOTARY_SKIP=1 skips signing and notarization,
# packages the .app as built and writes notarization.txt "skipped" (never a
# release). Credentials are passed only to notarytool; notarytool output is
# printed only through redact(); nothing is written to any keychain.
# Journal: $CASCADE_WIDGET_OUT/notary-journal.log.
set -eu
set -o pipefail
umask 077

die() {
	printf 'sign-notarize-widget: %s\n' "$*" >&2
	exit 1
}

MODE="${1:-}"
case "$MODE" in
submit | finish) ;;
*) die "usage: sign-notarize-widget.sh submit|finish" ;;
esac
OUT="${CASCADE_WIDGET_OUT:-}"
[ -n "$OUT" ] || die "CASCADE_WIDGET_OUT is required"
[ -d "$OUT" ] || die "CASCADE_WIDGET_OUT ($OUT) does not exist"
APP="$OUT/CascadeMenuBar.app"
EXT="$APP/Contents/PlugIns/CascadeWidgetExtension.appex"
JOURNAL="$OUT/notary-journal.log"
MARK="$OUT/notarization.txt"
SUMS="$OUT/checksums.txt"
IDENTITY="${CASCADE_APPLE_SIGNING_IDENTITY:-}"
SKIP=0
if [ "${CASCADE_NOTARY_SKIP:-}" = 1 ]; then SKIP=1; fi
[ -d "$APP" ] || die "missing $APP (run build-widget-release.sh first)"

# The journal, credential check, notarytool call, redaction and journal
# parsing are the shared notary core in notary-lib.sh.
# shellcheck source=.github/scripts/notary-lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/notary-lib.sh"

# clear_outputs removes the marker, the packages and their checksum lines so
# nothing from an earlier run survives a new one.
clear_outputs() {
	rm -rf "$MARK" "$OUT/CascadeMenuBar.zip" "$OUT/CascadeMenuBar.dmg" "$OUT/.pkg" "$OUT/notary-log.json"
	if [ -f "$SUMS" ]; then
		awk '!/  CascadeMenuBar\.(zip|dmg)$/' "$SUMS" >"$SUMS.tmp"
		mv "$SUMS.tmp" "$SUMS"
	fi
}

# package builds both packages in $OUT/.pkg and moves them into place only
# after the .dmg is signed, then adds their checksum lines.
package() {
	local pkg="$OUT/.pkg"
	clear_outputs
	mkdir -p "$pkg/stage"
	ditto "$APP" "$pkg/stage/CascadeMenuBar.app"
	ln -s /Applications "$pkg/stage/Applications"
	ditto -c -k --keepParent "$APP" "$pkg/CascadeMenuBar.zip"
	hdiutil create -quiet -volname CascadeMenuBar -srcfolder "$pkg/stage" -format UDZO -ov "$pkg/CascadeMenuBar.dmg"
	if [ "$SKIP" = 0 ]; then
		codesign --force --timestamp --sign "$IDENTITY" "$pkg/CascadeMenuBar.dmg"
	fi
	mv "$pkg/CascadeMenuBar.zip" "$pkg/CascadeMenuBar.dmg" "$OUT/"
	rm -rf "$pkg"
	(cd "$OUT" && shasum -a 256 CascadeMenuBar.zip CascadeMenuBar.dmg) >>"$SUMS"
}

submit_real() {
	local zip="$OUT/.notary/submit.zip" out id sha
	notary_require_real
	journal "begin"
	clear_outputs
	if [ -d "$EXT" ]; then
		codesign --force --options runtime --timestamp --preserve-metadata=entitlements --sign "$IDENTITY" "$EXT"
	fi
	codesign --force --options runtime --timestamp --preserve-metadata=entitlements --sign "$IDENTITY" "$APP"
	codesign --verify --deep --strict "$APP"
	mkdir -p "$OUT/.notary"
	rm -f "$zip"
	ditto -c -k --keepParent "$APP" "$zip"
	sha="$(shasum -a 256 "$zip" | cut -d ' ' -f 1)"
	journal "intent zip-sha256=$sha"
	if ! out="$(notary submit "$zip" --output-format json 2>&1)"; then
		redact "$out"
		journal "submit-failed"
		die "notarytool submit failed"
	fi
	id="$(json_field id "$out")"
	if ! valid_id "$id"; then
		redact "$out"
		journal "submit-failed no-id"
		die "notarytool submit returned no submission id"
	fi
	journal "submitted id=$id"
	printf 'submitted %s\n' "$id"
}

finish_real() {
	local id out status
	notary_require_real
	id="$(confirmed_id)"
	valid_id "$id" || die "no submission confirmed after the last submit; run submit again"
	if ! out="$(notary info "$id" --output-format json 2>&1)"; then
		redact "$out"
		journal "poll-failed id=$id"
		die "notarytool info failed for $id"
	fi
	status="$(json_field status "$out")"
	case "$status" in
	Accepted | "In Progress" | Invalid | Rejected) journal "status id=$id $status" ;;
	*)
		journal "status id=$id unexpected"
		redact "$out"
		die "notarization $id: unexpected status"
		;;
	esac
	case "$status" in
	"In Progress")
		printf 'notarization %s: In Progress (run finish again)\n' "$id"
		exit 75
		;;
	Invalid | Rejected)
		if ! out="$(notary log "$id" "$OUT/notary-log.json" 2>&1)"; then
			redact "$out"
			die "notarization $id: $status; fetching its log failed"
		fi
		die "notarization $id: $status; see $OUT/notary-log.json"
		;;
	esac
	xcrun stapler staple "$APP"
	xcrun stapler validate "$APP"
	codesign --verify --deep --strict "$APP"
	package
	printf 'accepted id=%s\n' "$id" >"$MARK"
	journal "finished id=$id"
	printf 'notarization %s: Accepted; packaged into %s\n' "$id" "$OUT"
}

if [ "$SKIP" = 1 ]; then
	if [ "$MODE" = submit ]; then
		clear_outputs
		printf 'skipped\n' >"$MARK"
		journal "skip"
		printf 'notarization skipped (CASCADE_NOTARY_SKIP=1)\n'
	else
		[ "$(cat "$MARK" 2>/dev/null)" = skipped ] || die "finish in skip mode needs a skip-mode submit first"
		package
		printf 'skipped\n' >"$MARK"
		printf 'packaged without notarization into %s\n' "$OUT"
	fi
elif [ "$MODE" = submit ]; then
	submit_real
else
	finish_real
fi
