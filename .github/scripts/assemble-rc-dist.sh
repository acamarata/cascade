#!/usr/bin/env bash
# assemble-rc-dist.sh [all|sign|submit|finish]: turn the CI build-<id>
# payload into the signed dist-<id> payload on the build Mac.
#
#   sign    copy the build input into a private work directory and verify
#           every copied file against the copied cascade_checksums.txt before
#           anything else; build both darwin helpers (build-elevate-helper.sh);
#           extract each darwin archive, place the helper beside cascade and
#           codesign cascade-elevate-helper, cascade-github and cascade
#           (hardened runtime, identifiers set, entitlements only on the
#           helper), then `codesign --verify --strict` each.
#   submit  zip the six signed executables and notarize them through
#           notary-lib.sh (journal intent, then the submission id). Skip mode
#           journals "skip" instead.
#   finish  real mode: one notarytool poll (exit 75 while In Progress).
#           Accepted, or skip mode: re-pack each darwin archive
#           deterministically, re-check the packed executables against the
#           signed ones, regenerate their SBOMs with the pinned syft, rewrite
#           the checksums and the cask, add cascade_images.txt, minisign the
#           checksums and move the finished payload to CASCADE_DIST_OUT.
#   all     (default) sign, submit and finish in one call.
#
# Env: CASCADE_BUILD_DIR (the pulled build-<id> payload), CASCADE_DIST_OUT
# (must not exist; created exclusively by the final step, so a refusal leaves
# nothing there), CASCADE_APPLE_SIGNING_IDENTITY ("-" = ad-hoc),
# CASCADE_NOTARY_SKIP=1 (skip notarization). Ad-hoc identity and skip mode
# are refused unless the version is a -SNAPSHOT- rehearsal, never a release.
# CASCADE_MINISIGN_KEY (key file path; its passphrase is read by minisign from
# this script's stdin, never argv or env), CASCADE_RELEASE_VERSION; real mode
# also reads the notary key env of notary-lib.sh. Work state lives in
# "$CASCADE_DIST_OUT.work" (0700); "$CASCADE_DIST_OUT.lock" admits one
# assembly per output at a time. The helper must come from the commit the
# darwin cascade was built from (vcs.revision, clean tree), and every re-packed
# mtime is that commit's time (vcs.time of the checksum-verified cascade).
# fd 3 (the passphrase) is closed for sign, submit, the notarization poll,
# packing and publish, so go, clang, codesign, ditto, notarytool and syft never
# see it; base utilities of the preamble and the finish checks inherit it. Linux and
# windows archives, their SBOMs and install.sh are copied byte-identical.
# Nothing is written to any keychain.
set -eu
set -o pipefail
umask 077
exec 3<&0 0</dev/null # stdin (the minisign passphrase) is kept for minisign only

die() {
	printf 'assemble-rc-dist: %s\n' "$*" >&2
	exit 1
}

