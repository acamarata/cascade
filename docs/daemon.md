# Daemon startup, IPC surface, and crash recovery

This document covers the daemon's startup sequence, the IPC surface it
serves once started, and its crash-safety recovery scan: what the scan
checks, how it decides an artifact is stale, and what it does when it
cannot decide. Package home: [`internal/runtime`](../internal/runtime)
(`bootstrap.go`, `recovery.go`, `recovery_scan.go`, `lockfile.go`,
`domain_registry.go`) and [`internal/daemon`](../internal/daemon)
(`lifecycle_unix.go`, `lifecycle_unix_serve.go`, `daemon.go`).

## IPC surface

Once the socket is bound, every accepted connection is served for real:
`POST /rpc` (JSON-RPC 2.0, `internal/rpc`) and `GET /events` (a
server-sent-events bridge to the daemon's internal event bus,
`internal/rpc`'s `SSEHandler`) are both mounted on the same
`*http.Server`. A connection accepted while the daemon is mid-drain
(upgrade-in-place or ordinary shutdown) is refused rather than accepted
and silently dropped. `GET /events` binds to a single event-bus namespace,
`"daemon"`, the only namespace anything publishes to today (the
upgrade-in-place engine's `ShutdownRequested` event).

## Startup sequence

`cascade daemon run`'s real entrypoint
(`cmd/cascade/daemon_unix.go`'s `platformDaemonRun`) runs these steps in
order:

1. Resolve paths and load `config.toml`, running the schema-version
   upgrade frame if needed, and resolve `[daemon]` settings (socket path,
   shutdown grace).
2. Construct the log provider.
3. Open the daemon's on-disk store (`cascade.db`, `providers/sqlite`) and
   construct the event bus over it.
4. Run the crash-safety recovery scan (`runtime.Scan`, described below),
   with a real `StoreDomainRegistry` (backed by the same store) wired in
   for the orphaned-advisory-lock step.
5. Serve the IPC surface (above) and, for a real release build, the
   upgrade-in-place engine (see below), until a termination signal or the
   context is canceled.

`internal/runtime.Bootstrap` covers the same path/config/log sequence for
callers that do not also need the daemon-specific store, event bus, and
recovery-registry wiring; the daemon's own run path calls `runtime.Scan`
directly rather than through `Bootstrap`, so it keeps using the same
injectable `PathProvider` every other `cascade daemon` verb in that file
uses, rather than `Bootstrap`'s own `Getenv`/`HomeDir`-based path
resolution.

The recovery scan runs before the IPC surface is served. This placement is
deliberate: the scan's own socket probe is the daemon's write-arbitration
contract's socket-probe-first rule, and it has to run before any lock on
the daemon's store or advisory-lock state is taken. A scan that ran after
lock acquisition could never clean up the very lock blocking it, since it
would already be blocked waiting on that lock.

The scan's result (a `*RecoveryEvent`, or `nil` on a clean start) is
published to the daemon's event bus by the scan itself; the caller has
nothing further to do with it.

## Crash recovery

The recovery scan handles the case where the daemon's previous process
did not exit cleanly: killed with `SIGKILL`, terminated by an OOM killer,
or lost to a power failure. In all of these cases the process cannot run
its own shutdown code, so anything it would normally clean up on exit
(its pidfile, its IPC socket file, any advisory locks it held) is left
behind on disk.

The scan's first action, before touching the pidfile or any lock, is a
socket probe: it attempts to connect to the daemon's IPC socket. If a live
daemon answers, the scan aborts immediately with no cleanup performed.
A live daemon is never disturbed. Only when the probe confirms no live
daemon is listening does the scan proceed to check the pidfile and
advisory locks.

On Windows, this whole scan is skipped: the daemon socket probe is
unix-socket only, and the daemon itself is not supported on Windows. See
[Windows](#windows) below.

## Artifact classes

The scan checks three kinds of leftover state, in this order.

### IPC socket

Staleness is decided by attempting to dial the socket path:

- No file exists at the socket path: nothing to check. This is the
  ordinary clean-startup case, not a stale-socket case.
- The dial succeeds (something answers): a live daemon is running. The
  scan aborts with an error and performs no cleanup at all, not even
  removing files that later turn out to be genuinely stale.
- The dial fails with connection-refused: the socket file exists but
  nothing is listening on it. This is the confirmed-stale case. The
  socket file is removed and a warning is logged.
- The dial fails any other way (permission denied, an unexpected network
  error): this cannot be classified as either live or stale. The scan
  treats this as undecidable and aborts with an error rather than
  guessing.

### Pidfile

The pidfile is read and its content parsed as an integer pid:

- No pidfile exists: nothing to check, ordinary clean state.
- The content is not a valid pid (empty, non-integer, non-positive): the
  scan logs a warning and leaves the pidfile in place. This is reported,
  not silently ignored, but it does not stop the scan.
- The content parses to a pid: the scan checks whether that pid is a live
  process. Only when the check unambiguously confirms the process is dead
  does the scan remove the pidfile and log a warning naming the pid. If
  the process is confirmed alive, or if the liveness check itself could
  not decide, the pidfile is left in place and a warning is logged
  explaining why.

### Orphaned advisory locks

This step depends on an injected registry of advisory-lock records. In
the real daemon startup path this is `runtime.StoreDomainRegistry`
(`internal/runtime/domain_registry.go`): a `provider.Store`-backed
ledger of `lock_id`/`owner_pid` records, sharing the daemon's own
`cascade.db`. A caller that supplies no registry (`nil`) skips the step
outright, reported as such rather than silently pretending to check;
this is still true for any composition root that has no store to build
one from. Where a registry is present:

- Every advisory-lock record is checked against its stored owner pid.
- Only locks whose owner pid is unambiguously confirmed dead are
  released.
- A lock whose owner is alive, or whose liveness could not be decided, is
  left in place.
- If a single lock's release fails, the scan logs a warning and continues
  checking the remaining locks rather than stopping.
- If listing the locks fails outright, the scan logs a warning and skips
  this step entirely. Any pidfile or socket cleanup already performed
  earlier in the scan is unaffected.

## The undecidable case: refuse rather than guess

The scan's liveness check on a pid has three possible outcomes, not two:

- **Dead**: the operating system unambiguously reports no such process.
  This is the only outcome that licenses removing anything.
- **Alive**: either a process genuinely holds that pid, or a process
  holds it but the daemon lacks permission to signal it. Both cases are
  reported as alive, because a permission failure proves a process
  exists even though it cannot prove which one.
- **Undecided**: an unexpected error from the liveness check itself, one
  the check cannot classify as either of the above.

Alive and Undecided are both treated as "do not delete." The scan never
removes a pidfile, a socket, or a lock unless it can prove the owning
process is gone. When it cannot prove that, it leaves the artifact in
place, logs a warning explaining why, and moves on.

The socket probe has its own, stricter version of this same posture: an
undecidable dial failure does not just skip cleanup for the socket, it
aborts the whole scan with an error, so the daemon does not proceed to
load config and start up in an uncertain state. What the operator sees
in this case is the daemon failing to start with an error naming the
socket path and the underlying dial failure. This error carries no
specific taxonomy classification and surfaces as an internal error; the
fix is to investigate why the socket path is not dialable (commonly a
permissions problem on the socket's parent directory) rather than to
retry blindly.

## Pid recycling

A pidfile or a lock record naming a pid that is currently alive is never
touched, even if the scan has reason to believe the recorded pid belongs
to a long-dead process. The operating system's own report of "this pid is
alive right now" is trusted over any other evidence, because treating a
live pid as stale risks two failure modes at once: deleting a lock or
pidfile a running daemon still depends on, and in the worst case
signaling or interfering with an unrelated process that has since reused
the same pid number. The scan has no way to distinguish "the original
daemon, still running" from "an unrelated process that reused this pid"
without additional bookkeeping it does not do, so it treats both alike:
alive means untouched.

## Recovery event

When the scan finds and cleans up at least one stale artifact, it
constructs a recovery event and publishes it to the daemon's event bus
(if one was supplied at startup; publishing is skipped, not faked, when
none was). Its fields:

| Field | Meaning |
|---|---|
| `stale_pid` | `true` if a stale pidfile was found and removed. |
| `stale_socket` | `true` if a stale socket file was found and removed. |
| `stale_locks` | List of advisory-lock IDs that were released as orphaned. |
| `recovered_at` | Timestamp of the scan, from the daemon's injected clock. |

A clean startup, with nothing stale found, produces no event at all. No
event is published, and none is returned to the caller. Absence of an
event is the signal that startup was clean; it is not an omission to
watch for.

The scan is idempotent: running it again immediately afterward, against
the now-clean state it just produced, finds nothing to do and again
produces no event.

## Windows

The daemon is not currently supported on Windows. The recovery scan
detects this platform at its very first step and returns immediately,
logging a warning that the unix-socket probe was skipped for this
platform, without touching a pidfile or any lock. An operator running the
daemon on Windows sees this warning in the log and no further recovery
activity; nothing downstream of the scan on Windows depends on it having
done any cleanup.

## Operator troubleshooting

**Startup fails immediately, before config or logging come up.**
This is either the live-daemon case or the undecidable-socket case, both
from the socket probe step, since that step runs first and aborts on
either outcome.

- A message naming a live daemon already answering the socket means
  another daemon process is already running against this socket path.
  Check for a running `cascade` daemon process before doing anything
  else; do not delete the socket file by hand while a live daemon holds
  it.
- A message describing the socket probe as undecidable, naming the
  socket path and an underlying dial error, means the daemon could not
  tell whether a process is listening or not. Check filesystem
  permissions on the socket path and its parent directory. Do not delete
  the socket file to work around this without first confirming no daemon
  process is actually running; if the probe is undecidable because of a
  transient permission problem, fixing that problem lets the next start
  attempt classify the socket correctly on its own.

**Startup succeeds but a pidfile or lock warning appears in the log.**
Look for warning lines describing a pidfile or lock as present but not
confirmed dead. This means the daemon found leftover state naming a pid
it could not prove was gone, and left it untouched rather than guessing.
If you are confident no such process is running (for example, you have
independently verified the pid does not exist), the leftover file can be
removed by hand once you are certain no daemon is running.

**Telling "another daemon is running" apart from "stale artifacts need
cleaning."** The socket probe answers this directly and is checked first:
if it finds a live listener, the daemon is running, full stop, and no
cleanup runs. If the probe finds no listener, anything left behind
(pidfile, socket file, locks) is by definition not backed by a running
process holding the socket open, and the recovery scan handles it
according to the rules above.

**Log lines to look for**, all emitted at warning level:

```
runtime: removed stale socket file
runtime: pidfile present but unparsable; skipping removal
runtime: pidfile present but owner pid is not confirmed dead; skipping removal
runtime: removed stale pidfile
runtime: recovery: could not list orphaned locks; skipping lock cleanup
runtime: recovery: failed to release orphaned lock
runtime: recovery: released orphaned advisory lock
platform-unsupported: unix socket probe skipped
```

Each of these lines carries structured fields (path, pid, lock ID, or
error) alongside the message; consult those fields for the specifics of
what the scan found.

## Upgrade in place

`cascade daemon restart` is the only user-facing trigger for an upgrade.
There is no separate `daemon.upgrade` verb and no signal a user sends by
hand. Package home: [`internal/daemon`](../internal/daemon)
(`upgrade.go`, `upgrade_conntracker.go`).

The upgrade-in-place engine is only wired into a running daemon's
termination path for a real release build: an unreleased (`dev`) build's
embedded hash never matches any on-disk binary by construction, so
enabling this path unconditionally would make every ordinary shutdown
signal on a dev build attempt a drain-and-relaunch instead of a clean
exit. Every build this repository's own CI and test suite runs is a dev
build, so this guard is what keeps `cascade daemon stop`/`restart`
working during development at all; it costs a release build nothing,
since a release build's hash is real and CheckSkew works as designed.

When a termination trigger reaches a running daemon carrying a real
release build hash, it compares the on-disk `cascade` binary's SHA-256
hash against the hash embedded in the running process at build time. If
the two match, this is a logged no-op and the daemon shuts down and
restarts the ordinary way (stop, then a fresh spawn). If they differ, the
binary on disk has been replaced since this daemon started, and the
daemon drains and re-execs itself in place instead:

1. Stop accepting new IPC connections; a connection accepted after this
   point is refused rather than accepted and silently dropped.
2. Wait, up to the configured `shutdown_grace`, for any in-flight
   connection to finish. Anything still running once grace elapses is
   force-closed.
3. `exec()` the on-disk binary with the same arguments and environment.
   The process keeps its PID; no new process is spawned and no window
   opens where nothing is listening on the socket.

If the `exec()` call itself fails (the new binary is missing or not
executable, for example), the daemon has already drained and cannot go
back to serving. It falls through to its normal shutdown path instead
of trying to limp on with a closed listener. The pidfile and socket are
cleaned up exactly as they are on any other clean exit, so `cascade
daemon start` (or the crash-recovery scan) finds a clean, restartable
state rather than a half-alive process.

**Resume leg (allowed-fail, W1).** Before a drain begins, the daemon
writes a best-effort checkpoint of its current write-ahead-log position.
On the next startup this checkpoint is read back and logged if present;
if it is absent or unreadable, startup proceeds as a clean start with no
error. Actually resuming in-flight session state from that checkpoint is
not implemented yet: this leg only records and reports the position.

**Windows.** The daemon does not run on Windows at all (tier-2), so
there is nothing to upgrade in place there; every daemon verb, including
`restart`, refuses with the same typed unsupported-platform error.
