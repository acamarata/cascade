#!/bin/sh
# Crash stand-in: on `git worktree add` it SIGKILLs its parent (the Go
# process driving WorktreeManager.Create) before git runs, simulating a
# crash between the intent row and the add. Every other call execs the
# real git unchanged.
if [ "$1" = "worktree" ] && [ "$2" = "add" ]; then
	kill -9 "$PPID"
	exit 137
fi
exec "$CASCADE_REAL_GIT" "$@"
