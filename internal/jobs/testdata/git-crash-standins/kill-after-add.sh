#!/bin/sh
# Crash stand-in: on `git worktree add` it runs the real git, then SIGKILLs
# its parent (the Go process driving WorktreeManager.Create) before Create
# can re-check the fence or journal its ack. Every other call execs the
# real git unchanged.
if [ "$1" = "worktree" ] && [ "$2" = "add" ]; then
	"$CASCADE_REAL_GIT" "$@"
	rc=$?
	kill -9 "$PPID"
	exit "$rc"
fi
exec "$CASCADE_REAL_GIT" "$@"
