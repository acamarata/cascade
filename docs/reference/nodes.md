# Node agent

`cascade node serve` runs the node agent: the same binary, running in
node-serve mode, hosting the node-side JSON-RPC surface on a unix socket
under the data directory (`<data_dir>/nodes/node.sock`). The controller
reaches this surface over an ssh tunnel — see Transport below.

## Identity

On first run, a node generates its own Ed25519 identity (`internal/nodes`)
and stores the private key exclusively in the OS keystore (never in a
config file, journal, payload, or environment variable). The public
identity is persisted at `<data_dir>/nodes/self_identity.json` so restarts
reuse the same identity rather than generating a new one.

## Enrollment

`node.enroll` is mounted on the node's own RPC registry and admits a peer
identity into this node's device-record store, following the mutually
signed transcript protocol (`internal/nodes`, S-36.T1). `node serve` is
mounted by S-36.T2; every other verb (`enroll`/`list`/`status`/`drain`/
`remove`/`rotate-key`/`revoke`) is mounted by S-36.T4, documented in
§CLI below.

## CLI (S-36.T4)

`cascade node <verb>` is a thin surface over `internal/nodes`' own
methods. Every verb refuses on Windows tier-2 (`internal/nodes.RefuseOnGOOS`,
the same refusal `node serve` uses).

- **`enroll <user@host> --trust-tier {worker-trusted|controller}`** ⚠
  (elevated). `--trust-tier` is REQUIRED — omitting it is a typed refusal
  before anything else runs, never a default. `--host-key-fingerprint
  <sha256>` supplies the out-of-band override `KnownHosts.Verify` accepts.
  The command performs a real ssh dial to `user@host` to observe and
  verify/pin the host key, then admits the peer via the real `EnrollNode`
  operation. The node-supplied half of the handshake payload
  (`node_id`/`node_pubkey_b64`/`node_signature_b64`) is read as JSON from
  stdin, or from `--payload-file`: this CLI does not fetch it over the
  wire (see the contradiction noted in the ticket journal — the existing
  ssh `Session` has no remote-dial primitive to do so, and adding one is
  outside this ticket's files_scope).
- **`list`** ✦ / **`status <id>`** ✦ (read-only, never elevated): render
  the device record joined with derived liveness. `status`'s `tunnel`
  field is currently always `"unknown (no in-process tunnel session)"` —
  a fresh CLI process has no access to another process's in-memory
  `Manager`; see the ticket journal for the full contradiction against
  this doc's own Transport section.
- **`drain <id>`**: marks the device record drained (not accepting new
  work) via `RecordStore.Drain`. Idempotent; preserves every other field.
- **`remove <id>`** ⚠ (elevated, already in `internal/rpc`'s
  elevation table): deletes the device record outright.
- **`rotate-key <id>`** ⚠ / **`revoke <id>`** ⚠ (elevated; enforced
  directly by the CLI rather than via the shared elevation table, which
  does not yet list these two verbs — see the ticket journal). `rotate-key`
  rotates THIS machine's own local identity (the machine invoking it must
  hold the private key for `<id>` in its own keystore); `revoke` calls
  `RecordStore.Revoke`, moving the current key into the record's
  `RevokedKeys` set.
