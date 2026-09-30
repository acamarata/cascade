#!/bin/sh
# Test stand-in for the injected git binary path: records every invocation
# (one line per call) to $CASCADE_GIT_RECORD, then execs the real git with
# the same arguments. Used by the jobs package tests.
printf '%s\n' "$*" >> "$CASCADE_GIT_RECORD"
exec "$CASCADE_REAL_GIT" "$@"
