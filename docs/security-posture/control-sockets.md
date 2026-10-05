# Control sockets: owner-only, 0600, refuse what is not ours

Cascade listens on two local unix sockets: the daemon socket and the socket
`cascade mcp serve --socket` opens (the daemon socket path with `-mcp`
appended). Both are bound by one helper, `internal/runtime.ListenOwnerSocket`
(`internal/runtime/socket_unix.go`), so they follow the same rules. A
request that reaches either socket is still checked by the owner-uid peer
guard (`internal/rpc.ConnContext`); the bind rules below are the first layer.

## Where the socket goes

The path is `CASCADE_SOCKET` when set, else `daemon.sock` under the Cascade
home (`~/.cascade` unless `CASCADE_HOME` says otherwise). The parent
directory is created with mode 0700 if it is missing. Created or not, it must
then pass the directory rule:

| Directory | Result |
|---|---|
| Owned by you, not group- or other-writable (for example 0700 or 0755) | allowed |
| Owned by you or by root, with the sticky bit (for example `/tmp`, 1777) | allowed |
| Group- or other-writable without the sticky bit (0770, 0777) | refused |
| Owned by another user, or by root without the sticky bit | refused |

A sticky shared directory stays allowed. Only an entry's owner can remove or
rename it there, so another user cannot swap the socket out from under the
daemon.

Only the parent directory is checked. Every ancestor directory must be
trusted too: a non-sticky directory higher up that another user can write
lets them swap the whole subtree after the check. ACLs (for example macOS
`chmod +a` entries) are not inspected. Keep `CASCADE_SOCKET` under a
directory tree only you control, or directly in a sticky directory such as
`/tmp`.

## The socket lock

Each socket has a lock file beside it, `<socket path>.lock` (so
`daemon.sock.lock`). A listener takes an exclusive `flock` on it before it
judges the path and holds it until it closes; the kernel drops it if the
process dies. The lock is the only proof that a socket is stale. A refused
connect is not proof: on macOS a live listener whose backlog is full also
refuses a connect.

The lock file is opened without following a symlink and created mode 0600.
It is refused and left in place, and nothing is bound, when it is:

| Found at `<socket path>.lock` | Result |
|---|---|
| A symlink, dangling or not | refused, `permission-denied` |
| A directory, FIFO, socket or anything that is not a regular file | refused, `permission-denied` |
| A file owned by another user | refused, `permission-denied` |
| Your file, but readable or writable by group or others | refused, `permission-denied` |
| Your file, but with other hard links | refused, `permission-denied` |
| Your file, locked by a running listener | `conflict` |

The lock file is never removed. Removing it would let a second process
create and lock a new file while the first still holds the old one.

The daemon's start-up crash-recovery scan (`internal/runtime/recovery.go`)
uses the same lock and the same directory rule. It takes the lock before it
judges the socket path and holds it until the scan ends:

| Found by the scan | Result |
|---|---|
| Nothing at the path | clean start, no lock taken |
| The lock held by a running listener | `conflict` (a daemon is running), even if a connect is refused |
| A regular file, directory or FIFO at the path | refused as a `conflict`, left in place |
| A symlink whose target answers a connect | `conflict` (a daemon is running) |
| A symlink whose target refuses a connect | refused as a `conflict`, link left in place |
| A dangling symlink | refused with an "undecidable" error (no error kind of its own), left in place |
| A socket owned by another user | refused, `permission-denied`, left in place |
| A hostile `.lock` (see the first table) | refused, `permission-denied`, path left in place |
| Your socket, the lock free, and a connect refused | removed as stale |

"Lock held" also covers another start's scan in progress. That start then
aborts as a `conflict`, with nothing removed. It fails safe: the other start
holds the lock for the scan only, and a retry succeeds once it has ended.

The scan releases the lock when it returns, before the daemon binds. If a
second start takes the lock in that gap, it owns the socket: the first
start's bind then fails as a `conflict` and nothing it does removes the
other daemon's socket.

## What may already be at the path

The path is examined with `lstat`, so a symlink is seen as a symlink and
never followed.

| Found at the path | Result |
|---|---|
| Nothing | bind |
| A symlink, dangling or not | refused, left in place |
| A regular file, directory or anything that is not a socket | refused, left in place |
| A socket owned by another user | refused, left in place |
| Your socket, and a process answers on it | refused as a conflict |
| Your socket, the lock was free, and a connect is refused (a crashed run left it) | removed, then bind |