- **`upgrade [NODE_ID] [--all] --artifact <path> --signature <path> [--pubkey <path>]`**
  ⚠ (elevated, already in `internal/rpc`'s elevation table): see
  [Version management](#version-management-s-36t5) below.

## Version management (S-36.T5)

Fleet version management (§D-17): enroll-time and rollout binary
provisioning over ssh, with minisign verification post-transfer, a
same-minor version negotiation window, and the node-managed install
channel deferral.

**Provisioning (`internal/nodes.Provision`).** Ships a signed release
artifact to a node over ssh, or verifies the node's preinstalled version
stamp and skips (idempotent convergence: a second run against an
already-current node reports `Skipped`, never re-transfers). The
artifact's minisign signature is verified BEFORE any dial or transfer —
an invalid or missing signature refuses outright, and the node is never
contacted. After transfer, the receiving end's actual bytes are
checksummed and compared against a digest computed locally over the
already-verified artifact before it was sent; a mismatch (an interrupted
or corrupted transfer) refuses the install and removes the partial
upload, leaving the previous binary untouched. Install itself is an
atomic `chmod +x && mv -f` into place: nothing is ever removed before its
verified replacement is fully staged, so a crash or refusal at any point
before that rename structurally cannot leave a node without a working
binary.

**Minisign verification (`internal/nodes.VerifyMinisign`, §D-32).** A
real parser and verifier for minisign's own armored signature-file
format (both the legacy `Ed` direct-Ed25519 mode and the default `ED`
BLAKE2b-512-prehashed mode), verified byte-for-byte against the real
`minisign` 0.12 CLI (`internal/nodes/testdata/minisign/README.md`
records provenance). This is the SAME signature format the §D-16 release
train's `.goreleaser.yaml`/`release.yml` already produce and verify for
end-user checksum verification — node provisioning checks the identical
artifact class through the identical wire format, never a second dialect.

**Version negotiation (`internal/nodes.NegotiateVersion`, §D-17).**
Controller and node versions must share the same major.minor window.
`Provision` refuses to ship an artifact outside its own controller
version's window before ever dialing a node (a controller must never
push a binary it could not itself negotiate with). `WouldFallOutOfWindow`
surfaces an advisory `skew_warning` on a node whose pre-upgrade version
had already drifted out of window — never a refusal, just visibility.
The node's build stamp rides `CapabilityReport.BuildVersion` (S-36.T2's
heartbeat report).

**`node upgrade [--all]`** routes through the same elevation flow every
other elevated verb in this package uses (`ELEVATION_REQUIRED` without a
proof; `CASCADE_NO_INPUT=1` hard-errors rather than prompting; refused
outright on Windows tier-2; CLI+RPC-with-auth only, never MCP-exposed).
`--all` rolls every enrolled, non-drained node with a configured
`RouteConfig`; a node with no route is reported `Skipped` with a reason,
never silently omitted, and one node's failure never aborts the rollout
for the rest — every node gets its own reported outcome. The daemon also
exposes `node.upgrade` over RPC (`internal/daemon/node_upgrade_rpc.go`),
reading a staged artifact/signature from `DataDir()/nodes/upgrade/` (an
operator, or a future release-fetch ticket, stages the files there before
calling it).

**Node-managed install channel (§D-33, `internal/nodes.DeferSelfUpdate`).**
A binary stamped `install_channel=node-managed`
(`internal/buildinfo.ResolvedInstallChannel`) is under this controller
rollout, not a self-directed update path. `DeferSelfUpdate` reports true
for that one channel value; AA/S-55.T7's `cascade self-update` (not yet
built) is the consumer named in `internal/build/testonly-allow.json`.

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

## Transport

The controller reaches an enrolled node over a managed, reconnecting ssh
tunnel (`internal/nodes/tunnel.go`, `reconnect.go`; S-36.T3). The
controller dials `<user@host>` (the same ssh access enrollment used) as
the ssh client; the tunnel never introduces a new credential class or new
wire format — it carries the existing node RPC framing (Epic D) and the
S-36.T2 heartbeat frame over it.

**Lifecycle.** A `Tunnel` moves through three states: `down` ->
`reconnecting` -> `up`. `Manager.Start` is idempotent per node id — a
second `Start` for a node already running returns the existing tunnel,
never a duplicate connection or forward. Every transition is emitted as
an event (`node.tunnel.reconnecting` / `.up` / `.down` /
`.host_key_refused` / `.reconnect_exhausted`) and is readable live via
`Manager.State(nodeID)`.

