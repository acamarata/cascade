# Daemon startup and crash recovery

This document covers the daemon's startup sequence and its crash-safety
recovery scan: what the scan checks, how it decides an artifact is stale,
and what it does when it cannot decide. Package home:
[`internal/runtime`](../internal/runtime) (`bootstrap.go`, `recovery.go`,
`recovery_scan.go`, `lockfile.go`).

## Startup sequence

`Bootstrap` (`internal/runtime/bootstrap.go`) is the single entrypoint the
daemon's startup path calls into. It runs these steps in order:

1. Resolve paths (`PathProvider`). This establishes the pidfile path
   (`Root()/daemon.pid`) and the IPC socket path.
2. Run the crash-safety recovery scan (`Scan`, described below).
3. Load and validate `config.toml`, running the schema-version upgrade
   frame if needed.
4. Construct the log provider.
5. Start the metrics registry, and the periodic metrics emitter if a
   metrics event bus was supplied.

The recovery scan runs immediately after paths resolve and before
anything else, including config load. This placement is deliberate: the
scan's own socket probe is the daemon's write-arbitration contract's
socket-probe-first rule, and it has to run before any lock on the daemon's
store or advisory-lock state is taken. A scan that ran after lock
acquisition could never clean up the very lock blocking it, since it would
already be blocked waiting on that lock.

The scan's result (a `*RecoveryEvent`, or `nil` on a clean start) is not
consumed further inside `Bootstrap`. If a recovery event was produced, it
was already published to the event bus by the scan itself; `Bootstrap` has
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

This step depends on an injected registry of advisory-lock records. If no
registry is wired in, the step is skipped outright and reported as such
in the code's own comments. No advisory-lock cleanup happens, which is
reported honestly rather than silently pretending to check. Where a registry is present:

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
