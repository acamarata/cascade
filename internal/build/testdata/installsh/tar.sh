#!/bin/sh
# Test shim for tar (TOCTOU leg): on the first call, swaps the source archive
# $SHIM_SWAP_TARGET for $SHIM_SWAP_EVIL after the installer already read it,
# then runs the real tar at $SHIM_REAL_TAR.
if [ ! -e "$SHIM_SWAP_EVIL.done" ]; then
	cp "$SHIM_SWAP_EVIL" "$SHIM_SWAP_TARGET"
	: >"$SHIM_SWAP_EVIL.done"
fi
exec "$SHIM_REAL_TAR" "$@"