**Host-key verification (R-21.220).** Every connect attempt — the first
and every reconnect alike — verifies the presented ssh host key against
the S-36.T1 `known_hosts` store and the fingerprint pinned in the
enrollment transcript, through the identical `KnownHosts.Verify` call
each time. An unknown key on first contact, or a key that has changed
since it was pinned, is refused as the typed `ErrHostKeyUnknown` /
`ErrHostKeyChanged` error (`KindPermissionDenied`) and is terminal: the
tunnel sets `down` and never reconnects on it. Re-establishing a tunnel
to a host whose key changed requires an operator to re-pin the key out of
band first (`KnownHosts.Pin` with `force=true`).

**Reconnect.** Every other connect failure (unreachable, connection
refused, auth failure, a mid-stream drop) is transient and feeds a
capped exponential backoff (`ReconnectPolicy`: `InitialBackoff` doubling
up to `MaxBackoff`), driven by the injected `Sleeper`/`Clock` so the
schedule is deterministic under test — never a real sleep. The loop is
bounded both by `ReconnectPolicy.MaxAttempts` (optional) and by the
caller's `context.Context`; canceling that context (`Manager.Stop`, or
daemon shutdown) tears the tunnel down cleanly, mid-backoff or
mid-session alike.

**Carried channel (the D-24 ssh-forwarded-socket pattern).** Once up, the
tunnel remote-forwards a unix socket on the node's filesystem (ssh -R
streamlocal) back to the controller process; connections a node-side
process makes to that socket arrive at the controller and are piped,
byte for byte, to the controller's own local RPC socket. This is how
S-36.T2's heartbeat sender (`NewTunnelHeartbeatSender`, the wire-delivery
sink deferred to this ticket) reaches the controller: it POSTs the
existing `node.heartbeat` JSON-RPC envelope over the tunnel's local end,
unchanged.

**Later consumers.** This transport is the substrate, not the payload,
for: S-36.T5's over-ssh binary provisioning, S-37.T2's remote dispatch /
journal streaming, and S-38.T1's chunked sync transfer. None of them are
implemented by this ticket; each rides the same tunnel once it lands.

**`node status <id>` fields.** The connection-state surface S-36.T4
mounts reads `Manager.State(nodeID)` directly: `state` (`down` /
`reconnecting` / `up`) and whether the node has a registered tunnel at
all. No additional persisted field is required — state is live, in
Manager's own registry, matching Liveness's derived-not-persisted
convention above.

**Real-counterpart testing (Art.2).** Establish/host-key/forward/
reconnect are exercised against a REAL sshd, never a self-authored
dialect: the tagged `integration` lane spawns a loopback OpenSSH server
at test time (`internal/nodes/tunnel_test.go`; provenance in
`internal/nodes/testdata/README.md`) and wires the CI job
`node-tunnel-real-sshd`.

## Placement (S-37.T1)

Placement answers one question: given a unit of work, which enrolled nodes
are ELIGIBLE to run it? It decides a set, never a choice — scoring the
eligible set by cost, health and lane affinity is the conductor router's
job, and the two are kept apart deliberately so neither can quietly become
the other.

The requirement surface is `cascade run --require node.<capability>=true`
(an open, machine-advertised vocabulary, unlike the closed three-key lane
vocabulary alongside it). A request that names no node capability is not a
placement question at all and never reaches this engine.

### Eligibility dimensions

Four filters, applied most-restrictive-first so the exclusion an operator
is shown is the most fundamental one true of that node, not whichever check
happened to run first:

| Order | Filter | A node is excluded when |
|---|---|---|
| 1 | Sensitivity / trust tier | the work is local-only, or the node's tier does not clear the work's sensitivity |
| 2 | Drain | an operator marked the node as not accepting new work |
| 3 | Presence | presence is anything other than `reachable` |
| 4 | Connection | no tunnel to the node is up |
| 5 | Capability | the node does not report every required capability |