Only that last case removes anything. The path is judged only after the lock
is taken, so a running listener is a conflict before any connect is tried.
The connect then only catches a listener that never took the lock (an older
build). A connect that fails other than by refusal (a timeout, for example)
proves nothing, so it is a conflict too.

Refusals carry error kinds: a directory or path refusal is
`permission-denied`, a held lock or a live or unprovable socket is
`conflict`, and a mkdir, open, bind or listen failure or an over-long path is
`unavailable`. The daemon and `cascade mcp serve --socket` both return these
kinds unchanged.

The path must fit in a unix socket address (104 bytes on macOS, 108 on
Linux). So must the private bind name used below, which is up to 14 bytes
longer than the directory. A path that fails either check is refused by name
before anything is bound.

## How the socket is made 0600 with no wider window

The socket inode is born with the mode the process umask allows. Cascade
never changes the umask, because it is process-wide. Instead:

1. Bind inside a fresh private 0700 directory created beside the path. No
   other user can reach or plant anything there.
2. `lstat` the new inode: it must be a socket you own. Then `chmod` it to
   0600 and check the same inode now has exactly 0600.
3. Call `listen(2)`, still under the private name.
4. Hard-link it to the real path. `link(2)` never follows an entry at the
   destination, so a symlink planted at the path after the first check makes
   the link fail. The entry is then judged again by the table above: a
   symlink is refused and left, your own stale socket is removed and the link
   is tried once more.
5. Remove the private name and directory.

Binding straight to the path would not be enough on macOS: there, `bind(2)`
follows a dangling symlink and creates the socket at its target. Nothing
listens before the inode is 0600, and the path never names a socket that is
not yet listening.

If the process is killed between steps 1 and 5, a private directory (a dot
followed by digits, holding one socket named `s`) stays beside the socket.
It is harmless and is not reaped automatically: the daemon socket and the
MCP socket share a directory but not a lock, so a sweep by one could delete
the other's directory mid-bind. Remove a leftover one by hand while Cascade
is stopped.

The daemon splits these steps in two. `PrepareOwnerSocket` takes the lock
and runs steps 1 to 3, so nothing can dial the path yet. The daemon then
writes its pidfile, and `Publish` runs steps 4 and 5. `cascade daemon start`
treats an answering socket as "up" and the next command reads the pidfile,
so a path that answers always has a pidfile behind it. A second daemon
started at the same moment fails at the lock with a conflict and never
writes or removes the winner's pidfile. If the pidfile write or the publish
fails, the daemon removes only its own pidfile, while it still holds the
lock, then drops the private name and releases the lock.
`ListenOwnerSocket`, which the MCP socket uses, is the two halves in one call.

Closing the listener removes the socket file only if the path still names
the inode it bound, then releases the lock. The daemon's shutdown does
nothing more: it never removes the socket path on its own, so a successor's
socket survives an old run's late cleanup.

## Windows

`internal/runtime/socket_windows.go` keeps the symlink and non-socket refusal
and the stale-socket rule. Windows has no owner uid or mode bits for the
directory rule or 0600; the socket takes its directory's ACL. The socket
lock is unix-only, so on Windows a refused connect still counts as stale,
and a live listener with a full backlog could be mistaken for a crashed one.

## Tests

- `TestListenOwnerSocketRefusesSymlink`, `TestListenOwnerSocketRefusesRegularFile`,
  `TestListenOwnerSocketRefusesForeignOwner`, `TestListenOwnerSocketRefusesWritableDir`
  and `TestListenOwnerSocketRemovesOwnStaleSocket` cover the tables above.
- `TestOwnerSocketBindWindowRefusesConnect` dials during the bind-to-chmod
  window and expects a refusal, then expects an answer before the link.
  `TestOwnerSocketLinkRaceReclassifies` plants a symlink between the check
  and the link.
- `TestListenOwnerSocketHeldLockIsConflict` holds the lock over a socket
  that refuses connects and expects a conflict, then a rebind once the lock
  is free. `TestOwnerSocketLockHostileStates` covers the lock table and
  `TestOwnerSocketCloseLeavesSwappedPath` swaps the path before Close.
- `TestMCPSocketModeIs0600` and `TestMCPSocketRefusesPlantedSymlink` cover the
  MCP socket. The daemon socket tests sit in `internal/daemon/daemon_socket_test.go`,
  including the error kinds reaching the caller and a cleanup that leaves a
  successor's socket. `TestConcurrentDaemonStartKeepsWinnerPidfile` races two
  daemon setups and checks the pidfile order; `TestSetUpSocket_FailureAfterPrepareReleasesLock`
  covers a failed pidfile write and a failed publish.
