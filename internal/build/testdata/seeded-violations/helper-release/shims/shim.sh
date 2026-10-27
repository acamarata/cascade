#!/usr/bin/env bash
# PATH shim for helper_release_test.go. Installed under the name of each tool
# it replaces (go, codesign, syft, ditto, xcrun and, for the canary run only,
# minisign). Every call is logged to $SHIM_LOG as "<tool> <args>". Entries of
# $SHIM_FAIL (space separated) exit 1: "<tool>" fails every call and
# "<tool>:<first arg>" one subcommand (codesign:--verify).
# Every call also logs "fd3-open <tool>" when fd 3 is open in the shim.
# go: build writes a fake helper named for its GOARCH (marked "trimpath" when
# it got -trimpath, unless SHIM_NOTRIM=1) and logs the GOFLAGS and GIT_DIR it
# saw; env answers GOENV ($SHIM_GOENV), GOPROXY, GOSUMDB and GOWORK
# (SHIM_GOPROXY, SHIM_GOSUMDB, SHIM_GOWORK); version -m describes a fake helper
# (overrides SHIM_NOCGO=1, SHIM_GOOS, SHIM_GOARCH, SHIM_PATH, SHIM_TAGS=1,
# SHIM_HELPER_REV, SHIM_DIRTY=1) or a fake cascade (SHIM_REV, SHIM_VCS_TIME,
# SHIM_CASCADE_DIRTY=1); run (nodevkeys) fails when SHIM_DEVKEYS=1.
# go build also logs "go-build-var NAME" for every variable it received.
# git: logs the GIT_DIR, GIT_WORK_TREE and GIT_NO_REPLACE_OBJECTS it saw;
# rev-parse --show-toplevel prints $SHIM_GIT_TOPLEVEL (default the cwd),
# rev-parse HEAD $SHIM_GIT_HEAD (default the fake commit), status
# $SHIM_GIT_STATUS (exit 128 when SHIM_GIT_STATUS_FAIL=1), ls-files -v $SHIM_GIT_LSV (default one H entry), ls-files
# -o -i $SHIM_GIT_IGNORED.
# codesign: signing appends one fixed line naming the identifier and identity;
# --verify fails for a path containing $SHIM_VERIFY_FAIL_UNDER.
# syft: version prints $SHIM_SYFT_VERSION (default 1.52.0); scan writes a
# fixed SBOM and, when SHIM_MKDIR is set, creates that directory (a racing
# writer). notarytool: submit answers a fixed id, info Accepted.
# notarytool and minisign echo their arguments, the key file and stdin to
# stderr on purpose: the scripts must keep those out of their own output.
# A tool run in an empty environment (go under env -i) reads the SHIM_*
# settings from shim.env beside the shim, which the test writes per run.
set -eu
if [ -z "${SHIM_LOG:-}" ] && [ -f "$(dirname "$0")/shim.env" ]; then
	# shellcheck disable=SC1091
	. "$(dirname "$0")/shim.env"
fi
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
last() { for a in "$@"; do :; done && printf '%s' "$a"; }

n="$(basename "$0")"
if [ "$n" = xcrun ]; then
	n="$1"
	shift
fi
printf '%s %s\n' "$n" "$*" >>"$SHIM_LOG"
if [ -e /dev/fd/3 ]; then printf 'fd3-open %s\n' "$n" >>"$SHIM_LOG"; fi
case " ${SHIM_FAIL:-} " in
*" $n "* | *" $n:${1:-} "*)
	echo "$n: shim failure" >&2
	exit 1
	;;
esac

