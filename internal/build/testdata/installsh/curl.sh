#!/bin/sh
# Test shim for curl: logs every URL to $SHIM_LOG and serves files from
# $SHIM_SERVE by URL basename. No network. A missing file exits 22 (HTTP error).
out=""
w=""
url=""
while [ $# -gt 0 ]; do
	case $1 in
	-o) out=$2; shift ;;
	-w) w=$2; shift ;;
	--proto) shift ;;
	http*) url=$1 ;;
	esac
	shift
done
printf '%s\n' "$url" >>"$SHIM_LOG"
if [ -n "$w" ]; then
	printf 'https://github.com/acamarata/cascade/releases/tag/%s' "$SHIM_LATEST"
	exit 0
fi
f=$SHIM_SERVE/${url##*/}
[ -f "$f" ] || exit 22
cp "$f" "$out"
