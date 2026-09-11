# Node agent

`cascade node serve` runs the node agent: the same binary, running in
node-serve mode, hosting the node-side JSON-RPC surface on a unix socket
under the data directory (`<data_dir>/nodes/node.sock`). The controller
reaches this surface over an ssh tunnel; this ticket does not implement
the tunnel transport itself.

## Identity

On first run, a node generates its own Ed25519 identity (`internal/nodes`)
and stores the private key exclusively in the OS keystore (never in a
config file, journal, payload, or environment variable). The public
identity is persisted at `<data_dir>/nodes/self_identity.json` so restarts
reuse the same identity rather than generating a new one.

## Enrollment

`node.enroll` is mounted on the node's own RPC registry and admits a peer
identity into this node's device-record store, following the mutually
signed transcript protocol (`internal/nodes`, S-36.T1). The enroll/list/
status/drain/remove CLI verbs are mounted by a later ticket (S-36.T4); this
ticket mounts `node serve` only.

## Heartbeat and capability report

A node that has enrolled with a controller (a `controller_binding.json`
file under `<data_dir>/nodes/`, written by the enroll CLI) sends a signed
heartbeat frame to that controller on a deterministic schedule (every
`DefaultHeartbeatInterval`, 30s by default). Each frame carries:

- a strictly monotonic sequence number and the controller-issued
  enrollment id, both checked by the controller before the frame is
  trusted (R-21.221);
- a capability report: the node's declared capability set (e.g. `browser`,
  `docker`, `4hr_runtime`) plus a K12 hardware-envelope classification
  (`minimal`/`balanced`/`performance`), bounded in size so a misbehaving
  node cannot exhaust the controller.

The controller refuses a frame whose node id does not match the verified
signer, whose sequence is not strictly increasing, or whose signing key has
been revoked. A capability report is never trusted for an authorization
decision: it cannot widen a node's trust tier.

## Liveness

Liveness is a three-state result: `reachable`, `unavailable`, `unknown`.
`unknown` is the fail-closed default — a node that has never heartbeated,
or whose most recent heartbeat is older than the heartbeat timeout
(`DefaultHeartbeatTimeout`, 90s by default), reports `unknown`, never a
stale `reachable`. `unavailable` is reserved for a controller-observed
dispatch failure, set by later dispatch/re-queue logic.

## Doctor

`internal/nodes.NewHealthCheck` implements the `nodes` doctor check
(R-14.91): it lists every enrolled device record and reports how many are
currently reachable. As of this ticket it is not yet registered in
`cascade doctor`'s check registry — that registration point
(`cmd/cascade/doctor_mounts.go`) is outside this ticket's files_scope; see
`internal/build/testonly-allow.json`'s `internal/nodes.NewHealthCheck`
entry for the tracked follow-up.

## Windows

`cascade node serve` refuses unconditionally on Windows: the serve
listener is daemon-class, outside Windows tier-2's binary + headless
one-shot promise (06-FORGE-SPEC §2). The refusal is a typed
`KindUnsupported` error with an actionable message. The heartbeat and
capability-report protocol logic itself is platform-independent and is
unit-tested on every platform.
