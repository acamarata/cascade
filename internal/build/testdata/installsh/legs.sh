#!/bin/sh
# shellcheck disable=SC2016 # CHECK strings are single-quoted on purpose: they expand in the leg's sh -c.
# shellcheck disable=SC2015 # `setup && run NAME W || failsetup NAME` is deliberate: run/remote/leg always
# return 0 (their last statement is an assignment), so failsetup fires only when setup, not run, failed.
# legs.sh INSTALL_SH WORKDIR: verify-before-use legs for install.sh, run by
# TestInstallVerifyLegs. Releases are built here with tar and signed through the
# real minisign with an ephemeral key; the PATH shims in this directory stand in
# for curl (no network), uname, mv and tar. Every leg runs install.sh with a
# fresh HOME and a clean environment. A refusal leg passes only when install.sh
# exits with the wanted code AND the HOME dir is still empty (nothing installed,
# no receipt). Prints PASS/FAIL per leg and "legs: N failed"; exits non-zero on
# any failure.
set -u
INSTALL=$1
W=$2
HERE=$(cd "$(dirname "$0")" && pwd)
AR=cascade_1.0.0_linux_amd64.tar.gz
SUMS=cascade_1.0.0_checksums.txt
failed=0
TAG=v1.0.0

shim_dir() { # shim_dir DIR NAME...: a PATH dir holding the named shims
	mkdir -p "$1"
	d=$1
	shift
	for s in "$@"; do cp "$HERE/$s.sh" "$d/$s" && chmod 0755 "$d/$s"; done
}
sha() { if command -v sha256sum >/dev/null 2>&1; then sha256sum <"$1"; else shasum -a 256 <"$1"; fi | cut -d' ' -f1; }
# failsetup NAME: record a leg as failed when its setup (not install.sh) failed,
# so a broken chain before `run`/`remote` cannot silently drop the leg.
failsetup() { echo "FAIL $1 (setup failed)"; failed=$((failed + 1)); }
# mkrel DIR SUMSNAME ARCHIVE BODY KEY: archive holding a cascade script, a checksums line, its signature
mkrel() {
	rm -rf "$W/src" && mkdir -p "$1" "$W/src"
	printf '#!/bin/sh\necho %s\n' "$4" >"$W/src/cascade" && chmod 0755 "$W/src/cascade"
	tar -czf "$1/$3" -C "$W/src" cascade
	printf '%s  %s\n%s  %s.sbom.json\n' "$(sha "$1/$3")" "$3" "$(sha "$1/$3")" "$3" >"$1/$2"
	minisign -S -s "$5" -m "$1/$2" -x "$1/$2.minisig" -t "test checksums" >/dev/null
}
fresh() { rm -rf "$W/leg" && cp -R "$W/pristine" "$W/leg" && : >"$W/curl.log"; }
# leg NAME WANT_RC HOME [VAR=VALUE...] CMD...: run CMD under env -i and record the verdict
leg() {
	name=$1 want=$2 H=$3
	shift 3
	mkdir -p "$H"
	env -i PATH="${LEGPATH:-$W/bin:$PATH}" HOME="$H" USERPROFILE="$H" CASCADE_HOME="$H/.cascade" \
		CASCADE_INSTALL_DIST="$W/leg/dist" CASCADE_MINISIGN_PUBKEY="$W/k.pub" \
		SHIM_SERVE="$W/leg/serve" SHIM_LOG="$W/curl.log" SHIM_MVLOG="$W/mv.log" SHIM_LATEST=v1.0.0 \
		SHIM_UNAME_S=Linux SHIM_UNAME_M=x86_64 SHIM_REAL_TAR="$(command -v tar)" \
		SHIM_SWAP_EVIL="$W/evil.tar.gz" SHIM_SWAP_TARGET="$W/leg/dist/$AR" \
		"$@" >"$W/out.$name" 2>&1
	rc=$?
	ok=1
	[ "$rc" -eq "$want" ] || ok=0
	if [ "$want" -ne 0 ] && [ -z "${HOME_PRESET:-}" ] && [ -n "$(ls -A "$H")" ]; then ok=0; fi
	[ -z "${CHECK:-}" ] || sh -c "$CHECK" _ "$H" || ok=0
	if [ "$ok" -eq 1 ]; then echo "PASS $name (rc=$rc)"; else
		echo "FAIL $name (rc=$rc, want $want)"
		sed 's/^/    /' "$W/out.$name"
		failed=$((failed + 1))
	fi
	CHECK="" HOME_PRESET=""
}
run() { name=$1 want=$2 H=$W/home.$1; shift 2; leg "$name" "$want" "$H" "$@" sh "$INSTALL" v1.0.0; }
remote() { name=$1 want=$2 H=$W/home.$1; shift 2; leg "$name" "$want" "$H" CASCADE_INSTALL_DIST= \
	CASCADE_INSTALL_BASE_URL=https://example.invalid/dl "$@" sh "$INSTALL" "$TAG"; }

rm -rf "$W" && mkdir -p "$W"
shim_dir "$W/bin" curl uname mv
shim_dir "$W/bin-tar" curl uname mv tar
minisign -G -W -s "$W/k.key" -p "$W/k.pub" >/dev/null
minisign -G -W -s "$W/o.key" -p "$W/o.pub" >/dev/null
mkrel "$W/pristine/dist" "$SUMS" "$AR" good "$W/k.key"
mkrel "$W/pristine/serve" cascade_checksums.txt "$AR" good "$W/k.key"
cp "$W/k.pub" "$W/pristine/serve/k.pub"
rm -rf "$W/src" && mkdir -p "$W/src" && printf '#!/bin/sh\necho evil\n' >"$W/src/cascade" && tar -czf "$W/evil.tar.gz" -C "$W/src" cascade

# Controls: the unmodified release installs in both lanes.
fresh
CHECK='grep -q "^echo good" "$1/.local/bin/cascade" && grep -q "\"channel\":\"script\"" "$1/.cascade/install-receipt.json"'
run local-ok 0
fresh
CHECK='grep -q "\"release_tag\":\"v1.0.0\"" "$1/.cascade/install-receipt.json"'
TAG=""
remote download-latest-tag 0
TAG=v1.0.0

# Refusals: each exits non-zero and leaves HOME empty. `|| failsetup NAME`
# catches a chain whose setup (before `run`/`remote`) fails, so the leg is
# never silently dropped.
fresh && cp "$W/evil.tar.gz" "$W/leg/dist/$AR" && run archive-checksum-mismatch 1 || failsetup archive-checksum-mismatch
fresh && printf 'untrusted comment: x\nRWQgarbage\n' >"$W/leg/dist/$SUMS.minisig" && run garbage-signature 1 || failsetup garbage-signature
fresh && rm "$W/leg/dist/$SUMS.minisig" && run missing-signature-local 2 || failsetup missing-signature-local
fresh && minisign -S -s "$W/o.key" -m "$W/leg/dist/$SUMS" -x "$W/leg/dist/$SUMS.minisig" >/dev/null && run signature-by-other-key 1 || failsetup signature-by-other-key
fresh && printf '%s  %s\n' "$(sha "$W/evil.tar.gz")" "$AR" >"$W/leg/dist/$SUMS" && run checksums-edited-after-signing 1 || failsetup checksums-edited-after-signing
fresh && minisign -S -s "$W/o.key" -m "$W/leg/dist/$SUMS" -x "$W/leg/dist/$SUMS.minisig" >/dev/null &&
	cp "$W/o.pub" "$W/leg/dist/minisign.pub" && cp "$W/o.pub" "$W/leg/dist/$SUMS.pub" && run key-file-in-dist-ignored 1 || failsetup key-file-in-dist-ignored
# Two signed entries for this platform (both real, both correctly hashed): the download
# lane (the local lane refuses two archives itself, earlier) must refuse to pick one.
fresh && cp "$W/leg/serve/$AR" "$W/leg/serve/cascade_1.0.1_linux_amd64.tar.gz" &&
	printf '%s  %s\n%s  %s\n' "$(sha "$W/leg/serve/$AR")" "$AR" "$(sha "$W/leg/serve/$AR")" cascade_1.0.1_linux_amd64.tar.gz >"$W/leg/serve/cascade_checksums.txt" &&
	minisign -S -s "$W/k.key" -m "$W/leg/serve/cascade_checksums.txt" -x "$W/leg/serve/cascade_checksums.txt.minisig" >/dev/null &&
	remote two-matching-archives 1 || failsetup two-matching-archives
fresh && rm "$W/leg/serve/cascade_checksums.txt.minisig" && remote missing-signature-download 1 || failsetup missing-signature-download
fresh && head -c 100 "$W/pristine/serve/$AR" >"$W/leg/serve/$AR" && remote partial-download 1 || failsetup partial-download
fresh && CHECK='test ! -s "'"$W"'/curl.log"' && run http-base-url 1 CASCADE_INSTALL_DIST= CASCADE_INSTALL_BASE_URL=http://example.invalid/dl || failsetup http-base-url
fresh && CHECK='test ! -s "'"$W"'/curl.log"' && run http-key-url 1 CASCADE_MINISIGN_PUBKEY= CASCADE_MINISIGN_PUBKEY_URL=http://example.invalid/k.pub || failsetup http-key-url
fresh && run unreachable-key-url 1 CASCADE_MINISIGN_PUBKEY= CASCADE_MINISIGN_PUBKEY_URL=https://127.0.0.1:9/minisign.pub || failsetup unreachable-key-url
fresh && CHECK='grep -q "^install.sh: minisign is required" "'"$W"'/out.minisign-absent"'
mkdir -p "$W/nominisign"
for t in sh tar mktemp find awk cp rm chmod mv mkdir sed date cat cut sha256sum shasum perl; do
	if p=$(command -v "$t"); then ln -sf "$p" "$W/nominisign/$t"; fi
done
cp "$W/bin/uname" "$W/nominisign/uname"
LEGPATH=$W/nominisign
run minisign-absent 1
LEGPATH=""
if [ "$(id -u)" -ne 0 ]; then
	fresh && mkdir -p "$W/home.unwritable/.local/bin" && chmod 0555 "$W/home.unwritable/.local/bin"
	CHECK='test -z "$(ls -A "$1/.local/bin")" && test ! -e "$1/.cascade"' HOME_PRESET=1
	leg unwritable-install-dir 1 "$W/home.unwritable" sh "$INSTALL" v1.0.0
	chmod 0755 "$W/home.unwritable/.local/bin"
fi

# Download lane: the archive name comes from the verified checksums file, never the tag.
fresh && rm "$W/leg/serve/$AR"* && mkrel "$W/leg/serve" cascade_checksums.txt cascade_1.2.3-renamed_linux_amd64.tar.gz good "$W/k.key"
CHECK='grep -q "^https://example.invalid/dl/v9.9.9/cascade_1.2.3-renamed_linux_amd64.tar.gz$" "'"$W"'/curl.log"'
TAG=v9.9.9
remote renamed-archive-from-verified-entry 0
TAG=v1.0.0
fresh
CHECK='grep -q "^echo good" "$1/.local/bin/cascade"'
remote key-from-https-url 0 CASCADE_MINISIGN_PUBKEY= CASCADE_MINISIGN_PUBKEY_URL=https://keys.invalid/k.pub

# TOCTOU: the tar shim swaps the dist archive after install.sh copied it; the
# verified copy is what gets installed.
fresh
CHECK='grep -q "^echo good" "$1/.local/bin/cascade" && cmp -s "'"$W"'/evil.tar.gz" "'"$W"'/leg/dist/'"$AR"'"'
LEGPATH=$W/bin-tar:$PATH
run archive-swapped-after-verification 0
LEGPATH=""

# Idempotence: a second run of the same tag reports already installed and
# leaves every mtime and the receipt bytes unchanged.
H=$W/home.local-ok
touch -t 200101010000 "$H/.local/bin/cascade" "$H/.cascade/install-receipt.json"
touch -t 200101010001 "$W/ref"
cp "$H/.cascade/install-receipt.json" "$W/receipt.before"
fresh
CHECK='grep -q "^already installed: cascade$" "'"$W"'/out.idempotent-rerun" && cmp -s "'"$W"'/receipt.before" "$1/.cascade/install-receipt.json" && test -z "$(find "$1/.local/bin" "$1/.cascade" -type f -newer "'"$W"'/ref")"'
leg idempotent-rerun 0 "$H" sh "$INSTALL" v1.0.0

echo "legs: $failed failed"
[ "$failed" -eq 0 ]
