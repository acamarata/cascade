#!/bin/sh
# Test shim for mv: logs the destination to $SHIM_MVLOG, then runs the real mv.
for last in "$@"; do :; done
printf '%s\n' "${last##*/}" >>"$SHIM_MVLOG"
exec /bin/mv "$@"
