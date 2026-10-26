# Delegation: agent processes and the daemon socket

This page covers how the daemon treats the processes that agent spawns start.
The delegation verbs themselves are documented here when they ship.

## Agent processes may not act as the owner

ADR-0006 section 3 says a request that claims the owner CLI is only as
trustworthy as the process behind the socket. An agent that drops its own
principal token (`env -u CASCADE_PRINCIPAL_TOKEN cascade ...`) sends the same
owner-CLI marker the owner's terminal sends. The daemon therefore judges every
owner-CLI request without a token by the process that opened the connection:

- A process that carries a live spawn's lease marker (`CASCADE_LEASE_ID`), sits
  in a live spawn's process group or session, or has a live spawn's pid in its
  ancestor chain, is refused with HTTP 403 and a `permission-denied` error,
  "agent processes may not act as the owner".
- A process the daemon cannot inspect, or whose pid it cannot resolve, is
  refused too.
- A request that carries a principal token is never judged by pid. An agent
  works through its own token, with the scope it was minted with.

Every agent spawn the daemon starts must be registered in the daemon's
live-spawn registry before the child runs, and released only after its lease
sweep returned nil. A spawn that is not registered is not recognised.

## What this does not stop

The check narrows an agent's reach. It is not isolation. A descendant that left
the spawn's tree (double fork, environment cleared, `setsid`, reparented to
init or launchd) is not recognised. On macOS same-uid code can read the
daemon's environment through `KERN_PROCARGS2`. Both are tracked as
DEF-P2-agent-owner-reach, with OS isolation planned for P2.

Details, residuals and the tests that prove each claim:
[agent owner reach](security-posture/agent-owner-reach.md).