SYFT_PIN=1.52.0 # the syft release.yml installs
EXES="cascade-elevate-helper cascade-github cascade"
MEMBERS="LICENSE README.md THIRD_PARTY_LICENSES cascade cascade-github"
ARCHS="arm64 amd64"
SUMS=cascade_checksums.txt
MODE="${1:-all}"
case "$MODE" in all | sign | submit | finish) ;; *) die "usage: assemble-rc-dist.sh [all|sign|submit|finish]" ;; esac
BUILD="${CASCADE_BUILD_DIR:-}"
OUT="${CASCADE_DIST_OUT:-}"
IDENTITY="${CASCADE_APPLE_SIGNING_IDENTITY:-}"
VER="${CASCADE_RELEASE_VERSION:-}"
KEY="${CASCADE_MINISIGN_KEY:-}"
SKIP=0
if [ "${CASCADE_NOTARY_SKIP:-}" = 1 ]; then SKIP=1; fi
[ -n "$BUILD" ] && [ -d "$BUILD" ] || die "CASCADE_BUILD_DIR must name the build-<id> directory"
[ -n "$OUT" ] || die "CASCADE_DIST_OUT is required"
# abspath prints PATH made absolute through its (existing) parent directory.
abspath() {
	local d
	d="$(cd "$(dirname "$1")" && pwd -P)" || return 1
	printf '%s/%s' "$d" "$(basename "$1")"
}
OUT="$(abspath "$OUT")" || die "the parent of CASCADE_DIST_OUT does not exist"
BUILD="$(cd "$BUILD" && pwd -P)"
if [ -e "$OUT" ] || [ -L "$OUT" ]; then die "CASCADE_DIST_OUT ($OUT) already exists"; fi
[ -n "$IDENTITY" ] || die "CASCADE_APPLE_SIGNING_IDENTITY is required (\"-\" for ad-hoc)"
[[ "$VER" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || die "CASCADE_RELEASE_VERSION is missing or malformed"
[ -n "$KEY" ] && [ -f "$KEY" ] && [ -r "$KEY" ] || die "CASCADE_MINISIGN_KEY must name a readable key file"
KEY="$(abspath "$KEY")"
if { [ "$SKIP" = 1 ] || [ "$IDENTITY" = - ]; } && [[ "$VER" != *-SNAPSHOT-* ]]; then
	die "ad-hoc signing and CASCADE_NOTARY_SKIP=1 are only for a -SNAPSHOT- rehearsal, never a release version"
fi
SCRIPTS="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ENT="$SCRIPTS/../../apps/elevate-helper/entitlements.plist"
WORK="$OUT.work"
LOCK="$OUT.lock"
JOURNAL="$WORK/notary-journal.log"
# shellcheck source=.github/scripts/notary-lib.sh
. "$SCRIPTS/notary-lib.sh"
if [ "$SKIP" = 0 ]; then notary_require_real; fi
[ "$(syft version 3<&- | awk '$1 == "Version:" {print $2}')" = "$SYFT_PIN" ] || die "syft $SYFT_PIN (the pinned release) is required"
command -v minisign >/dev/null || die "minisign is required"

regular() { [ -f "$1" ] && ! [ -L "$1" ]; }
mkdir "$LOCK" 2>/dev/null || die "another assembly holds $LOCK (remove it only when no assembly runs)"
CLEAN=() # removed when the script fails (any exit but 0 and 75)
on_exit() {
	local rc=$?
	if [ "$rc" != 0 ] && [ "$rc" != 75 ]; then rm -rf ${CLEAN[@]+"${CLEAN[@]}"}; fi
	rm -rf "$LOCK"
}
trap on_exit EXIT
sha() { shasum -a 256 "$1" | cut -d ' ' -f 1; }
darwin_archive() { printf 'cascade_%s_darwin_%s.tar.gz' "$VER" "$1"; }

# copy_inputs copies the checksums file and every file it lists into
# $WORK/in, refusing malformed lines, duplicates, paths and non-regular files,
# then verifies the copies (never the originals) against the copied sums.
copy_inputs() {
	local in="$WORK/in" sum name seen=" "
	mkdir -m 700 "$in" "$in/Casks"
	regular "$BUILD/$SUMS" || die "$SUMS is missing from the build input"
	cp "$BUILD/$SUMS" "$in/$SUMS"
	while IFS= read -r line; do
		[[ "$line" =~ ^([0-9a-f]{64})\ \ ([A-Za-z0-9_+-][A-Za-z0-9._+-]*)$ ]] || die "malformed $SUMS line: $line"
		name="${BASH_REMATCH[2]}"
		case "$seen" in *" $name "*) die "$name is listed twice" ;; esac
		seen="$seen$name "
		case "$name" in "$SUMS" | cascade_images.txt | *.minisig) die "$name must not be listed in the build $SUMS" ;; esac
		regular "$BUILD/$name" || die "$name is listed but not a regular file in the build input"
		cp "$BUILD/$name" "$in/$name"
	done <"$in/$SUMS"
	(cd "$in" && shasum -a 256 -c -q "$SUMS") || die "the build input does not match its $SUMS"
	for arch in $ARCHS; do
		for name in "$(darwin_archive "$arch")" "$(darwin_archive "$arch").sbom.json"; do
			case "$seen" in *" $name "*) ;; *) die "$name is not listed in $SUMS" ;; esac
		done
	done
	case "$seen" in *" install.sh "*) ;; *) die "install.sh is not listed in $SUMS" ;; esac
	copy_extra
}

# copy_extra copies Casks/cascade.rb and cascade_images.txt (required for a
# final version; a snapshot rehearsal has no images).
copy_extra() {
	local in="$WORK/in"
	regular "$BUILD/Casks/cascade.rb" || die "Casks/cascade.rb is missing from the build input"
	cp "$BUILD/Casks/cascade.rb" "$in/Casks/cascade.rb"
	if regular "$BUILD/cascade_images.txt"; then
		cp "$BUILD/cascade_images.txt" "$in/cascade_images.txt"
	elif [[ "$VER" != *-SNAPSHOT-* ]]; then
		die "cascade_images.txt is missing from the build input"
	fi
}

