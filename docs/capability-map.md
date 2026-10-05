# Capability map

This page maps every Cascade capability to the code path that serves it and
says how well that path is proven. The table below is checked by a test
harness, so a row cannot claim more than a passing probe backs up.

## What the table is

Each row ties one capability to one entrypoint. A capability is one id from
`internal/build/testdata/capability-ids.txt`, the single public capability
list. An entrypoint is where a caller reaches the capability: a CLI verb, an
RPC method, a daemon subsystem, a plugin, an MCP tool, or a job or scheduler
consumer. The production path is the chain from that entrypoint through the
composition root to the code that does the work. A capability may have several
rows. Every id in the list needs at least one.

The columns are:

- `capability`: the capability id, written exactly as it appears in the list.
- `entrypoint`: the CLI verb, RPC method, subsystem, plugin, tool or consumer.
- `production_path`: how the entrypoint reaches the code, in plain words.
- `classification`: one of the four values below.
- `owner`: who closes the gap. Use `-` on a verified row.
- `evidence`: for a verified row, the probe that proves it; otherwise a short
  note on what was checked or what is missing.

A cell cannot be empty and cannot contain a pipe character.

## Classification

- `verified`: a probe proves the path works. The evidence cell names the
  probe, written `TestCapmap_<Name>`.
- `present-unverified`: the code exists on the path, but no probe proves it.
- `missing`: the path does not exist or is not wired to its entrypoint.
- `policy-refused-as-specified`: the specification says Cascade refuses this on
  purpose, and the refusal is the behavior.

Every row that is not `verified` needs an owner: a planning ticket id of the
form `P<phase>-<EPIC>-<NN>`, or `NEW:P1-<CODE>` when a ticket does not exist
yet.

## What a verified probe asserts

A probe registered for a verified row makes four assertions about the real
binary, built once from the tree under test:

1. Authorization: an authorized caller succeeds, or an unauthorized caller gets
   the typed refusal.
2. Routing: the request reaches the named subsystem, not a stand-in.
3. Side effect: the change shows up in stored state, not only in a reply.
4. Result: the caller gets the documented result or receipt.

## How the harness checks the table

Run it with `go test -tags capmap -count=1 ./internal/integration/capmap/`. The
build tag keeps it out of the default test run.

`TestCapmapTable_Evaluates` runs the probe each verified row names as a
subtest. A probe counts as passed only if its subtest ran to the end, did not
fail and did not skip. The harness then checks the table against the capability
list and that set of passed probes in the same run. It fails on any of these:

- a capability in the list with no row, or a row whose id is not in the list;
- a wrong header, a separator row without six cells, a row with the wrong
  number of columns or without its leading pipe, a second table, or an empty
  cell;
- a classification outside the four values;
- a verified row that names no probe, or names one that was not registered,
  failed or skipped;
- a row that is not verified and has no owner.

## Current state

This table was filled from a census of the built binary at the tip of the
integration branch. Every `verified` row has a registered probe that ran as a
subtest of the evaluating run. Every other row names the ticket that closes
it. A row says `missing` only where the built binary has no entrypoint or the
daemon does not register the method; it says `present-unverified` where the
path exists but no probe proves all four assertions, usually because the probe
would need a provider, a node, an installed harness or an enrolled helper.
Planned capabilities that the capability registry places in a later phase are
`missing`, and their owners are tickets of that later phase.

