#!/usr/bin/env bash
# build-widget-release.sh: archive the macOS widget (apps/widget-macos) and
# leave a signed CascadeMenuBar.app in $CASCADE_WIDGET_OUT. Nothing else is
# written outside that directory: the XcodeGen project, derived data and the
# archive live under $CASCADE_WIDGET_OUT/.widget-build. Packages, the
# notarization marker and their checksum lines from an earlier run are removed,
# and a "begin" journal line voids any submission id confirmed before it.
#
# Env:
#   CASCADE_WIDGET_OUT              output directory (required)
#   CASCADE_APPLE_SIGNING_IDENTITY  codesign identity (required); "-" = ad-hoc
#   CASCADE_APPLE_TEAM_ID           team id (required unless the identity is "-")
#
# Ad-hoc copies the archived .app and never exports. Any other identity
# renders apps/widget-macos/ExportOptions.plist into
# $CASCADE_WIDGET_OUT/ExportOptions.rendered.plist (adding signingCertificate
# and teamID) and exports with it. This script reads no notary credential and
# writes nothing to any keychain. Next step: sign-notarize-widget.sh.
set -eu
set -o pipefail
umask 077

die() {
	printf 'build-widget-release: %s\n' "$*" >&2
	exit 1
}

OUT="${CASCADE_WIDGET_OUT:-}"
IDENTITY="${CASCADE_APPLE_SIGNING_IDENTITY:-}"
TEAM="${CASCADE_APPLE_TEAM_ID:-}"
[ -n "$OUT" ] || die "CASCADE_WIDGET_OUT is required"
[ -n "$IDENTITY" ] || die 'CASCADE_APPLE_SIGNING_IDENTITY is required ("-" for ad-hoc)'
if [ "$IDENTITY" != "-" ] && ! [[ "$TEAM" =~ ^[A-Z0-9]{10}$ ]]; then
	die 'CASCADE_APPLE_TEAM_ID (ten letters or digits) is required unless the identity is "-"'
fi
case "$IDENTITY$TEAM" in
*\"* | *\'* | *\\*) die "identity and team id must not contain quotes or backslashes" ;;
esac

ROOT="$(cd "$(dirname "$0")/../.." && pwd -P)"
SRC="$ROOT/apps/widget-macos"
[ -f "$SRC/project.yml" ] || die "missing $SRC/project.yml"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd -P)"
case "$OUT/" in
"$ROOT/"*)
	git -C "$ROOT" check-ignore -q "$OUT" ||
		die "CASCADE_WIDGET_OUT inside the working tree must be a gitignored path"
	;;
esac

PLISTBUDDY="$(command -v PlistBuddy || printf '%s' /usr/libexec/PlistBuddy)"
WORK="$OUT/.widget-build"
ARCHIVE="$WORK/CascadeMenuBar.xcarchive"
APP="$OUT/CascadeMenuBar.app"

rm -rf "$WORK" "$APP" "$OUT/ExportOptions.rendered.plist" "$OUT/notarization.txt" \
	"$OUT/CascadeMenuBar.zip" "$OUT/CascadeMenuBar.dmg" "$OUT/.notary" "$OUT/.pkg" "$OUT/notary-log.json"
printf '%s begin build\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >>"$OUT/notary-journal.log"
if [ -f "$OUT/checksums.txt" ]; then
	awk '!/  CascadeMenuBar\.(zip|dmg)$/' "$OUT/checksums.txt" >"$OUT/checksums.txt.tmp"
	mv "$OUT/checksums.txt.tmp" "$OUT/checksums.txt"
fi
mkdir -p "$WORK"
cp -R "$SRC" "$WORK/src"
rm -rf "$WORK/src/CascadeWidget.xcodeproj" "$WORK/src/build"
xcodegen generate --quiet --spec "$WORK/src/project.yml" --project "$WORK/src"

if [ "$IDENTITY" = "-" ]; then
	xcodebuild archive -quiet -project "$WORK/src/CascadeWidget.xcodeproj" \
		-scheme CascadeMenuBar -configuration Release \
		-archivePath "$ARCHIVE" -derivedDataPath "$WORK/dd" \
		CODE_SIGN_STYLE=Manual CODE_SIGN_IDENTITY=- DEVELOPMENT_TEAM=
	ditto "$ARCHIVE/Products/Applications/CascadeMenuBar.app" "$APP"
else
	xcodebuild archive -quiet -project "$WORK/src/CascadeWidget.xcodeproj" \
		-scheme CascadeMenuBar -configuration Release \
		-archivePath "$ARCHIVE" -derivedDataPath "$WORK/dd" \
		CODE_SIGN_STYLE=Manual CODE_SIGN_IDENTITY="$IDENTITY" \
		DEVELOPMENT_TEAM="$TEAM" OTHER_CODE_SIGN_FLAGS=--timestamp
	RENDERED="$OUT/ExportOptions.rendered.plist"
	cp "$SRC/ExportOptions.plist" "$RENDERED"
	"$PLISTBUDDY" -c "Add :signingCertificate string \"$IDENTITY\"" "$RENDERED"
	"$PLISTBUDDY" -c "Add :teamID string \"$TEAM\"" "$RENDERED"
	xcodebuild -exportArchive -quiet -archivePath "$ARCHIVE" \
		-exportPath "$WORK/export" -exportOptionsPlist "$RENDERED"
	ditto "$WORK/export/CascadeMenuBar.app" "$APP"
fi

rm -rf "$WORK/dd" "$WORK/src"
printf 'build-widget-release: %s\n' "$APP"
