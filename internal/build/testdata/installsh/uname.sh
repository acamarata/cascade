#!/bin/sh
# Test shim for uname: reports $SHIM_UNAME_S for -s and $SHIM_UNAME_M for -m.
case ${1:-} in
-m) printf '%s\n' "$SHIM_UNAME_M" ;;
*) printf '%s\n' "$SHIM_UNAME_S" ;;
esac