Capability matching is exact. A capability name is a term from a shared
vocabulary, not a prefix or a pattern, so a node reporting `docker-ce` does
not satisfy a requirement for `docker`.

A node's capabilities come from `DeviceRecord.LastReport` — the report
carried by its most recent **verified** heartbeat. A node that has never
sent one advertises nothing and satisfies no capability requirement.

### The trust-tier placement matrix

Tiers are compared by ordered rank: `controller` (2) > `worker-trusted`
(1) > `paired-device` (0).

| Work sensitivity | Eligible nodes |
|---|---|
| local-only | **none** — the controller machine is the only place it may run |
| restricted | `rank(tier) >= rank(worker-trusted)`; a paired device is never eligible |
| normal | any node whose tier this build recognizes |

Two readings fail closed rather than fail open:

- An unresolvable or unrecognized sensitivity resolves to **local-only**.
  Guessing wrong on a classification is the case that leaks work off the
  controller machine, so it resolves the way that cannot.
- Local-only work excludes every candidate unconditionally, whatever tier
  string a record happens to carry. Every candidate is an *enrolled* node
  and therefore not the controller machine running the engine; routing
  local-only work to a remote node because its stored tier read
  `controller` is precisely the leak the classification exists to prevent.

`policy.external_allowed = false` on a request forces local-only placement
regardless of its declared sensitivity, so a caller that both forbids
leaving the controller machine and demands a capability only another
machine can advertise is refused rather than half-honoured.

### No eligible node

An empty eligible set is always an error, never an empty list with no
error. Work classified to run somewhere specific fails loudly when it
cannot, rather than silently falling back to the controller machine — the
caller must not be able to read "nowhere to run this" as success.

The error is `KindUnavailable` (the request was well formed and permitted;
there is simply nowhere to run it right now) and names the aggregate first,
then the breakdown by reason, then per-node detail bounded so a large fleet
produces a readable message:

```
nodes: no enrolled node is eligible for this work (4 considered, 0 eligible);
required capabilities: browser; by reason: 2 drained, 1 not-connected,
1 missing-capability; node-a: node is drained and is not accepting new work; ...
```

An engine wired without a connection source places **nothing**, and says so
in those words (`(no connection source wired)`) rather than reporting every
node as merely disconnected — two very different bugs.

### Where it is consulted

`internal/conductor`'s router runs the eligibility consult ahead of its
five lane filters: "is there a machine for this at all" is the more
fundamental question than "which lane serves it", and a request nothing can
host should not cost a registry read. The router holds no copy of the
eligibility rules; it supplies the inputs and propagates the answer.

A router with no placement engine wired **refuses** a request that names
node capabilities. It does not route it as though the requirement had not
been typed: a caller cannot tell a satisfied requirement from an ignored one
by looking at a successful response.

## Remote dispatch (S-37.T2)

The controller ships work to an enrolled node over git: it pushes a work
branch, the node checks it out into a dedicated worktree, runs, and pushes
its results back. Placement (S-37.T1) decides which nodes are eligible
before any of this begins; a node that fails placement is never reached.

**Direction.** The ssh tunnel is a *reverse* forward: the node connects out
and its connections arrive at the controller's own RPC socket. The node is
therefore the RPC client and the controller the server, so a dispatch is a
rendezvous rather than a call to the node. The controller publishes an
attempt and waits; the node claims it, runs it, and reports back. Three
verbs live on the controller for the node to call:

| verb | who calls it | what it does |
|---|---|---|
| `node.dispatch` | the controller's own run path | ships one dispatch |
| `node.dispatch.claim` | the node | takes the work placed on it, once |
| `node.dispatch.report` | the node | returns its signed result frame |
| `node.dispatch.journal` | the node | streams journal records back |

The node also answers `node.dispatch.execute` on its own socket.