# stage_arch extracts one darwin archive (exactly the expected regular
# members) into $WORK/stage/darwin_ARCH.
stage_arch() {
	local arch="$1" a d list
	a="$WORK/in/$(darwin_archive "$arch")"
	d="$WORK/stage/darwin_$arch"
	list="$(tar -tzf "$a" | LC_ALL=C sort | tr '\n' ' ')" || die "cannot list $(basename "$a")"
	# shellcheck disable=SC2086 # fixed names without spaces
	[ "$list" = "$(printf '%s\n' $MEMBERS | LC_ALL=C sort | tr '\n' ' ')" ] || die "$(basename "$a") does not hold exactly: $MEMBERS"
	mkdir -p "$d"
	tar -xzf "$a" -C "$d" || die "cannot extract $(basename "$a")"
	for m in $MEMBERS; do regular "$d/$m" || die "$m in $(basename "$a") is not a regular file"; done
	vcs_info "$d/cascade" >"$d.vcs"
}

# vcs_info FILE prints "<vcs.revision> <vcs.time>" of a cascade binary built
# from a clean tree, or refuses.
vcs_info() {
	local info rev t
	info="$(go version -m "$1")" || die "go version -m failed for $1"
	rev="$(sed -n -E 's/^[[:space:]]+build[[:space:]]+vcs\.revision=([0-9a-f]{40})$/\1/p' <<<"$info")"
	t="$(sed -n -E 's/^[[:space:]]+build[[:space:]]+vcs\.time=([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z)$/\1/p' <<<"$info")"
	[ -n "$rev" ] && [ -n "$t" ] || die "$1 carries no vcs.revision and vcs.time"
	grep -Eq '^[[:space:]]+build[[:space:]]+vcs\.modified=false$' <<<"$info" || die "$1 was built from a modified tree"
	printf '%s %s\n' "$rev" "$t"
}

# sign_exe signs one executable with its own identifier and checks that the
# signature verifies strictly and names that identifier.
sign_exe() {
	local f="$1" id ts=--timestamp ent=()
	id="$(basename "$f")"
	if [ "$IDENTITY" = - ]; then ts=--timestamp=none; fi
	if [ "$id" = cascade-elevate-helper ]; then ent=(--entitlements "$ENT"); fi
	codesign --force --options runtime "$ts" --identifier "$id" ${ent[@]+"${ent[@]}"} --sign "$IDENTITY" "$f" ||
		die "codesign failed for $f"
	codesign --verify --strict -R "=identifier \"$id\"" "$f" || die "codesign --verify --strict refused $f"
}

do_sign() {
	rm -rf "$WORK"
	mkdir -m 700 "$WORK"
	CLEAN=("$WORK")
	journal "begin"
	copy_inputs
	[ -f "$ENT" ] || die "missing $ENT"
	for arch in $ARCHS; do stage_arch "$arch"; done
	cmp -s "$WORK/stage/darwin_arm64.vcs" "$WORK/stage/darwin_amd64.vcs" || die "the darwin archives come from different commits"
	rev="$(cut -d ' ' -f 1 "$WORK/stage/darwin_arm64.vcs")"
	if regular "$BUILD/metadata.json"; then
		grep -Eq '"commit": *"'"$rev"'"' "$BUILD/metadata.json" || die "metadata.json names another commit than the darwin cascade ($rev)"
	fi
	CASCADE_HELPER_OUT="$WORK/helpers" CASCADE_HELPER_REVISION="$rev" bash "$SCRIPTS/build-elevate-helper.sh" ||
		die "the helper build was refused"
	for arch in $ARCHS; do
		cp "$WORK/helpers/darwin_$arch/cascade-elevate-helper" "$WORK/stage/darwin_$arch/cascade-elevate-helper"
		for x in $EXES; do sign_exe "$WORK/stage/darwin_$arch/$x"; done
	done
	(cd "$WORK/stage" && for arch in $ARCHS; do for x in $EXES; do shasum -a 256 "darwin_$arch/$x"; done; done) >"$WORK/signed.sha256"
	printf 'identity=%s\nskip=%s\n' "$IDENTITY" "$SKIP" >"$WORK/signed.mode"
	journal "signed"
	CLEAN=()
	printf 'signed the darwin executables in %s\n' "$WORK/stage"
}

