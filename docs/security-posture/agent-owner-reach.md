# Agent owner reach

How far a shell-capable agent session can reach the daemon as the owner, and
what P1 does about it. This page follows ADR-0006 section 3: the caller
principal protects model-facing surfaces against cross-session leakage and
prompt-driven misuse. It is not a boundary against shell-capable code running
as the same uid. Read the limits first.

## Limits

| Control | What it is | What it is not | Deferred item |
|---|---|---|---|
| Owner-peer guard | Refuses an owner-CLI request when the process that opened the socket connection is inside a live agent spawn's tree. | Isolation. A process that left the tree is not recognised and keeps the owner view. | `DEF-P2-agent-owner-reach` |
| Daemon environment hiding (linux) | `PR_SET_DUMPABLE=0` makes `/proc/<daemon>/environ` unreadable to a same-uid process. | A boundary against root, a `CAP_SYS_PTRACE` holder or the kernel. Not available on macOS. | `DEF-P2-agent-owner-reach` |

Same-uid shell code is not contained by either control. Nothing in this page,
in `cascade doctor` or in any other doc may claim that it is.

## The gap the guard closes

Every harness, MCP server, plugin and driver reaches the daemon socket as the
owner uid. A session that holds a principal token is scoped. A session that
drops its token, for example `env -u CASCADE_PRINCIPAL_TOKEN cascade ...`,
makes the CLI send the owner-CLI marker (`Cascade-Client: owner-cli`) with no
principal header, and the daemon would give it the owner view. The socket
check alone (peer uid equals owner uid) cannot tell the two apart.

## What the guard refuses

The daemon records the pid of the process that opened each socket connection
(`SO_PEERCRED` on linux, `LOCAL_PEERPID` on macOS) together with that process's
start time, once per accepted connection. For a request that carries the
owner-CLI marker and no principal token, the daemon first checks that the pid
still names the process that connected (same start time), then asks the
installed guard (`agents.AgentPeerGuard`, installed with
`rpc.SetOwnerPeerGuard`) about that pid. The request is refused with HTTP 403 and
a `permission-denied` error, "agent processes may not act as the owner", when
any of these holds for the peer:

1. Its environment holds `CASCADE_LEASE_ID=<marker>` for a live spawn.
2. Its process group or session is a live spawn's process group.
3. It, or any ancestor within 64 hops, is a live spawn's pid.

The group and session test (2) runs for the peer and for every ancestor in the
walk. Only the peer's own environment is read (1), never an ancestor's.

The guard fails closed. A peer that cannot be inspected is refused: the pid is
unknown (another pid namespace, an unsupported platform), the process is gone or
its pid now names a different process, its environment is unreadable (including
a process that made itself non-dumpable; the environment is read for every
peer once any spawn is live, even when a spawn is registered by pid or group
before its marker is known), or its ancestor chain is
longer than 64, or a live spawn carries no usable identifier (no marker, pid or
group) so it cannot be matched. A peer is admitted only when every check ran and matched
nothing. With no live spawn there is nothing to match, so the owner CLI is
admitted without inspection.

What it does not judge:

- A request that carries a principal token (the token decides its scope). A
  request that carries both headers is refused by the caller admission as
  conflicting, whatever its pid.
- A request that carries neither header: scoped surfaces already refuse a caller
  with no identity.

Which process the kernel names depends on the OS:

- linux (`SO_PEERCRED`): the process that called `connect()`, whoever writes
  afterwards. A descriptor an agent passes to a non-agent (`SCM_RIGHTS`) is still
  the agent's connection and is refused (`TestConnectionJudgedByItsConnector`).
- macOS (`LOCAL_PEERPID`): the last process to do I/O on the connection. The
  daemon reads it at accept and again for every owner-CLI request, and both must
  pass. A request an agent writes is therefore judged as the agent, even on a
  connection a non-agent opened (`TestLeakedDescriptorIsJudgedPerOS`). A
  non-agent that writes on an agent's connection before the daemon accepts is
  judged as that non-agent (`TestConnectionWrittenBeforeAcceptIsJudgedPerOS`);
  that needs a non-agent process to act on the agent's behalf and gives the agent
  nothing it could not get by asking that process.

## Residual reach (disclosed, not closed)

| Residual | Why the guard misses it | Closed by |
|---|---|---|
| A reparented descendant, or any descendant whose ancestor chain passes through a process that left the tree: for example a double-forked, `setsid` process that init or launchd adopted (it may keep the marker), whose own child runs with an empty environment and a new session. | The peer has no marker (only its own environment is read), a new process group and session, and no live spawn or live group in its ancestor chain. It is indistinguishable from a process the owner started. | OS isolation per agent (uid, sandbox or VM): `P2-SEC-06` under `DEF-P2-agent-owner-reach`. |
| macOS environment read: `KERN_PROCARGS2`. | macOS has no per-process switch like `PR_SET_DUMPABLE`. Same-uid code can read the daemon's environment, which holds every provider key under the E.1 authorization. | The same item. |
| A descriptor the owner leaks (linux). | On linux the peer is the connector. An agent that writes on a connection a non-agent opened and left open (a leaked, non-close-on-exec descriptor, or a deliberate hand-over) acts with that process's standing. Go sets close-on-exec by default. macOS judges the process that last used the connection, so it usually refuses this; a non-agent that touches the socket between the agent's write and the daemon's second lookup is judged instead. | Owner discipline; per-agent isolation. |
| macOS marker check blind spot. | The kernel returns no environment for Apple-signed system binaries (`sh`, `curl`, `python3`), so the marker test sees nothing for them. The process-group and ancestor tests still apply. | `DEF-P2-driver-tool-isolation` (see driver containment). |
| A spawn not yet registered. | The live list is the delegation service's. A spawn is covered from the moment its marker is registered; its pid and group are matched once they are filled in. A process running before registration is not covered by any rule. | Register the marker before the child is started. |

