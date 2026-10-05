#!/bin/sh
# install.sh: install a Cascade release into ~/.local/bin without sudo.
# Usage: install.sh [tag]. Verify before use: minisign over the checksums
# file with an out-of-band key, sha256 of the VERIFIED archive entry, safe
# extraction, then the receipt, written last. Every failure exits before
# anything is written.
# Exit: 0 installed/already installed, 1 verify/download/write failure, 2
# ambiguous or missing local-lane files, 3 unsupported platform.
# Env: CASCADE_INSTALL_DIST, CASCADE_INSTALL_BASE_URL, CASCADE_MINISIGN_PUBKEY,
# CASCADE_MINISIGN_PUBKEY_URL, CASCADE_HOME. Full docs: docs/install.md.
set -eu

REPO_URL=https://github.com/acamarata/cascade
DEFAULT_BASE_URL=$REPO_URL/releases/download
DEFAULT_KEY_URL=https://acamarata.github.io/cascade/keys/minisign.pub
EXES="cascade-elevate-helper cascade-github cascade" # install order: helper, plugin, cascade last

WORK=""
RECEIPT_TMP=""
cleanup() {
	if [ -n "$RECEIPT_TMP" ]; then rm -f "$RECEIPT_TMP"; fi
	if [ -n "$WORK" ]; then rm -rf "$WORK"; fi
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

say() { printf '%s\n' "$*"; }
die() {
	code=$1
	shift
	printf 'install.sh: %s\n' "$*" >&2
	exit "$code"
}

# manual_steps prints how to download and verify a release by hand.
manual_steps() {
	cat >&2 <<EOF
Install by hand:
  1. Download cascade_checksums.txt, cascade_checksums.txt.minisig and the archive for your platform from $REPO_URL/releases
  2. Get the release public key from $DEFAULT_KEY_URL (also docs/keys/minisign.pub in the repository).
  3. minisign -Vm cascade_checksums.txt -p minisign.pub
  4. Check the archive: its sha256 must equal its line in cascade_checksums.txt.
  5. Extract the archive and copy its executables onto your PATH.
Details: $REPO_URL/blob/main/docs/install.md
EOF
}

# fail_verify stops with exit 1 and the manual verification steps.
fail_verify() {
	printf 'install.sh: %s\n' "$*" >&2
	manual_steps
	exit 1
}

# detect_platform maps uname to OS/ARCH or exits 3.
detect_platform() {
	uos=$(uname -s 2>/dev/null || true)
	uarch=$(uname -m 2>/dev/null || true)
	case $uos in Darwin) OS=darwin ;; Linux) OS=linux ;; *) OS="" ;; esac
	case $uarch in x86_64) ARCH=amd64 ;; aarch64 | arm64) ARCH=arm64 ;; *) ARCH="" ;; esac
	if [ -z "$OS" ] || [ -z "$ARCH" ]; then
		printf 'install.sh: unsupported platform %s/%s; no prebuilt install for it.\n' "$uos" "$uarch" >&2
		manual_steps
		exit 3
	fi
}

# check_hex exits 1 unless $1 is a 64-character lowercase hex digest.
check_hex() {
	case $1 in *[!0-9a-f]* | '') die 1 "not a sha256 digest: $1" ;; esac
	[ "${#1}" -eq 64 ] || die 1 "not a sha256 digest: $1"
}

# sha256 prints the lowercase hex sha256 of file $1.
sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sum=$(sha256sum <"$1") || die 1 "cannot hash $1"
	else
		sum=$(shasum -a 256 <"$1") || die 1 "cannot hash $1"
	fi
	sum=${sum%% *}
	check_hex "$sum"
	printf '%s' "$sum"
}

# fetch downloads URL $1 to file $2 over HTTPS only.
fetch() {
	curl --proto '=https' --tlsv1.2 -fsSL -o "$2" "$1"
}