| capability | entrypoint | production_path | classification | owner | evidence |
| --- | --- | --- | --- | --- | --- |
| cap:agent-drivers | RPC conductor.delegate and agent spawn | no registration: the live daemon answers method not found for conductor.delegate, and providers/agents holds only the conformance and local packages | missing | P1-AGT-08 | live daemon: conductor.delegate returns method not found; git grep finds no non-test importer of a claude, codex or opencode driver package |
| cap:audit-log | `cascade approval deny`, then `cascade policy audit query` | approval verbs dial the daemon, whose one approval queue and policy engine append records to the hash-chained audit store | verified | - | TestCapmap_AuditLogApprovalDeny |
| cap:authority-boundary | none yet: capability is planned | no production entrypoint exists; precursor is internal/jobs/release_gate.go only | missing | P2-GOV-03 | no CLI verb, RPC method or subsystem names this capability on the built binary |
| cap:backup | `cascade backup create` (elevated) | backup verbs run daemonless through internal/backup; create is an elevated verb that needs an enrolled helper | present-unverified | P1-EXIT-04 | backup create refuses without the elevation helper and a target; no probe can enroll a helper locally; backup list answers empty |
| cap:backup | `cascade backup list` and `backup target list` while the daemon runs | verbs open cascade.db directly instead of routing through the daemon | present-unverified | P1-EXIT-04 | with the daemon up, both verbs fail: conflict, sqlite exclusive lock held by another process |
| cap:backup | `cascade backup target add` usage | cobra requires --cron and --domain but the usage text does not mark them required | present-unverified | P1-EXIT-04 | target add with no flags fails with required flag(s) cron, domain not set; the help lists both flags as optional (A1-80 still true) |
| cap:ci-integration | `cascade ci run` while the daemon runs | ci run opens the store itself | present-unverified | P1-CI-11 | with the daemon up, ci run fails: conflict, daemon owns the store; run daemon status and retry |
| cap:ci-integration | daemon CI dispatcher and `cascade github ci wait/watch` | the live daemon manifest has ci.attention-subscriber only; no CI dispatcher subsystem is composed | present-unverified | P1-CI-10 | live status lists no dispatcher subsystem; ci status answers (no CI results recorded) until a local run is recorded |
| cap:cli-output | `--json` on `status`, `memory forget` and `vault get` | cmd/cascade command code writes through internal/output, which renders the versioned envelope and maps the error kind to the exit code | verified | - | TestCapmap_CliOutputEnvelope |
| cap:cli-output | `cascade doctor --json` when a check warns | doctor writes the report envelope and then a second error envelope on stdout | missing | P1-CORE-09 | with the daemon stopped, doctor --json exits 5 and stdout holds two JSON documents: an ok report envelope and an unavailable error envelope |
| cap:cli-output | `cascade version --json` | version prints plain text and ignores --json | missing | P1-CORE-09 | version --json writes the human text on stdout; docs/cli-output-contract.md requires --json as the equivalent of every human output |
| cap:conductor-routing | `cascade run --task classify` | run dials conductor.execute; with no provider registered the router has no lane | present-unverified | P1-CAP-11 | run returns unavailable: conductor: no candidate lane; conductor.router is running with 9 task classes |
| cap:conductor-routing | conductor Select failure handling | Router.Select error is replaced by ErrNoLane for every failure | present-unverified | P1-CAP-11 | internal/conductor/execute.go lines 101-105 audit and return ErrNoLane whatever Select returned (W3 lead, still true) |
| cap:context-assembly | `cascade context slice` and `context show` | assembly runs but draws nothing in a general scope | present-unverified | P1-GEN-01 | with one stored memory record, context slice reports 0 tokens and 0 sources in every slot and context show prints no tiers resolved |
| cap:context-pack | none yet: capability is planned | no production entrypoint exists; precursors are internal/context/assembly.go and budget.go | missing | P2-EXC-04 | no CLI verb, RPC method or subsystem names this capability on the built binary |
| cap:conversation-journal | RPC chat.append_turn and chat.get_thread | daemon registers the chat namespace through cmd/cascade chat_wiring.go; internal/conversation stores turns in cascade.db | verified | - | TestCapmap_ConversationJournalAppend |
| cap:conversation-journal | `cascade_cpa_send` on a bridged thread | bridge send path blocked | present-unverified | P1-CPA-01 | bridge disabled; ticket P1-CPA-01 is blocked |
| cap:doc-freshness | no verb: docs gate runs in internal/build | freshness is checked by build gates only; no runtime entrypoint on the binary | missing | P1-DOC-09 | no CLI verb or RPC method for doc freshness on the built binary; counts drift gate is test-only (internal/build/countsdriftgate.go) |
| cap:doctor | `cascade doctor` and `cascade doctor bundle` | doctor builds its check registry in cmd/cascade, probes the live daemon for the census check, and bundle writes the report archive | verified | - | TestCapmap_DoctorBundle |
| cap:egress-policy | egress substitution on stream, embed, chat and count paths | no probe reaches an outbound call without a provider counterpart | present-unverified | P1-CPA-10 | conductor refuses before any egress with no lane; the open defect named in the registry is not re-provable without a provider |
| cap:elevation | elevated verbs in daemonless mode (`vault get`) | elevated verbs refuse unless a helper is enrolled | policy-refused-as-specified | P1-SEC-03 | vault get exits 8 with elevation-required and prints nothing secret; docs/elevation.md states daemonless elevated verbs refuse |
| cap:elevation | elevation helper enrollment | needs the platform helper and a trust store | present-unverified | P1-EXIT-02 | elevate-helper enrollment cannot run in an isolated home |
| cap:embeddings-retrieval | production embedder | no composition root builds an embedding provider | missing | P1-CPA-11 | chat.append_turn returns topic_warning: no production embedding provider is composed in this daemon; recall index rebuild writes 0 chunks |
| cap:events-bus | daemon SSE stream `GET /events` | stream accepts an owner subscriber and refuses browser-shaped requests; no event observed | present-unverified | P1-SEC-15 | owner subscriber got 200 text/event-stream; browser-shaped request got 403; no event arrived after memory.remember, approval.deny or chat.append_turn within 3 seconds |
| cap:evidence | `cascade pbd evidence` and its RPC mirror | no verb and no RPC method serves the evidence ledger | missing | P1-PBD-02 | pbd verbs are board claim create dispatch done edit lint move step summary validate; the live registry lists no evidence method |
| cap:execution-contract | none yet: capability is planned | no production entrypoint exists; no contract package exists under internal/conductor | missing | P2-PEW-15 | no CLI verb, RPC method or subsystem names this capability on the built binary |
| cap:execution-path-matrix | none yet: capability is planned | no production entrypoint exists; precursor is internal/fleet/topology/types.go | missing | P2-JDG-07 | no CLI verb, RPC method or subsystem names this capability on the built binary |
| cap:execution-telemetry | learn outcome and retention jobs | no learn subsystem in the live manifest | present-unverified | P1-CAP-09 | live status shows no learn or telemetry subsystem |
| cap:fleet-sessions | `cascade fleet capacity` (RPC fleet.capacity) | verb exists; the daemon does not register the method | missing | P1-CAP-05 | fleet capacity returns not-found: method not found: fleet.capacity |
| cap:fleet-sessions | `cascade fleet sessions` (RPC fleet.sessions.list) | answers an empty list; harness-session-reader is running | present-unverified | P1-HRN-13 | fleet sessions prints the header only; no installed harness to register a session |
| cap:fleet-sessions | `cascade fleet usage` | verb exists and answers; store empty | present-unverified | P1-TOP-13 | fleet usage prints no usage recorded |
| cap:github-integration | `cascade github repos/prs/issues/ci` | verbs exist; dispatch needs a launched process plugin | missing | P1-PLG-04 | github repos returns unavailable: the cascade-github plugin is process-tier and this build has no trust-elevation path that can launch one |
| cap:governor-admission | daemon admission controller | no governor or admission subsystem in the live manifest | present-unverified | P1-CAP-12 | live status lists 14 subsystems and none is the admission controller |
| cap:harness-projection | `cascade context harness list/sync` | list probes harness directories; sync regenerates files for installed harnesses | present-unverified | P1-GEN-06 | in an isolated home all three harnesses read not installed; sync reports 0 files regenerated; no installed harness counterpart |
| cap:impact-closure | none yet: capability is planned | no production entrypoint exists; precursor is internal/ci/affected_go.go | missing | P2-PEW-11 | no CLI verb, RPC method or subsystem names this capability on the built binary |
| cap:init-config | `cascade init --yes --no-daemon` and `cascade config set/get/path` | init walks its steps through internal/runtime/initconfig and creates cascade.db; config verbs load, validate and write config.toml | verified | - | TestCapmap_InitConfigInitAndSet |
| cap:init-config | `cascade config validate` | validate does not check the profile enum | present-unverified | P1-SEC-30 | config set runtime.profile bogus is accepted and config validate says valid, yet the next load fails: invalid profile (must be one of local, server, worker) |
| cap:init-config | `cascade init --yes --no-daemon` storage step | init reports the machine set up and names cascade.db, but the database file is not created | present-unverified | P1-NODE-12 | after init --yes --no-daemon exits 0 and prints cascade is set up with a storage path, cascade.db does not exist; init --check then exits 3 and still says it would create the local database |
| cap:inventory-sport | `cascade doctor sport` and `doctor counts` | reads an embedded registry snapshot | present-unverified | P1-DOC-06 | doctor sport prints a registry as of a fixed time; doctor counts says 9 plugins while plugin list shows 7 |
| cap:job-dag-leases | `cascade fleet jobs/leases` (RPC job.* and lease.*) | verbs answer an empty list; no CLI verb creates a job outside a PEWS tree | present-unverified | P1-CAP-10 | fleet jobs list prints the header only; jobs.scheduler is running as controller; no job source in the probe |
| cap:mcp-server | `cascade mcp serve` (stdio tools/list and tools/call) | mcp serve builds the policy filter and method table (cmd/cascade mcp_tools.go) and forwards tool calls to the daemon over its socket | verified | - | TestCapmap_McpServerServe |
| cap:memory | `cascade memory remember` (RPC memory.remember) | memory verb dials the daemon, which writes the record through internal/memory to the memory store under the data home | verified | - | TestCapmap_MemoryRemember |
| cap:migration-tooling | `cascade migrate v1` | no migrate verb in the command tree | missing | P1-MIG-03 | the built binary has no migrate command (199 commands walked) |
| cap:model-door | RPC conductor.execute | registered on the daemon registry; needs a lane and a provider counterpart to answer | present-unverified | P1-CAP-11 | live daemon: empty params return invalid-input; a valid request returns no candidate lane without a provider; no local counterpart |
| cap:node-placement | RPC node.enroll and node.heartbeat | registered in internal/nodes but absent from the live daemon registry | missing | P1-NODE-18 | live daemon: node.enroll and node.heartbeat return method not found |
| cap:node-placement | `cascade node enroll USER@HOST` (elevated, ssh) | needs an ssh node and an enrolled helper | present-unverified | P1-NODE-15 | node list prints no enrolled nodes; enroll needs ssh to a real host |
| cap:node-placement | router node placement | engine composed at daemon_unix_conductor.go:127; consulted only when a request names node capabilities | present-unverified | P1-NODE-02 | internal/conductor/router_placement.go:84-89 skips the consult with no node capabilities; the run --require vocabulary is reasoning, context, structured |
| cap:notify | `cascade pa pair` and the bridge subsystem | bridge module disabled in the manifest | present-unverified | P1-AGT-04 | pa pair returns unavailable: no bridge module is enabled; cascade-pa.bridge subsystem is disabled |
| cap:pbd-engine | `cascade pbd board/validate/summary` | verbs need a PEWS tree; an empty tree prints nothing | present-unverified | P1-PBD-03 | pbd board on an empty directory exits 0 with no output |
| cap:plan-projection | none yet: capability is planned | no production entrypoint exists; ticket compilation still lives in internal/jobs | missing | P2-PEW-20 | no CLI verb, RPC method or subsystem names this capability on the built binary |
| cap:plugin-host | `cascade plugin list/info/disable` | builtin plugins list; builtin cannot be disabled | present-unverified | P1-PLG-10 | daemonless: seven builtin plugins listed; plugin disable pbd refused unsupported; no external plugin to load |
| cap:plugin-host | `cascade plugin list` while the daemon runs | verb opens cascade.db directly | present-unverified | P1-PLG-10 | with the daemon up plugin list fails: conflict, sqlite exclusive lock held by another process |
| cap:provenance | none: package holds a doc file only | internal/provenance has no implementation | missing | P2-GOV-01 | no verb, method or caller; the registry lists the capability as a stub |
| cap:provider-drivers | `cascade provider add/test/health` | needs a credential and a reachable provider | present-unverified | P1-EXIT-02 | provider list prints no providers registered; add needs a key and network |
| cap:quality-gates | `cascade ci run --build-only` on a checkout | ci run resolves the repository, runs the build step, records the run in the CI store, and ci status reads it back | verified | - | TestCapmap_QualityGatesCiRun |
| cap:retrieval-fusion | `cascade recall what` and `recall index rebuild` | recall.what runs the memory leg only; rebuild indexes no corpus | present-unverified | P1-CPA-09 | with a stored memory record, recall what returns no results; legs list is memory only; conversation and file legs error in general scope; rebuild writes 0 chunks |
| cap:review | `cascade review --diff` | needs a reviewer lane and provider | present-unverified | P1-PBD-01 | review cannot dispatch with no provider; plugin.review.review is registered |
| cap:rpc-daemon-api | `cascade daemon start`, then JSON-RPC over the daemon socket | cmd/cascade daemon lifecycle starts the ipc-socket and rpc-registry subsystems; POST /rpc runs the owner and browser-shape guard, then the registry | verified | - | TestCapmap_RpcDaemonApiStatus |
| cap:run-engine | none yet: capability is planned | no production entrypoint exists; pkg/provider carries text-only chat messages | missing | P2-SRF-19 | no CLI verb, RPC method or subsystem names this capability on the built binary |
| cap:scheduler | daemon scheduler subsystems | events.scheduler-loop and jobs.scheduler run; no runnable observed | present-unverified | P1-TOP-06 | live status: both subsystems running; no scheduled runnable fires inside a probe time bound |
| cap:secrets-vault | `cascade vault set` and `vault list` | vault verb reads stdin, writes through internal/secrets to the file-vault custody backend (vault.age), and an unelevated get is refused | verified | - | TestCapmap_SecretsVaultSet |
| cap:storage-abstraction | sqlite store and domains | sqlite exercised through the verified probes; postgres adapter needs a server | present-unverified | P1-LRN-01 | chat and memory persist across a daemon restart (journal and memory probes); no postgres server available locally |
| cap:sync | `cascade sync run` | no run path is wired | missing | P1-EXIT-03 | sync run returns unavailable: no run path is wired; sync status lists seven domains with strategies |
| cap:verification-policy | none yet: capability is planned | no production entrypoint exists; precursors are internal/jobs/risk.go and internal/review/crc.go | missing | P2-GOV-02 | no CLI verb, RPC method or subsystem names this capability on the built binary |
| cap:verification-provider | none yet: capability is planned | no production entrypoint exists; precursor is internal/ci/route.go | missing | P2-GOV-05 | no CLI verb, RPC method or subsystem names this capability on the built binary |
| cap:widget-macos | `status.widget` RPC feed | feed answers; no widget app consumes it | present-unverified | P1-WID-08 | status.widget is in the live registry; apps/ does not exist |
| cap:widget-macos | apps/widget-macos | directory absent | missing | P1-WID-02 | ls apps fails: no such directory |
