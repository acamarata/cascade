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