**Naming.** Branch `dispatch/<dispatch-id>/<attempt>`, worktree
`<repo>/.cascade/worktrees/dispatch-<dispatch-id>-<attempt>`. Both are
deliberately disjoint from the jobs namespace (`job/<id>`). The worktree is
removed on any terminal outcome.

**Fencing.** Every attempt carries a monotonic number, and it is inside the
*signed* payload — so a partitioned node cannot relabel a stale result as
the current attempt without invalidating its own signature. Results, pushes
and journal records from a superseded attempt are all refused with
`ErrStaleAttempt`. A retry after a dropped attempt lands on its own branch,
so a node that is still alive and pushing to the old one cannot write into
the live attempt's work.

A dispatch id names one dispatch. On a terminal outcome the controller
forgets its attempt (the register is not allowed to grow without bound), so
reusing a *completed* dispatch id restarts numbering at 1 and reuses that
ref. Mint a fresh id per dispatch.

**Deduplication.** Every dispatched action carries a stable action id. The
node records it durably — written and fsynced — *before* the action runs,
so a redelivery after a crash is refused rather than re-run. A duplicate is
answered with outcome `refused`, not an error: the work already happened,
and an error would invite the retry that duplicates the side effect. An
action declares whether it is idempotent; an ambiguous outcome on a
non-idempotent action is held in `unknown-outcome` for a human decision and
is never auto-re-queued.

**Credentials (§D-11).** Static API keys are NEVER shipped to a node —
those lanes relay their calls through the controller, and the dispatch
result reports `relayed` so a caller can tell a node that was trusted with
a token from one that was not. Lanes that support short-lived credentials
get a per-dispatch scoped token bound to {job, node, audience, verbs,
expiry <= 1h}, delivered only over the control channel to the node's
in-memory broker. A token is never written to git, journals, payloads,
argv, the environment or any persisted store, and does not survive a node
crash. As defence in depth the payload is *also* scanned: anything shaped
like a long-lived provider key is refused before the push leg runs.

**Egress.** The node-dispatch egress class is registered at
`internal/nodes`' own package init, so every outbound payload — git refs,
RPC frames, journal records, token handoffs — transits the substitution and
sensitivity pass.

**Configuration.** Remote dispatch reads two knobs from `[nodes]`:

```toml
[nodes]
dispatch_repo_root = "/srv/work"          # controller-side repository
dispatch_remote    = "/srv/remote.git"    # the remote both sides reach
```

Either one missing REFUSES the dispatch. There is deliberately no fallback
to the controller's own checkout: running remote work against the
operator's working tree is the one outcome this must never produce
silently.

**Failures.** Unreachable node, failed push or fetch, a tunnel dropped
mid-dispatch, and a worktree that could not be prepared are each their own
typed error. A dropped tunnel is reported as such rather than as an
unreachable node, because the branch was already pushed and the work may be
running — telling those apart is what S-37.T3's recovery depends on. There
is never a silent fallback to running the work locally.

## Travel profile

`[nodes].travel` (bool, default false) marks THIS controller machine as
one that regularly leaves its home LAN (08-INIT-CONFIG-SPEC.md §3
Round-16 schema). It has two effects.

**Route fallback for presence (R-16.37 §Nodes).** Each enrolled device
record can carry an optional `Route` — the same `<user@host>` ssh access
captured at enrollment (S-36.T1), reused verbatim, never a second
transport. The presence prober (`internal/nodes/prober.go`, S-72.T2)
tries this route only once a node's direct probe has already failed
`ProbeMissThreshold` (3) consecutive times AND the record carries a
configured route — never on the first miss, and never for a node with no
route to try. A route that answers classifies the node
`remote-via-route` instead of `unavailable`; a configured route that
does NOT answer, or no route at all, leaves the node on the ordinary
path to `unavailable`. These are deliberately different answers at
`RouteReachable`'s own level (`internal/nodes/travel.go`): "not
configured" is a typed error distinct from "configured but did not
answer" (a real negative, never an error) and from "answered" (true) —
the bool-shaped `RouteChecker` interface the prober calls collapses the
first two to `false`, but the finer distinction is real and tested.
`RouteReachable` dials through the identical tunnel `Dialer`
(`tunnel.go`, S-36.T3) the direct transport already uses; the ssh
private key never leaves `NodeKeystore` custody, and `RouteConfig`
itself carries only `User`/`Addr` — no credential field exists for a
secret scanner to ever need to flag.