# check_signed refuses unless the staged executables are the ones signed.
check_signed() {
	[ -f "$WORK/signed.sha256" ] || die "nothing signed yet; run sign first"
	(cd "$WORK/stage" && shasum -a 256 -c -q ../signed.sha256) || die "a signed executable changed after signing"
	[ "$(cat "$WORK/signed.mode" 2>/dev/null)" = "$(printf 'identity=%s\nskip=%s' "$IDENTITY" "$SKIP")" ] ||
		die "the executables were signed with another identity or notarization mode; run sign again"
}

do_submit() {
	local zip="$WORK/notary/submit.zip" out id
	check_signed
	if [ "$SKIP" = 1 ]; then
		journal "skip"
		printf 'notarization skipped (CASCADE_NOTARY_SKIP=1): a %s rehearsal, never a release\n' "$VER"
		return 0
	fi
	rm -rf "$WORK/notary"
	mkdir -m 700 "$WORK/notary"
	for arch in $ARCHS; do
		mkdir -p "$WORK/notary/exes/darwin_$arch"
		for x in $EXES; do cp "$WORK/stage/darwin_$arch/$x" "$WORK/notary/exes/darwin_$arch/$x"; done
	done
	(cd "$WORK/notary/exes" && shasum -a 256 -c -q ../../signed.sha256) || die "the notarized copies differ from the signed executables"
	ditto -c -k --keepParent "$WORK/notary/exes" "$zip" || die "cannot zip the executables"
	journal "intent zip-sha256=$(sha "$zip")"
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

# notarized returns 0 when the confirmed submission is Accepted, exits 75 while
# it is In Progress and refuses otherwise.
notarized() {
	local id out status
	id="$(confirmed_id)"
	valid_id "$id" || die "no submission confirmed after the last submit; run submit again"
	if ! out="$(notary info "$id" --output-format json 2>&1)"; then
		redact "$out"
		journal "poll-failed id=$id"
		die "notarytool info failed for $id"
	fi
	status="$(json_field status "$out")"
	case "$status" in
	Accepted) journal "status id=$id Accepted" ;;
	"In Progress")
		journal "status id=$id In Progress"
		printf 'notarization %s: In Progress (run finish again)\n' "$id"
		exit 75
		;;
	Invalid | Rejected)
		journal "status id=$id $status"
		if ! out="$(notary log "$id" "$WORK/notary-log.json" 2>&1)"; then redact "$out"; fi
		die "notarization $id: $status; see $WORK/notary-log.json"
		;;
	*)
		journal "status id=$id unexpected"
		redact "$out"
		die "notarization $id: unexpected status"
		;;
	esac
}

# pack_arch re-packs one darwin archive deterministically (C-sorted names,
# commit-time mtime, uid/gid 0, no xattrs, gzip -n), then extracts the result
# and checks every executable is byte-identical to the signed one and still
# verifies.
pack_arch() {
	local arch="$1" d name chk ts
	d="$WORK/stage/darwin_$arch"
	name="$(darwin_archive "$arch")"
	ts="$(cut -d ' ' -f 2 "$d.vcs")"
	chmod 0644 "$d/LICENSE" "$d/README.md" "$d/THIRD_PARTY_LICENSES"
	chmod 0755 "$d/cascade" "$d/cascade-github" "$d/cascade-elevate-helper"
	# shellcheck disable=SC2046,SC2086 # word-split fixed names without spaces
	(cd "$d" && touch -d "$ts" $MEMBERS cascade-elevate-helper &&
		COPYFILE_DISABLE=1 tar --format ustar --uid 0 --gid 0 --uname '' --gname '' --no-xattrs --no-acls \
			--no-fflags --no-mac-metadata -cf - $(printf '%s\n' $MEMBERS cascade-elevate-helper | LC_ALL=C sort)) |
		gzip -n -9 >"$WORK/out/$name" || die "cannot pack $name"
	chk="$WORK/check/darwin_$arch"
	mkdir -p "$chk"
	tar -xzf "$WORK/out/$name" -C "$chk" || die "cannot re-read $name"
	for x in $EXES; do
		grep -qx "$(sha "$chk/$x")  darwin_$arch/$x" "$WORK/signed.sha256" || die "$x in $name is not the signed executable"
		codesign --verify --strict -R "=identifier \"$x\"" "$chk/$x" || die "$x in $name does not verify"
	done
	(cd "$WORK/out" && SYFT_FILE_METADATA_CATALOGER_ENABLED=true syft scan "$name" --output "spdx-json=$name.sbom.json" \
		>"$WORK/syft-$arch.log" 2>&1) || die "syft failed for $name (log: $WORK/syft-$arch.log)"
}

