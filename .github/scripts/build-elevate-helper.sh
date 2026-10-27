#!/usr/bin/env bash
# build-elevate-helper.sh: build the cgo cascade-elevate-helper
# (./cmd/cascade-elevate-helper) for darwin arm64 and amd64 into
# $CASCADE_HELPER_OUT/darwin_<arch>/cascade-elevate-helper. Runs on the build
# Mac only: CI never builds, receives or packs the helper.
#
# Env: CASCADE_HELPER_OUT (required; made absolute, must not exist yet and
# must lie outside the source tree, created 0700) and CASCADE_HELPER_REVISION
# (the 40-hex commit the release cascade was built from; defaults to HEAD).
# The script's own tree must be a clean git checkout (untracked files
# included) at that commit, at its top level with a real .git, and each
# helper must show that vcs.revision and vcs.modified=false. GIT_DIR and the
# other git location overrides are unset, replace objects are ignored, and go
# runs in an empty environment with an allow-list (see gobuild).
# Each helper is built with CGO_ENABLED=1, CC="clang -arch <arch>" (x86_64
# for amd64) and
# -trimpath, with GOFLAGS, GOEXPERIMENT and the CGO_*FLAGS of the caller
# removed, and the script refuses unless `go version -m` shows CGO_ENABLED=1,
# GOOS=darwin, the arch, -trimpath=true, no -tags and the revision, and the
# devkeys guard
# (`go run ./internal/build/nodevkeys`) passes on it. On any refusal the
# output directory is removed, so no unchecked helper is left behind.
set -eu
set -o pipefail
umask 077

die() {
	printf 'build-elevate-helper: %s\n' "$*" >&2
	exit 1
}

