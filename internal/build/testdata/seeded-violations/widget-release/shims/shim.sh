#!/usr/bin/env bash
# PATH shim for widget_release_test.go. Installed under the name of each tool
# it replaces (xcodegen, xcodebuild, PlistBuddy, codesign, ditto, hdiutil,
# xcrun). Every call is logged to $SHIM_LOG as "<tool> <args>". Entries of
# $SHIM_FAIL (space separated) exit 1: "<tool>" fails every call, and
# "<tool>:<first arg>" one subcommand (stapler:validate, codesign:--verify);
# notarytool and stapler count as tools.
# notarytool: $SHIM_NOTARY=hang makes submit write $SHIM_DIR/notary.pid and
# sleep until killed; $SHIM_STATUS sets the info status (default Accepted).
# notarytool echoes its arguments and the key file to stderr on purpose, so
# the scripts' redaction is what keeps the canaries out of their output.
set -eu
arg_after() { # arg_after FLAG ARGS...: print the argument following FLAG
	local f="$1"
	shift
	while [ $# -gt 1 ]; do
		if [ "$1" = "$f" ]; then
			printf '%s' "$2"
			return
		fi
		shift
	done
}

n="$(basename "$0")"
if [ "$n" = xcrun ]; then
	n="$1"
	shift
fi
printf '%s %s\n' "$n" "$*" >>"$SHIM_LOG"
if [ "$n" = notarytool ]; then
	echo "notarytool args: $*" >&2
	auth="key-id $(arg_after --key-id "$@") issuer $(arg_after --issuer "$@")"
	echo "notarytool auth: $auth; AUTH: $(printf '%s' "$auth" | tr '[:lower:]' '[:upper:]')" >&2
	cat "$(arg_after --key "$@")" >&2
	echo "notarytool fragment: \"$(sed -n 2p "$(arg_after --key "$@")" | tr -d '\r' | cut -c1-50)...\"" >&2
fi
case " ${SHIM_FAIL:-} " in
*" $n "* | *" $n:${1:-} "*)
	echo "$n: shim failure" >&2
	exit 1
	;;
esac

fake_app() {
	mkdir -p "$1/Contents/MacOS" "$1/Contents/PlugIns/CascadeWidgetExtension.appex/Contents"
	printf 'fake app\n' >"$1/Contents/MacOS/CascadeMenuBar"
}

case "$n" in
xcodegen) mkdir -p "$(arg_after --project "$@")/CascadeWidget.xcodeproj" ;;
xcodebuild)
	if [ "$1" = archive ]; then
		fake_app "$(arg_after -archivePath "$@")/Products/Applications/CascadeMenuBar.app"
	else
		fake_app "$(arg_after -exportPath "$@")/CascadeMenuBar.app"
	fi
	;;
ditto)
	if [ "$1" = -c ]; then
		printf 'zip of %s\n' "$4" >"$5"
	else
		cp -R "$1" "$2"
	fi
	;;
hdiutil) for a in "$@"; do last="$a"; done && printf 'dmg\n' >"$last" ;;
notarytool)
	case "$1" in
	submit)
		if [ "${SHIM_NOTARY:-}" = hang ]; then
			echo $$ >"$SHIM_DIR/notary.pid"
			exec sleep 300
		fi
		printf '{"id":"0f0e0d0c-0b0a-4908-8706-050403020100","message":"args %s"}\n' "$*"
		;;
	info) printf '{"status":"%s","id":"%s","message":"args %s"}\n' "${SHIM_STATUS:-Accepted}" "$2" "$*" ;;
	log) printf '{"issues":[]}\n' >"$3" ;;
	esac
	;;
esac