require_https() {
	case $1 in https://*) ;; *) fail_verify "refusing a non-https URL: $1" ;; esac
}

# resolve_local checks the local lane has exactly one archive, one checksums file and its signature (exit 2 otherwise).
resolve_local() {
	[ -d "$DIST" ] || die 2 "CASCADE_INSTALL_DIST is not a directory: $DIST"
	n=0
	for f in "$DIST"/cascade_*_"$OS"_"$ARCH".tar.gz; do
		if [ -f "$f" ]; then n=$((n + 1)); fi
	done
	[ "$n" -eq 1 ] || die 2 "expected exactly one cascade_*_${OS}_${ARCH}.tar.gz in $DIST, found $n"
	n=0
	for f in "$DIST"/*_checksums.txt; do
		if [ -f "$f" ]; then
			n=$((n + 1))
			SUMS_SRC=$f
		fi
	done
	[ "$n" -eq 1 ] || die 2 "expected exactly one *_checksums.txt in $DIST, found $n"
	[ -f "$SUMS_SRC.minisig" ] || die 2 "missing signature $SUMS_SRC.minisig"
}

# check_tools exits 1 when a required tool is missing.
check_tools() {
	command -v minisign >/dev/null 2>&1 ||
		die 1 "minisign is required to verify the release: see https://jedisct1.github.io/minisign/ (brew install minisign, apt install minisign)"
	for t in tar mktemp find awk; do
		command -v "$t" >/dev/null 2>&1 || die 1 "$t is required"
	done
	if [ -z "$DIST" ]; then command -v curl >/dev/null 2>&1 || die 1 "curl is required"; fi
}

# load_key copies the out-of-band public key into the work dir; it never reads a key from the release source.
load_key() {
	if [ -n "${CASCADE_MINISIGN_PUBKEY:-}" ]; then
		[ -f "$CASCADE_MINISIGN_PUBKEY" ] || fail_verify "public key file not found: $CASCADE_MINISIGN_PUBKEY"
		cp "$CASCADE_MINISIGN_PUBKEY" "$WORK/key.pub" || fail_verify "cannot read the public key"
		return 0
	fi
	key_url=${CASCADE_MINISIGN_PUBKEY_URL:-$DEFAULT_KEY_URL}
	require_https "$key_url"
	fetch "$key_url" "$WORK/key.pub" || fail_verify "cannot fetch the public key from $key_url"
}

# fetch_signed copies or downloads the checksums file and its signature, then verifies the copy with minisign.
fetch_signed() {
	if [ -n "$DIST" ]; then
		cp "$SUMS_SRC" "$WORK/sums" || die 1 "cannot read $SUMS_SRC"
		cp "$SUMS_SRC.minisig" "$WORK/sums.minisig" || die 1 "cannot read the signature"
	else
		fetch "$BASE/$TAG/cascade_checksums.txt" "$WORK/sums" || die 1 "cannot download cascade_checksums.txt for $TAG"
		fetch "$BASE/$TAG/cascade_checksums.txt.minisig" "$WORK/sums.minisig" || die 1 "cannot download the checksums signature for $TAG"
	fi
	minisign -V -q -p "$WORK/key.pub" -m "$WORK/sums" -x "$WORK/sums.minisig" ||
		fail_verify "the checksums signature does not verify with the release public key"
}

# select_archive picks the single verified entry for this platform; the name comes from the signed file only, never the tag.
select_archive() {
	re="^cascade_[0-9A-Za-z.+-]+_${OS}_${ARCH}[.]tar[.]gz\$"
	sel=$(awk -v re="$re" 'NF == 2 && $2 ~ re { n++; h = $1; f = $2 } END { if (n != 1) exit 1; print h, f }' "$WORK/sums") ||
		fail_verify "the verified checksums file has no single archive entry for $OS/$ARCH"
	WANT_SHA=${sel%% *}
	ARCHIVE_NAME=${sel#* }
	check_hex "$WANT_SHA"
}

# fetch_archive copies or downloads the selected archive and compares its sha256.
fetch_archive() {
	if [ -n "$DIST" ]; then
		[ -f "$DIST/$ARCHIVE_NAME" ] || die 1 "the verified archive $ARCHIVE_NAME is not in $DIST"
		cp "$DIST/$ARCHIVE_NAME" "$WORK/archive.tar.gz" || die 1 "cannot read $ARCHIVE_NAME"
	else
		fetch "$BASE/$TAG/$ARCHIVE_NAME" "$WORK/archive.tar.gz" || die 1 "cannot download $ARCHIVE_NAME"
	fi
	got=$(sha256 "$WORK/archive.tar.gz")
	[ "$got" = "$WANT_SHA" ] || fail_verify "sha256 mismatch for $ARCHIVE_NAME"
}

# extract_safe refuses absolute, parent-relative, linked and special entries before and after extracting into the work dir.
extract_safe() {
	a=$WORK/archive.tar.gz
	tar -tzf "$a" >"$WORK/names" || die 1 "cannot list $ARCHIVE_NAME"
	tar -tvzf "$a" >"$WORK/long" || die 1 "cannot list $ARCHIVE_NAME"
	while IFS= read -r entry; do
		case $entry in /* | .. | ../* | */.. | */../*) die 1 "unsafe path in archive: $entry" ;; esac
	done <"$WORK/names"
	bad=$(awk '!/^[-d]/ || / link to /' "$WORK/long")
	[ -z "$bad" ] || die 1 "archive holds a link or special file: $bad"
	mkdir "$WORK/x"
	tar -xzf "$a" -C "$WORK/x" || die 1 "cannot extract $ARCHIVE_NAME"
	bad=$(find "$WORK/x" ! -type f ! -type d -print)
	[ -z "$bad" ] || die 1 "archive holds a link or special file: $bad"
	bad=$(find "$WORK/x" -type f -links +1 -print)
	[ -z "$bad" ] || die 1 "archive holds a hard link: $bad"
}

# check_dst_safe refuses when the destination for $1 is a directory or a symlink to one, before anything is written.
check_dst_safe() {
	dst=$DIR/$1
	if [ -d "$dst" ]; then die 1 "$dst is a directory"; fi
}

# stage_one checks executable $1 at the archive root is a regular file.
stage_one() {
	p=$WORK/x/$1
	if [ -e "$p" ] || [ -L "$p" ]; then
		if [ -L "$p" ] || [ ! -f "$p" ]; then die 1 "$1 in the archive is not a regular file"; fi
		FOUND="$FOUND $1"
	fi
}

# prepare_dir creates the install dir (0755) and checks it is writable.
prepare_dir() {
	if [ ! -d "$DIR" ]; then
		(umask 022 && mkdir -p "$DIR") || die 1 "cannot create $DIR"
		chmod 0755 "$DIR" || die 1 "cannot set the mode of $DIR"
	fi
	[ -w "$DIR" ] || die 1 "$DIR is not writable"
	ABS_DIR=$(cd "$DIR" && pwd -P) || die 1 "cannot resolve $DIR"
	case $ABS_DIR in *'"'* | *\\* | *[[:cntrl:]]*) die 1 "unsupported characters in $ABS_DIR" ;; esac
}

# install_one installs verified executable $1 via a temp file and rename.
install_one() {
	src=$WORK/x/$1
	dst=$DIR/$1
	want=$(sha256 "$src")
	if [ -f "$dst" ] && [ ! -L "$dst" ] && [ "$(sha256 "$dst")" = "$want" ]; then
		say "already installed: $1"
	else
		if [ -d "$dst" ]; then die 1 "$dst is a directory"; fi
		new=$DIR/.$1.new
		rm -f "$new"
		cp "$src" "$new" || die 1 "cannot write $new"
		chmod 0755 "$new" || die 1 "cannot chmod $new"
		mv -f "$new" "$dst" || die 1 "cannot install $dst"
		[ "$(sha256 "$dst")" = "$want" ] || die 1 "$dst does not match the verified archive"
		CHANGED=1
		say "installed: $dst"
	fi
	FILES_JSON="$FILES_JSON${FILES_JSON:+,}{\"name\":\"$1\",\"sha256\":\"$want\"}"
}

# write_receipt writes the install receipt last, via temp file and rename; a converged run leaves it untouched.
write_receipt() {
	rdir=${CASCADE_HOME:-$HOME/.cascade}
	rfile=$rdir/install-receipt.json
	now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
	head="{\"schema_version\":1,\"channel\":\"script\",\"release_tag\":\"$RELEASE_TAG\",\"archive_name\":\"$ARCHIVE_NAME\",\"archive_sha256\":\"$WANT_SHA\",\"install_dir\":\"$ABS_DIR\""
	body="$head,\"installed_at\":\"$now\",\"files\":[$FILES_JSON]}"
	if [ "$CHANGED" -eq 0 ] && [ -f "$rfile" ]; then
		old=$(sed 's/,"installed_at":"[^"]*"//' "$rfile" 2>/dev/null || true)
		if [ "$old" = "$head,\"files\":[$FILES_JSON]}" ]; then
			say "receipt unchanged: $rfile"
			return 0
		fi
	fi
	(umask 022 && mkdir -p "$rdir") || die 1 "cannot create $rdir"
	RECEIPT_TMP=$(mktemp "$rdir/.install-receipt.json.XXXXXX") || die 1 "cannot write in $rdir"
	printf '%s\n' "$body" >"$RECEIPT_TMP" || die 1 "cannot write the receipt"
	chmod 0644 "$RECEIPT_TMP" || die 1 "cannot chmod the receipt"
	mv -f "$RECEIPT_TMP" "$rfile" || die 1 "cannot write $rfile"
	RECEIPT_TMP=""
	say "receipt: $rfile"
}

main() {
	TAG=${1:-}
	case $TAG in *[!0-9A-Za-z._+-]*) die 1 "invalid tag: $TAG" ;; esac
	detect_platform
	DIST=${CASCADE_INSTALL_DIST:-}
	if [ -n "$DIST" ]; then
		resolve_local
		RELEASE_TAG=${TAG:-local}
	else
		BASE=${CASCADE_INSTALL_BASE_URL:-$DEFAULT_BASE_URL}
		require_https "$BASE"
	fi
	check_tools
	[ -n "${HOME:-}" ] || die 1 "HOME is not set"
	case $HOME in /*) ;; *) die 1 "HOME must be an absolute path" ;; esac
	DIR=$HOME/.local/bin
	WORK=$(mktemp -d) || die 1 "cannot create a temporary directory"
	if [ -z "$DIST" ] && [ -z "$TAG" ]; then
		u=$(curl --proto '=https' --tlsv1.2 -fsSLI -o /dev/null -w '%{url_effective}' "$REPO_URL/releases/latest") ||
			die 1 "cannot resolve the latest release"
		TAG=${u##*/}
		case $TAG in '' | latest | *[!0-9A-Za-z._+-]*) die 1 "cannot resolve the latest release tag" ;; esac
	fi
	if [ -z "$DIST" ]; then RELEASE_TAG=$TAG; fi
	load_key
	fetch_signed
	select_archive
	fetch_archive
	extract_safe
	FOUND=""
	for n in $EXES; do stage_one "$n"; done
	case " $FOUND " in *" cascade "*) ;; *) die 1 "the archive carries no cascade executable" ;; esac
	prepare_dir
	for n in $FOUND; do check_dst_safe "$n"; done
	CHANGED=0
	FILES_JSON=""
	for n in $FOUND; do install_one "$n"; done
	write_receipt
}

main "$@"