# write_sums rewrites the darwin archive and SBOM lines in input order and
# appends cascade_images.txt; write_cask swaps the darwin sha256 fields and
# adds the helper binary on macOS.
write_sums() {
	local sum name new
	while read -r sum name; do
		case "$name" in cascade_"$VER"_darwin_*) new="$(sha "$WORK/out/$name")" ;; *)
			new="$sum"
			cp "$WORK/in/$name" "$WORK/out/$name"
			;;
		esac
		printf '%s  %s\n' "$new" "$name"
	done <"$WORK/in/$SUMS" >"$WORK/out/$SUMS"
	if [ -f "$WORK/in/cascade_images.txt" ]; then
		cp "$WORK/in/cascade_images.txt" "$WORK/out/cascade_images.txt"
		printf '%s  cascade_images.txt\n' "$(sha "$WORK/out/cascade_images.txt")" >>"$WORK/out/$SUMS"
	fi
}

write_cask() {
	local a64 x64
	a64="$(awk -v n="$(darwin_archive arm64)" '$2 == n {print $1}' "$WORK/in/$SUMS")"
	x64="$(awk -v n="$(darwin_archive amd64)" '$2 == n {print $1}' "$WORK/in/$SUMS")"
	mkdir -m 700 "$WORK/out/Casks"
	awk -v oa="$a64" -v na="$(sha "$WORK/out/$(darwin_archive arm64)")" -v ox="$x64" -v nx="$(sha "$WORK/out/$(darwin_archive amd64)")" '
		$0 ~ "^ *sha256 \"" oa "\"$" { sub(oa, na); a++ }
		$0 ~ "^ *sha256 \"" ox "\"$" { sub(ox, nx); x++ }
		/cascade-elevate-helper/ { h++ }
		{ print }
		/^  on_macos do$/ { print "    binary \"cascade-elevate-helper\""; m++ }
		END { if (a != 1 || x != 1 || h != 0 || m != 1) exit 1 }' "$WORK/in/Casks/cascade.rb" >"$WORK/out/Casks/cascade.rb" ||
		die "the cask does not carry exactly one sha256 per darwin archive and one on_macos block"
}

# check_payload DIR refuses unless DIR matches its checksums and holds exactly
# the listed files, the checksums, their signature and the cask.
check_payload() {
	(cd "$1" && shasum -a 256 -c -q "$SUMS") || die "the assembled payload does not match its $SUMS"
	[ "$(cd "$1" && find . -type f | sed 's|^\./||' | LC_ALL=C sort)" = "$( (awk '{print $2}' "$1/$SUMS" &&
		printf '%s\n' "$SUMS" "$SUMS.minisig" Casks/cascade.rb) | LC_ALL=C sort)" ] || die "the assembled payload has an unexpected layout"
}

# publish moves the checked payload into CASCADE_DIST_OUT, which it creates
# exclusively (mkdir fails when anything appeared there meanwhile), and checks
# it again in place.
publish() {
	mkdir "$OUT" 2>/dev/null || die "CASCADE_DIST_OUT ($OUT) appeared during the assembly"
	CLEAN=("$WORK/out" "$WORK/check" "$OUT")
	mv "$WORK/out/"* "$OUT/" || die "cannot move the payload into $OUT"
	check_payload "$OUT"
	CLEAN=()
}

do_finish() {
	check_signed
	if [ "$SKIP" = 1 ]; then
		[ "$(notary_last_state)" = skip ] || die "finish in skip mode needs a skip-mode submit first"
	else
		notarized 3<&-
	fi
	rm -rf "$WORK/out" "$WORK/check"
	mkdir -m 700 "$WORK/out" "$WORK/check"
	CLEAN=("$WORK/out" "$WORK/check")
	for arch in $ARCHS; do pack_arch "$arch" 3<&-; done
	write_sums 3<&-
	write_cask 3<&-
	minisign -S -s "$KEY" -t "cascade $VER checksums" -m "$WORK/out/$SUMS" <&3 >/dev/null 2>&1 || die "minisign could not sign $SUMS"
	check_payload "$WORK/out"
	publish 3<&-
	rm -rf "$WORK"
	printf 'assembled %s\n' "$OUT"
}

case "$MODE" in
sign) do_sign 3<&- ;;
submit) do_submit 3<&- ;;
finish) do_finish ;;
all)
	do_sign 3<&-
	do_submit 3<&-
	do_finish
	;;
esac
