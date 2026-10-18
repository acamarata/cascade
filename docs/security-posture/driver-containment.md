# Driver containment

What the daemon does around each external agent driver spawn, and what that
does not stop. The egress proxy itself is described in
[driver-egress-proxy.md](driver-egress-proxy.md); this page covers how a spawn
composes it and the process sweep that ends a lease. Read the limits first.

## Limits

| Control | What it is | What it is not | Deferred item |
|---|---|---|---|
| Per-spawn egress proxy | Advisory routing: a harness that honours `HTTPS_PROXY` reaches only listed destinations, and every decision is journaled. | A firewall. A driver can open a direct connection. | `DEF-P2-driver-netns` (per-driver network namespace) |
| Lease-marker sweep | Kills the processes that still carry the spawn's marker when the lease ends, and fails closed if one survives. | Tool isolation. A process that scrubs or rewrites its environment leaves the sweep's reach. | `DEF-P2-driver-tool-isolation` |

## Per-spawn proxy

`StartSpawnProxy` starts one proxy for one spawn over that driver's
`[agents.egress.<driver-id>]` list. A driver with no table gets a nil list and
every destination is denied. The caller puts the proxy's environment in the
child and closes the proxy when the lease is released; after the close a
connection to the old port fails.

`ProxyJournal` binds the proxy's journal to the redacting audit log:

- each decision row is one `policy.decide` audit event with actor
  `agent-driver:<id>`, action `agent.egress.connect`, verdict allow or deny,
  the reason as the outcome, and an explain body of destination, job id and
  phase;
- each dial is an effect: the intent is written before the dial
  (`agent.egress:<job>:<n>`), the confirm after it, and a failed dial or a
  forbidden resolved address fails the effect;
- if the effect log cannot record the intent, the journal returns the error and
  the proxy refuses with `journal-unavailable`; it never dials without a
  recorded intent. An intent that committed while its notification failed is
  closed as failed, so recovery does not call a dial that never ran
  unknown-outcome. A decision row (a refusal, or the end of a tunnel) that the
  audit sink cannot append is returned to the proxy, which cannot turn a
  refusal into an admission. In the daemon both writers are the one audit log,
  so they fail together;
- the confirm and fail writes are not cancelled by the proxy closing, so a
  close racing a finished dial still settles its effect.

A process killed between the intent and the confirm leaves a pending effect.
Recovery marks it unknown-outcome; the key can never begin again, so the dial
is not repeated.

The journal records destinations and decisions only. It never reads or
records tunneled bytes.

## Lease marker and sweep

Each spawn gets a fresh marker from `NewLeaseMarker`: `clm_` plus 32 hex
characters from `crypto/rand`. The spawn sets `CASCADE_LEASE_ID=<marker>` in
the child environment, so every descendant that inherits the environment
carries it, including one that called `setsid` and was reparented.

`SweepLease` runs at lease end:

1. List this uid's processes. Linux reads `/proc/<pid>/environ`; macOS reads
   `kern.procargs2`. Windows has no portable way to read another process's
   environment without CGO, so the sweep returns an unsupported error there.
2. Keep the processes whose environment holds the exact entry
   `CASCADE_LEASE_ID=<marker>`. A match is one whole entry, never a substring:
   `CASCADE_LEASE_ID=clm_ab` does not match marker `clm_a`. On macOS,
   `kern.procargs2` pads the exec path to 8 bytes, so any NULs beyond that padding are leading empty arguments and the
   parser starts the environment exactly `argc` strings in; an argument that
   merely equals the entry is not matched. If the padding is shorter than the
   kernel's, the layout is not the measured one and the parser accepts every
   string the environment could start at, so an evader is never missed.
3. Signal only the process that was checked. Linux opens a pidfd, re-reads the
   environment, then signals through the pidfd, so a recycled pid cannot be
   hit; a failure to open the descriptor (for example out of file descriptors)
   fails closed, and only a kernel or sandbox with no pidfd at all (before 5.3,
   or a seccomp filter) falls back to a check then a signal by pid, which
   narrows the window without closing it. macOS has no pidfd and cannot pin a
   process: it checks the owner, stops the pid, re-reads the environment on the
   stopped process, then kills it (or resumes it if the entry is gone; a process
   that was already stopped is left stopped). Linux also confirms `/proc/<pid>`
   is still owned by this uid before it signals. The kernel itself refuses a
   signal to another uid's process unless the sweeper is privileged (root or
   `CAP_KILL`); a privileged sweeper
   cannot stop a child that changes uid between the check and the signal, so
   the daemon should not run the sweep as root. That
   leaves only a different process killing and reaping the stopped target
   inside one read while a new process takes the pid; the blast radius is the
   same uid. If the daemon dies between the stop and the resume, a stopped
   process stays stopped until something resumes or kills it.
4. List again. The sweep returns success only when that list is empty. A
   survivor after the last round is `ErrLeaseSweepIncomplete`.

A process that cannot be inspected for a reason other than "gone", "zombie" or
"permission denied" fails the sweep closed (`KindUnavailable`), never a clean
re-list. A process denied to the sweeper is a residual, below.

The sweep never signals another uid's process, pid 1 or itself, and never
kills by process name or by process group alone. A marker that is not in the
`NewLeaseMarker` shape is refused, so an empty marker cannot widen the match.

### Residual: macOS hides the environment of system binaries

On macOS `kern.procargs2` returns no environment, and no error, for
Apple-signed system binaries (`sh`, `bash`, `zsh`, `sleep`, `tail`, `git` were
measured). A descendant that is one of them carries the marker but the sweep
cannot read it, so `bash -c 'tail -f log &'` can outlive the lease while the
sweep reports success. The sweep catches the driver's own processes and any
descendant that is not an Apple-signed binary. A parent-chain fallback (kill
the hidden-environment children of a marked process in the same round) would
shrink this; it is part of `DEF-P2-driver-tool-isolation`, as is the cgroup or
job object that closes the whole class.
`TestDarwinAppleSignedBinaryEnvironmentIsAKnownResidual` pins the measured
behaviour so a kernel that changes it prompts an update here.

### Residual: environment scrubbing and unreadable processes

The marker lives in the process's initial environment block. A driver, or a
tool it runs, that execs with an empty environment, or overwrites that block
in place, is no longer found by the sweep. So is a descendant that makes its
environment unreadable to the sweeper: `PR_SET_DUMPABLE=0`, or a setuid helper
such as `sudo`, whose `/proc` entry is no longer owned by this uid or is denied.
The sweep cannot tell such a process from an unrelated hardened one (a key
agent, for example), and failing on every denied process would fail every
sweep on a normal desktop. The sweep then reports success
about a process it cannot see. Closing this needs tool isolation
(`DEF-P2-driver-tool-isolation`): a cgroup or job object per spawn, or a
sandbox that forbids the exec. `TestLeaseSweepScrubbedChildIsAKnownResidual`
pins the current behaviour so a change to it is deliberate.