case "$n" in
go)
	case "$1" in
	build)
		printf 'go-build-env GOFLAGS=[%s] GIT_DIR=[%s]\n' "${GOFLAGS:-}" "${GIT_DIR:-}" >>"$SHIM_LOG"
		env -u SHIM_LOG | sed -n 's/^\([A-Za-z_][A-Za-z0-9_]*\)=.*/go-build-var \1/p' >>"$SHIM_LOG"
		trim=""
		case " $* " in *" -trimpath "*) [ "${SHIM_NOTRIM:-}" = 1 ] || trim=" trimpath" ;; esac
		printf 'fake helper %s%s\n' "$GOARCH" "$trim" >"$(arg_after -o "$@")"
		;;
	env)
		case "$2" in
		GOENV) printf '%s\n' "${SHIM_GOENV:-}" ;;
		GOPROXY) printf '%s\n' "${SHIM_GOPROXY:-https://proxy.golang.org,direct}" ;;
		GOSUMDB) printf '%s\n' "${SHIM_GOSUMDB:-sum.golang.org}" ;;
		GOWORK) printf '%s\n' "${SHIM_GOWORK:-}" ;;
		esac
		;;
	version)
		f="$(last "$@")"
		rev=1111111111111111111111111111111111111111
		if [ "$(head -1 "$f" | cut -d ' ' -f 2)" = cascade ]; then
			printf '%s: go1.26\n\tpath\tgithub.com/acamarata/cascade/cmd/cascade\n\tbuild\tvcs.revision=%s\n' "$f" "${SHIM_REV:-$rev}"
			printf '\tbuild\tvcs.time=%s\n' "${SHIM_VCS_TIME:-2002-03-04T05:06:07Z}"
			[ "${SHIM_CASCADE_DIRTY:-}" = 1 ] && printf '\tbuild\tvcs.modified=true\n' || printf '\tbuild\tvcs.modified=false\n'
			exit 0
		fi
		printf '%s: go1.26\n\tpath\t%s\n' "$f" "${SHIM_PATH:-github.com/acamarata/cascade/cmd/cascade-elevate-helper}"
		[ "${SHIM_NOCGO:-}" = 1 ] || printf '\tbuild\tCGO_ENABLED=1\n'
		[ "${SHIM_TAGS:-}" != 1 ] || printf '\tbuild\t-tags=devkeys\n'
		case "$(head -1 "$f")" in *" trimpath") printf '\tbuild\t-trimpath=true\n' ;; esac
		printf '\tbuild\tGOARCH=%s\n' "${SHIM_GOARCH:-$(head -1 "$f" | cut -d ' ' -f 3)}"
		printf '\tbuild\tGOOS=%s\n\tbuild\tvcs.revision=%s\n' "${SHIM_GOOS:-darwin}" "${SHIM_HELPER_REV:-$rev}"
		[ "${SHIM_DIRTY:-}" = 1 ] && printf '\tbuild\tvcs.modified=true\n' || printf '\tbuild\tvcs.modified=false\n'
		;;
	run) [ "${SHIM_DEVKEYS:-}" != 1 ] || { echo "nodevkeys: dev custody found" >&2; exit 1; } ;;
	esac
	;;
codesign)
	case "$(last "$@")" in *"${SHIM_VERIFY_FAIL_UNDER:-//none//}"*) if [ "$1" = --verify ]; then echo "codesign: shim verify failure" >&2 && exit 1; fi ;; esac
	if [ "$1" != --verify ]; then
		printf 'signature %s by %s\n' "$(arg_after --identifier "$@")" "$(arg_after --sign "$@")" >>"$(last "$@")"
	fi
	;;
syft)
	if [ "$1" = version ]; then
		printf 'Application:   syft\nVersion:       %s\n' "${SHIM_SYFT_VERSION:-1.52.0}"
	else
		if [ -n "${SHIM_MKDIR:-}" ]; then mkdir -p "$SHIM_MKDIR" && printf 'racing writer\n' >"$SHIM_MKDIR/sentinel"; fi
		o="$(arg_after --output "$@")"
		printf '{"sbom":"%s"}\n' "$2" >"${o#spdx-json=}"
	fi
	;;
git)
	printf 'git-env GIT_DIR=[%s] GIT_WORK_TREE=[%s] NOREPLACE=[%s]\n' "${GIT_DIR:-}" "${GIT_WORK_TREE:-}" "${GIT_NO_REPLACE_OBJECTS:-}" >>"$SHIM_LOG"
	case "$*" in
	"rev-parse --show-toplevel") printf '%s\n' "${SHIM_GIT_TOPLEVEL:-$(pwd -P)}" ;;
	"rev-parse HEAD") printf '%s\n' "${SHIM_GIT_HEAD:-1111111111111111111111111111111111111111}" ;;
	status*)
		[ "${SHIM_GIT_STATUS_FAIL:-}" != 1 ] || exit 128
		printf '%s' "${SHIM_GIT_STATUS:-}"
		;;
	"ls-files -v") printf '%s\n' "${SHIM_GIT_LSV:-H cmd/cascade-elevate-helper/main.go}" ;;
	"ls-files -o -i"*) printf '%s' "${SHIM_GIT_IGNORED:-}" ;;
	*) echo "git shim: unexpected $*" >&2 && exit 1 ;;
	esac
	;;
ditto) printf 'zip of %s\n' "$4" >"$5" ;;
notarytool)
	echo "notarytool args: $*" >&2
	cat "$(arg_after --key "$@")" >&2
	case "$1" in
	submit) printf '{"id":"0f0e0d0c-0b0a-4908-8706-050403020100","message":"args %s"}\n' "$*" ;;
	info) printf '{"status":"%s","id":"%s"}\n' "${SHIM_STATUS:-Accepted}" "$2" ;;
	log) printf '{"issues":[]}\n' >"$3" ;;
	esac
	;;
minisign)
	echo "minisign args: $*; stdin: $(cat)" | tee /dev/stderr
	cat "$(arg_after -s "$@")" >&2
	printf 'untrusted comment: shim\nRWQ=\ntrusted comment: %s\nRWQ=\n' "$(arg_after -t "$@")" >"$(arg_after -m "$@").minisig"
	;;
esac