**Advertise/browse precedence (R-21.198).** `travel=true` disables mDNS
advertisement unconditionally, regardless of `[nodes].scan_lan` — travel
wins the precedence. Browsing is independently confined to
`[nodes].discovery_networks`: an empty allowlist (default) means no
network qualifies, fail-closed, whether or not travel is set. The
mDNS advertiser/browser itself (`discovery.go`) is not yet built in this
tree (S-72.T1 deferred it); the travel⇒advertise-off decision lives as a
package-private seam in `internal/nodes/travel.go` for that file to call
once it lands.

**Hot reload.** `[nodes].travel`/route parsing is pure and idempotent —
applying the same config twice yields the same `Section` (06 §5.9). The
whole `[nodes]` section has no hot-reload registration hook in the tree
at all yet (`config.go`'s own doc comment records this pre-existing gap,
predating this ticket); this ticket does not reintroduce or worsen it.

## Failure semantics (S-37.T3)

A node can disappear mid-dispatch. The work it was running must not
disappear with it, and must not silently run twice either.

**The three loss signals.** Liveness is three-state — `reachable`,
`unavailable`, `unknown` — and only `reachable` is evidence that a node is
there. Both of the others are re-queue signals, `unknown` included: it is
what a controller with no news reads, and treating an absence of bad news
as health parks work on a machine that is gone. The tunnel state and the
ship leg's own typed errors are read the same way. A dispatch is considered
lost when any of the three says so, and the most specific signal is the one
reported, because that is the one an operator can act on.

**Re-queue re-enters placement.** The replacement target goes back through
the same placement engine with the same requirement, so trust tier,
sensitivity, liveness, connection and drain are all re-applied. Recovery is
where bypassing a filter is most tempting — the work is already late — and
where it is least defensible, because the filter in the way is the one
saying this machine must not see this work. Local-only work is still never
dispatched to any node. If nothing is eligible, that is the answer: the
controller does not decide to run it itself.

The node that was just lost is excluded explicitly rather than left to fail
placement. Its device record's liveness is maintained by a different loop on
a different schedule, so a recovery that trusted the record would place the
work straight back onto the machine it just left.

**Re-queue is fenced.** The replacement takes the next attempt number from
the same register the original used, so it runs on its own branch
(`dispatch/<id>/<attempt+1>`) and its own worktree. Results, pushes and
journal records still arriving from the superseded attempt are refused with
`ErrStaleAttempt`. A partitioned-but-alive node cannot race its own
replacement.

**Journal continuity.** The records the lost attempt streamed back are the
resume substrate. The replacement starts after the entity's last published
checkpoint and carries the operation ids already accounted for, so it
continues rather than starting over. The read is deliberately generous — it
may re-deliver an operation the first attempt finished, and it will never
skip one — because a re-delivery is caught by the node's durable dedup and
a skip is not caught by anything.

An unreadable journal is an ERROR, not a cold start. "There is nothing
recorded" and "I could not read what is recorded" produce the same resume
point and mean opposite things, and acting on the first when the second is
true re-runs completed work.

**At-least-once is not unconditional.** Only actions declared idempotent
re-queue automatically. For one that is not, both automatic choices are
destructive — re-running may duplicate an external effect, abandoning may
lose work that succeeded — and nothing in the system can tell which. So it
is HELD in `unknown-outcome` for a person, surfaced as an attention entry,
and never auto-re-queued or dropped. Holding requires an attention filer:
with none wired the controller refuses rather than holding quietly, because
a held item nobody is told about is a lost one with extra steps.