# check_checkout refuses unless the script's own tree ($ROOT, the cwd) is the
# top level of a git checkout with a real .git (a directory, or a gitfile
# whose worktree entry points back here) and has no change, untracked file
# included. Index flags (assume-unchanged, skip-worktree) and ignore rules
# that hide Go build inputs outside dot, underscore and testdata directories
# could make a modified tree look clean, so both are refused.
check_checkout() {
	local gd st
	if [ -f .git ] && ! [ -L .git ]; then
		gd="$(sed -n 's/^gitdir: //p' .git)"
		case "$gd" in /*) ;; *) gd="$ROOT/$gd" ;; esac
		[ -f "$gd/gitdir" ] && [ "$(cat "$gd/gitdir")" = "$ROOT/.git" ] || die "$ROOT/.git is not a worktree entry of this checkout"
	elif ! [ -d .git ] || [ -L .git ]; then
		die "$ROOT has no .git of its own; build the helper from a clone at the release commit"
	fi
	[ "$(git rev-parse --show-toplevel)" = "$ROOT" ] || die "$ROOT is not the top level of its git checkout"
	st="$(git status --porcelain --untracked-files=all)" || die "git status failed in $ROOT"
	[ -z "$st" ] || die "the checkout at $ROOT is not clean (changes or untracked files)"
	st="$(git ls-files -v)" || die "git ls-files failed in $ROOT"
	! grep -qv '^H ' <<<"$st" || die "the index of $ROOT marks files assume-unchanged or skip-worktree"
	st="$(git ls-files -o -i --exclude-standard -- '*.go' '*.s' '*.c' '*.h' '*.m' '*.syso' '*go.mod' '*go.sum' '*go.work' 'vendor')" ||
		die "git ls-files failed in $ROOT"
	! grep -Ev '(^|/)[._]|(^|/)testdata/' <<<"$st" | grep -q . || die "ignore rules hide Go build inputs in $ROOT"
}

# The caller's git location overrides would stamp or check another repository.
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES \
	GIT_COMMON_DIR GIT_CEILING_DIRECTORIES GIT_DISCOVERY_ACROSS_FILESYSTEM GIT_NAMESPACE \
	GIT_CONFIG GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM GIT_CONFIG_COUNT GIT_CONFIG_PARAMETERS
export GIT_NO_REPLACE_OBJECTS=1 # refs/replace must not swap the commit git and go read

# abspath prints PATH made absolute through its (existing) parent directory.
abspath() {
	local d
	d="$(cd "$(dirname "$1")" && pwd -P)" || return 1
	printf '%s/%s' "$d" "$(basename "$1")"
}

OUT="${CASCADE_HELPER_OUT:-}"
[ -n "$OUT" ] || die "CASCADE_HELPER_OUT is required"
OUT="$(abspath "$OUT")" || die "the parent of CASCADE_HELPER_OUT does not exist"
if [ -e "$OUT" ] || [ -L "$OUT" ]; then die "CASCADE_HELPER_OUT ($OUT) already exists"; fi
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
case "$OUT/" in "$ROOT"/*) die "CASCADE_HELPER_OUT ($OUT) is inside the source tree $ROOT" ;; esac
cd "$ROOT"
check_checkout
REV="${CASCADE_HELPER_REVISION:-$(git rev-parse HEAD)}"
[[ "$REV" =~ ^[0-9a-f]{40}$ ]] || die "CASCADE_HELPER_REVISION must be a 40-hex commit"
[ "$(git rev-parse HEAD)" = "$REV" ] || die "the checkout at $ROOT is not at commit $REV (the release cascade's revision)"
mkdir -m 700 "$OUT"
# drop_on_failure (the EXIT trap) removes $OUT when the script fails.
drop_on_failure() {
	local rc=$?
	if [ "$rc" != 0 ]; then rm -rf "$OUT"; fi
}
trap drop_on_failure EXIT

# gobuild runs go (and through it clang) in an empty environment holding only
# the allow-listed variables below plus any NAME=VALUE arguments the script
# itself passes first; everything else of the caller (GOFLAGS, GOTOOLCHAIN,
# GONOSUMDB, CGO_*FLAGS, CCC_OVERRIDE_OPTIONS, SDKROOT, ...) is dropped.
SAFE_ENV=("PATH=$PATH" "HOME=$HOME" "GIT_NO_REPLACE_OBJECTS=1")
for v in TMPDIR GOCACHE GOMODCACHE GOPATH GOROOT DEVELOPER_DIR; do
	if [ -n "${!v:-}" ]; then SAFE_ENV+=("$v=${!v}"); fi
done
gobuild() {
	local pre=()
	while [[ "${1:-}" =~ ^(CGO_ENABLED|GOOS|GOARCH|CC)= ]]; do
		pre+=("$1")
		shift
	done
	env -i "${SAFE_ENV[@]}" ${pre[@]+"${pre[@]}"} go "$@"
}

# check_go_env refuses go env file settings that change what is built or
# where modules come from, and an insecure module source in the environment.
check_go_env() {
	local f bad v
	f="$(gobuild env GOENV </dev/null)"
	if [ -n "$f" ] && [ "$f" != off ] && [ -f "$f" ]; then
		bad='^(GOFLAGS|GOEXPERIMENT|CGO_[A-Z]*FLAGS|GOTOOLCHAIN|GOWORK|GOPROXY|GONOPROXY|GOSUMDB|GONOSUMDB|GONOSUMCHECK|GOINSECURE|GOPRIVATE|GODEBUG|GOFIPS140|GOAMD64|GOARM64|CC|CXX)='
		if grep -qE "$bad" "$f"; then
			die "the go env file sets $(grep -oE "$bad" "$f" | tr -d '=' | tr '\n' ' '); clear them for a release build"
		fi
	fi
	v="$(gobuild env GOPROXY </dev/null | tr '[:upper:]' '[:lower:]')"
	case ",${v//|/,}," in *,file:*|*,http:*) die "GOPROXY names an unauthenticated source ($v)" ;; esac
	[ "$(gobuild env GOSUMDB </dev/null)" != off ] || die "GOSUMDB is off"
	v="$(gobuild env GOWORK </dev/null)"
	[ -z "$v" ] || [ "$v" = off ] || die "a go.work file is in use ($v)"
}

# check_helper ARCH FILE refuses unless FILE is a regular darwin/ARCH cgo
# build that passes the devkeys guard.
check_helper() {
	local arch="$1" file="$2" info
	if ! [ -f "$file" ] || [ -L "$file" ]; then die "go build left no regular file for darwin/$arch"; fi
	info="$(gobuild version -m "$file" </dev/null)" || die "go version -m failed for darwin/$arch"
	grep -Eq '^[[:space:]]+build[[:space:]]+CGO_ENABLED=1$' <<<"$info" || die "darwin/$arch helper is not a cgo build (CGO_ENABLED=1 missing)"
	grep -Eq '^[[:space:]]+build[[:space:]]+GOOS=darwin$' <<<"$info" || die "darwin/$arch helper is not a darwin build"
	grep -Eq '^[[:space:]]+build[[:space:]]+GOARCH='"$arch"'$' <<<"$info" || die "darwin/$arch helper has the wrong GOARCH"
	grep -Eq '^[[:space:]]+path[[:space:]]+github\.com/acamarata/cascade/cmd/cascade-elevate-helper$' <<<"$info" ||
		die "darwin/$arch helper is not built from ./cmd/cascade-elevate-helper"
	grep -Eq '^[[:space:]]+build[[:space:]]+-trimpath=true$' <<<"$info" || die "darwin/$arch helper is not a -trimpath build"
	! grep -Eq '^[[:space:]]+build[[:space:]]+-tags=' <<<"$info" || die "darwin/$arch helper was built with -tags"
	grep -Eq '^[[:space:]]+build[[:space:]]+vcs\.revision='"$REV"'$' <<<"$info" ||
		die "darwin/$arch helper is not built from commit $REV (the release cascade's revision)"
	grep -Eq '^[[:space:]]+build[[:space:]]+vcs\.modified=false$' <<<"$info" || die "darwin/$arch helper was built from a modified tree"
	gobuild run ./internal/build/nodevkeys "$file" </dev/null || die "darwin/$arch helper fails the devkeys guard"
}

check_go_env
for arch in arm64 amd64; do
	mkdir -m 700 "$OUT/darwin_$arch"
	file="$OUT/darwin_$arch/cascade-elevate-helper"
	carch="$arch"
	if [ "$arch" = amd64 ]; then carch=x86_64; fi # clang names the Intel arch x86_64
	gobuild CGO_ENABLED=1 GOOS=darwin GOARCH="$arch" CC="clang -arch $carch" build -trimpath -ldflags '-s -w' -o "$file" ./cmd/cascade-elevate-helper </dev/null ||
		die "go build failed for darwin/$arch"
	check_helper "$arch" "$file"
	printf 'built cascade-elevate-helper darwin/%s\n' "$arch"
done