Other limits worth knowing:

- Pid reuse: a pid alone can be recycled between accept and the check, so the
  daemon also records the connector's start time at accept and refuses the
  request when the pid now names a process with another start time, or none
  (`TestOwnerPeerGuardRefusesARecycledOrExitedConnector`). What remains is a pid
  recycled between the peer's `connect()` and the daemon's `accept()`, which
  includes the wait in the listen backlog and so grows when the daemon is busy;
  the start time is read at accept, so a process that exited and was replaced
  inside that window would be recorded as the new process. The start time is
  read a second time after the guard returns, so a pid recycled while the guard
  was reading the peer is refused. The same bracket applies to the pid the
  kernel names on the second lookup (macOS), from the moment its start time is
  read; a pid recycled between the lookup and that read is not caught.
- The owner CLI started from a shell an agent opened is refused. That is by
  design.
- The guard covers the daemon's unix socket, in the process that installs it.
  `mcp serve --socket` and `node serve` build their own servers with the plain
  connection context (no peer pid, no guard) in separate processes, so they are
  not covered; neither is any other door to the same uid.

## Hiding the daemon environment (linux)

`agents.HideDaemonEnviron` calls `prctl(PR_SET_DUMPABLE, 0)` and reads the flag
back. After it, `/proc/<daemon>/environ` is owned by root with mode 0400, so a
same-uid sibling gets `EACCES`. It costs:

- No core dumps for the daemon and no debugger attach by a same-uid process.
- Some `/proc/<daemon>/*` entries become root-owned, so the daemon itself can
  no longer open them. Measured on a linux 6.x kernel
  (`TestDaemonEnvironHiddenLinux`): `environ` and `io` are denied to the daemon
  itself; `exe`, `cmdline`, `stat`, `status`, `comm`, `cgroup`, `limits`,
  `maps`, `mountinfo`, `fd` and `cwd` still work. The daemon's code reads
  none of the two (it reads `exe` through `os.Executable`, other processes'
  `cmdline` and the global `/proc/stat` and `/proc/meminfo`), and the test also
  shows that the hidden daemon still serves.
- Agent children are not affected. The flag resets on `execve` of a readable
  binary, so the lease sweep and the owner guard still read their `/proc`
  entries.

If a future daemon change reads another process's entry that this flag blocks,
record which and ask the architect; do not remove the flag.

On macOS the function does nothing. That is the second residual above.

## Wiring

The daemon composition arms its own socket. `buildRPCServer` (the function that
builds the daemon's server) calls `armOwnerPeer`, which:

1. on linux calls `HideDaemonEnviron` once per process, and refuses to start the
   daemon if that fails;
2. installs the guard once per process,
   `rpc.SetOwnerPeerGuard(agents.AgentPeerGuard(daemonSpawns.Live))`, where
   `daemonSpawns` is the daemon's one `agents.SpawnRegistry`;
3. wraps the server's connection context with `rpc.GuardedConnContext`, which
   records the connecting process and marks the connection as judged.

The seam fails closed. On a connection marked this way, an owner-CLI request is
refused when no guard is installed or the peer pid is unknown. A server that
never marked its connections (a test handler, `mcp serve --socket`,
`node serve`) is not judged, even in a process with a guard installed.

The guard only knows the spawns in `daemonSpawns`. Whatever starts an agent
spawn in the daemon registers it there with its lease marker before the child
runs, adds the pid and process group once they exist, and releases it only
after the lease sweep returned nil (a descendant that outlives the leader still
carries the marker). At this tip the daemon starts no agent spawns, so the list
is empty and every owner-CLI peer is admitted without inspection; the
delegation service registers its spawns when it ships (see
[delegation](../delegation.md)).

## Tests

| Claim | Test |
|---|---|
| An agent child with a live marker and no token is refused. | `TestAgentChildDialsAsOwnerRefused` |
| A `setsid` grandchild with the marker removed is refused through its ancestor chain. | `TestAgentGrandchildAfterSetsidRefused` |
| A non-agent keeps the owner view, and the guard sees its real pid. | `TestOwnerCLIFromNonAgentAllowed` |
| An ancestor in a live process group is refused. | `TestAgentPeerGuardRefusesAnAncestorInALiveGroup` |
| An unknown or vanished peer is refused. | `TestOwnerPeerGuardFailsClosedOnUnknownPID` |
| A recycled or exited connector pid is refused. | `TestOwnerPeerGuardRefusesARecycledOrExitedConnector` |
| Token-bearing requests are not judged by pid. | `TestPrincipalRequestsUnaffectedByGuard` |
| The guard installs once. | `TestOwnerPeerGuardInstalledOnce` |
| An armed connection with no guard refuses the owner marker. | `TestOwnerPeerGuardAbsentOnArmedConnectionRefuses` |
| The daemon's own server (built by `buildRPCServer`) refuses an agent child and admits a non-agent child; on linux a same-uid sibling cannot read its environment. | `TestDaemonSocketRefusesAgentOwnerCLI` |
| The environment is hidden on linux and the daemon serves. | `TestDaemonEnvironHiddenLinux` |