Durable dedup is what makes the automatic case safe when the replacement is
the SAME node coming back — a restart or a reconnect. It cannot help when
the replacement is a different machine, whose action log is empty. That
limit is exactly why the held state exists.

**The kill-node drill.** `TestKillNodeMidRun` (tagged `integration`, CI job
`node-kill-mid-run`) runs a real `cascade node serve` process against a
throwaway home, has it reserve an action in its durable on-disk log,
SIGKILLs it — no graceful shutdown, nothing runs on the way out — and then
asserts the controller reads the dead socket as a loss, re-queues onto a
healthy spare at a fresh fenced attempt carrying the lost attempt's journal
position, and that the action is refused when it is redelivered to a
restarted node over the same data directory. One side effect, across a
process that was never allowed to clean up after itself.

The cross-machine variant of the same drill is the 06 §7 owner
prerequisite; it gates that drill only, never this one.

### What is wired (S-37.T6)

The recovery path has five collaborators. All five are real on a shipping
daemon as of S-37.T6; before it, two were nil and every loss was reported
rather than recovered.

| Collaborator | Source | What its absence costs |
|---|---|---|
| Candidate set | the enrolled device records | nothing to place on; the re-queue refuses |
| Fencing register | the same one the ship leg mints from | a replacement that races the attempt it replaces |
| Liveness | the heartbeat, three-state | every ship failure reads as a possible loss (fail closed) |
| Tunnel state | the heartbeat — see below | placement places nothing |
| Journal continuity | the dispatch journal store | the re-queue refuses rather than resuming from scratch |
| Attention queue | the fleet attention store | a held outcome refuses rather than being held quietly |

**The tunnel reading comes from the heartbeat, and that is a decision
rather than a shortcut (R-14.274).** Placement wants to know whether a node
is connected. The controller never dials a node: the node dials out and the
session carries a *reverse* forward, so the controller holds no socket it
could inspect and no per-node tunnel object — the tunnel manager lives on
the node side. What the controller does hold is the heartbeat, and the
heartbeat travels over that same tunnel, so its arrival within the timeout
is direct evidence the tunnel was up. The mapping fails closed at the one
place it could go either way: an unknown liveness reads as **down**, never
as *reconnecting*, because "reconnecting" would tell placement a node is on
its way back when the daemon in fact knows nothing.

**One journal and one queue, not two of each.** The continuity reader
replays the same store the dispatch-journal verb appends to, and the
attention filer writes into the same queue `cascade fleet attention` serves.
A second store over either namespace would be a second view of one thing,
and a held dispatch filed into one of them would be invisible in the other.

**The resume point is decided but not yet delivered.** `PlanRequeue`
computes the replacement's resume position from the journal, and the daemon
now really reads it. What does not exist yet is the channel that carries it
to the replacement node: neither the attempt, the claim response, nor the
execute request has a field for it, so a replacement on a *different*
machine still starts from its own empty action log. Tracked as its own
ticket; the durable-dedup case (the same node coming back) is unaffected and
already works.

## Windows

`cascade node serve` refuses unconditionally on Windows: the serve
listener is daemon-class, outside Windows tier-2's binary + headless
one-shot promise (06-FORGE-SPEC §2). The refusal is a typed
`KindUnsupported` error with an actionable message. The heartbeat and
capability-report protocol logic itself is platform-independent and is
unit-tested on every platform. The controller-side tunnel service is
refused the same way (`RefuseTunnelServiceOnGOOS`, S-36.T3), asserted
natively in the windows/amd64 CI lane
(`internal/nodes/tunnel_windows_test.go`); its state-machine and
host-key-verification logic stay unit-tested on every platform. The
travel-profile route check (`RefuseRouteOnGOOS`, S-72.T3) is refused the
same way, with an explicit typed message, for the identical reason: it
reuses the same daemon-class tunnel dial.
