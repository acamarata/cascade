# Jobs, DAG, Leases

The `jobs` cascade.db domain (R-14.5, owned by Epic AC) hosts seven
tables, materialized by `internal/jobs` via the shared portable migration
builder.

## Schema

| Table | Purpose | Key fields |
|---|---|---|
| `jobs_job` | one row per DAG-node job | id, state, capabilities[], mutable_scope, risk_class, min_task_class, node_requirements, timeout_seconds, cost_ceiling, priority |
| `jobs_task_dependency` | deps[] edges | job_id, depends_on_job_id |
| `jobs_execution` | one row per attempt (R-16.33) | id, job_id, attempt, state, started_at, ended_at |
| `jobs_execution_result` | one outcome per execution | execution_id, output_summary, error_kind, error_message, artifact_refs[] |
| `jobs_artifact` | one produced file/blob | id, job_id, execution_id, kind, blob_key (BLAKE3), path |
| `jobs_resource_lease` | the one-mutable-writer lease record | repo_id + scope_glob (natural key), holder, issued_at, ttl_seconds, renew_count, journal_ref |
| `jobs_worktree` | one checked-out working tree | path (key), lease ref, repo, branch |

No `jobs_ci_attestation` table exists; AF/S-65.T4 owns it.

## Job state machine (R-16.37, amended by R-21.140)

```
pending -> leased -> running -> verifying -> reviewing -> accepted
                                                         -> rejected
any non-terminal -> cancelling -> cancelled
any non-terminal -> failed
```

Unknown or unparseable state decodes to `failed` — never a permissive
zero value. Four transitions are **policy-reserved** and refused on the
public store path with a typed error: `running->verifying`,
`verifying->reviewing`, `reviewing->accepted`, `reviewing->rejected`.
Only `internal/policy`, through `PolicyTransitionAllowed`, may invoke
them (S-60.T3 wires the real caller).

## W9 columns (inert here; semantics owned elsewhere)

- `jobs_job.consecutive_failed_attempts` (R-21.84) — increment/reset/wake
  debounce owned by AP/S-81.T3.
- `jobs_job.consequence_class`, `jobs_artifact.consequence_class`
  (R-21.99, closed set `trivial`/`normal`/`consequential`) — derivation
  from the AC/S-59.T4 risk class owned by AQ/S-83.T4.
- `jobs_job.data_class` (request floor) and `jobs_artifact.data_class`
  (IMMUTABLE) (R-21.94) — lattice join owned by AQ/S-83.T1-T4.

## W6 fencing + liveness columns (inert here; semantics owned elsewhere)

- `jobs_resource_lease.epoch` (R-21.139, monotonic per repo id + scope)
  and `.state` (closed set `held`/`renewing`/`expired_unconfirmed`/
  `expired_orphaned`/`released`) — the fence CHECK and reclaim are
  AC/S-59.T2's.
- `jobs_execution.pgid` and `.heartbeat_at` (R-21.140/R-21.172), plus the
  `abandoned` execution state — the liveness probe and quarantine sweep
  are AC/S-59.T3's; the heartbeat reaper is AC/S-59.T5's.
- `JobState`'s non-terminal `cancelling` — the sole gateway into
  `cancelled` — with the verified-termination gate on
  `cancelling->cancelled` owned by AC/S-59.T5 (driver side: AD/S-61.T1).

## Lease model: one mutable writer per lease scope (AC/S-59.T2)

`LeaseManager` (`lease.go`/`lease_query.go`/`lease_expiry.go`/
`lease_fence.go`/`lease_events.go`) is the DECIDED authority: two
requests whose scopes intersect never both hold a lease at once.

**Scope normalization (R-21.168, superseding R-16.37's doublestar-
intersection wording).** A caller pattern is expanded at acquire time to
its minimal repo-relative directory-prefix cover: a trailing `/**` is the
only wildcard kept in the normalized, stored form, and a literal
(non-wildcard) pattern is read as a FILE path — reduced to its own
parent directory, never assumed to itself be the directory to lease. Any
other wildcard placement (a mid-segment `*`, a `**` not at the very end,
a bare trailing `*`) is rejected at acquire with a typed error naming the
offending segment. `github.com/bmatcuk/doublestar/v4` validates the
caller's glob syntax and finds the literal/meta split point;
intersection is then a total, decidable test — one normalized prefix is
a prefix of, or equal to, another — checked over every pair in each
side's set. Examples: `internal/jobs/**` and `internal/jobs/lease.go`
intersect (the second reduces to `internal/jobs`, a prefix match);
`internal/jobs/**` and `internal/nodes/**` do not.

**Defaults (R-16.37 `[jobs].lease` verbatim).** `ttl=2h`,
`renew_every=15m`, `expiry_grace=10m`, as the typed `LeaseDefaults`
struct (`DefaultLeaseDefaults()`), injected into `LeaseManager` by the
daemon composition root. Registering these as config keys in
`internal/runtime/config` is a named follow-up, not this ticket's
files_scope — see lease.go's `LeaseDefaults` doc comment for the full
contract-vs-files_scope note.

**Acquire/queue.** `Acquire` runs the conflict scan and the grant inside
one `*sql.Tx` over the Store's single-connection db, so two concurrent
Acquire calls for intersecting scopes can never both observe "no
conflict". No conflict grants a `held` row stamped with the next epoch
for that (repo id, normalized scope); a conflict returns a typed
contended result naming the conflicting lease, and the requester stays
queued (no CLI/RPC re-attempt surface here — that is S-60.T1's).

**Renew/expiry (R-21.139/R-21.177).** `Renew` accepts while `now <= last
issue/renew + ttl + expiry_grace`, incrementing `renew_count` and
re-arming `ttl`. `SweepExpired` moves any lease past that deadline to
`expired_unconfirmed` and raises exactly one `stall` attention item
(idempotent on (kind, source_ref) = the lease's stable entity id,
`lease:<repo>:<scope>`) — the lease is NEVER transferred to another
holder at this point; `expired_unconfirmed` still admits no contender.

**Fence and closed state machine.** The closed state set is `held` /
`renewing` / `expired_unconfirmed` / `expired_orphaned` / `released`.
`Fence(ctx, repoID, scopeGlob, epoch)` is the single validation entry
point every lease-scoped mutation (worktree add/remove, job-branch
commit/CI dispatch, integration/release, evidence write) presents its
believed epoch to; a mismatch refuses with `ErrLeaseFenced` and raises
the same idempotent attention item. `Reclaim` is the only path out of
`expired_unconfirmed`: it probes the holder's most recently recorded
execution pgid through the injected `ProcessLivenessProbe`
(`unixLivenessProbe`'s signal-0 check on unix,
`windowsLivenessProbe`'s handle/exit-code check on windows) — a dead
pgid moves the row to `expired_orphaned`, advances the epoch, and frees
the scope for a new grant; a live pgid changes nothing, and the
contender stays queued. A resuming job re-acquires by holder-id match at
the new epoch.

**Release.** Two paths: explicit `Release(repoID, scopeGlob, epoch)`,
and `store_job.go`'s `PutTransition` release-on-terminal hook
(`Store.onTerminal`, wired via `WireLeaseRelease`) firing once a job
lands in any of its four terminal states. Per R-21.193, the AH/S-70.T3
stall transition calls the SAME explicit `Release` path with the lease's
current epoch: a current-epoch stall release moves the lease to
`released` and wakes queued contenders, while a stale-epoch stall
release is refused with `ErrLeaseFenced` — the same fence check every
other mutation gets.

**Controller-singleton authority (R-21.169).** `Acquire`, `Release` and
`SweepExpired` all refuse with the typed `ErrNotController` unless the
injected `isController` func reports true — a node daemon never writes
lease state.

**Journal + event bus.** Every lifecycle transition
(acquired/renewed/released/expired/reclaimed/contended) appends to an
`internal/fleet/journal` entity log keyed by the lease's stable entity
id, and publishes on `internal/events`' `jobs.lease` namespace
(`EventLeaseAcquired`/`Contended`/`Renewed`/`Released`/`Expired`) — the
T5 scheduler's and S-60.T1's filtered-SSE feed's input. No RPC methods
are registered by this ticket.

**Forward note (R-21.141, not implemented here).** The W9 reservation is
the sole dispatch door; AC leases become subordinate claims it creates.
Routing acquisition through `economics.Reserve` is AO/S-79.T4's.

## Migration and owner registration

`internal/jobs.MigrationSet()` claims schema_version 5 in cascade.db's
single global version sequence (bootstrap=1, context/scope=2,
retrieval/lifecycle=3, providers/registry=4). `internal/storage/
domains.go`'s `AllDomains` entry for `DomainJobs` now names
`internal/jobs` as the owner package.

## DAG planner (AC/S-59.T4)

`Planner.Plan(ctx, PlanInput, SessionScope) (ExecutionDag, error)` is the
`conductor.plan` seam (the RPC alias over this Go seam lands with
AP/S-82.T1, R-21.39). `SessionScope` (E/S-08.T4) is a REQUIRED input,
resolving the repository root the risk classifier's content probes read.

`PlanInput` is a ticket|intent union: `TicketInput {ID, ModelClass,
DependsOn, Footprint}` plus five pass-through fields (`Capabilities`,
`NodeRequirements`, `Timeout`, `CostCeiling`, `Priority`) the planner
carries verbatim, never synthesizes; `IntentInput {Intent}` plus the same
pass-through fields. Each `Plan` call returns a single-node
`ExecutionDag`: a ticket's node has `Deps` from `DependsOn` and
`MutableScope` from `Footprint`; an intent's node has empty `Deps` and
`MutableScope`. `DagNode` carries EXACTLY `{Capabilities, Deps,
MutableScope, RiskClass, MinTaskClass, NodeRequirements, Timeout,
CostCeiling, Priority}` plus its id.

`MinTaskClass` is `conductor.ModelClassToTaskClass(ModelClass)` (the
06 §5.18 mapping: mech/build/heavy -> code, review -> review, arbiter ->
arbitrate) -- an unknown `ModelClass` is a typed `KindInvalidInput`
error, never a default task class. A dependency cycle (including a
ticket declaring itself as its own dependency) and an empty input are
also typed `KindInvalidInput` errors; cycle detection is a general
depth-first coloring check (`validateAcyclic`) that also covers a future
multi-node DAG, not only the single-node W6 case.

Consumers: T5's scheduler (`advance(dag, event)`) consumes
`ExecutionDag`; S-60.T1/T2/T3 and AH/S-69.T1 consume `RiskClass` +
`GateSet`; AH/S-69.T3 is the PEWS 17-field -> `PlanInput` compiler (this
package imports no `plugins/pbd`).

## Risk classifier and gate sets (AC/S-59.T4)

The R-21.182 footprint is the union of pre-image paths, post-image paths
(both folded into the caller's declared footprint) and an optional
injected `ReachabilityFn func(ctx, paths) ([]string, error)` (R-21.257):
`nil` means no expansion (the W6 default -- AG/S-67.T3 in W7 is the
first implementer); a non-nil seam is called once and its paths join the
union; a seam ERROR fails closed to Critical, never to a lower class and
never to a silent path-only fallback.

**Unlowerable Critical floor (R-21.182, NORMATIVE):** any footprint path
that is a gate table, a classifier table, a policy file, an egress-class
file or a generated harness artifact classifies Critical, evaluated
BEFORE the R-16.37 rules below; no later rule, reclassification or
config key lowers a floor hit. R-21.182 names the five categories by
role; this ticket's own derivation maps them to concrete paths
(`internal/build`'s gate files; this package's own `risk.go`/
`riskgates.go` plus `internal/conductor/task_classes.go`;
`internal/policy/**`; `internal/secrets/**`; `internal/repo/templates/**`
and `.github/workflows/**`) -- extending the list is expected as later
tickets add their own tables; narrowing it needs a T0 ruling.

**R-16.37 rules, first match, in this order:**

| Class | Rule |
|---|---|
| Critical | `internal/secrets/**`, `internal/policy/**`, `internal/elevation/**`, `providers/*/auth*`, `**/migrations/**` containing DROP/ALTER, `.goreleaser.yaml`, `install.sh` |
| High | >=2 distinct repositories in the plan; any `**/migrations/**` or `*.sql` path; an existing file importing `sync/atomic` or containing `go func`; any `pkg/**` path (a potential exported-signature change, unprovable at plan time) |
| Low | only `docs/**` and `*.md` paths |
| else | Normal (including an empty or unresolvable footprint) |

Content probes (the DROP/ALTER marker, `sync/atomic`, `go func`) read
only EXISTING files under the classifier's probe root; an absent path
contributes a path-rule match only.

**Gate sets** (`RiskClass -> GateSet`, DECIDED, additive by severity —
low < normal < high < critical):

| Class | Gates |
|---|---|
| Low | format + static + targeted verification |
| Normal | Low + build + lint + targeted tests + code review + integration checks |
| High | Normal + independent QA + adversarial review + affected/full integration CI + clean-node verification |
| Critical | High + explicit human approval + rollback evidence + release gate |

AH/S-69.T1 owns the policy-table-as-data form this mapping encodes.

## Job templates (AC/S-60.T2)

`JobTemplate.Resolve(ctx) (DagNode, error)` is the typed-template seam
for the six DECIDED work kinds (`implement`, `review`, `adversarial`,
`qa`, `ci`, `integrate`). `TemplateRegistry.By(kind)` resolves a kind to
its `JobTemplate`; an unrecognised kind returns the typed
`ErrUnknownTemplateKind`, never a panic or a `(nil, nil)` return.
`TemplateRegistry.Register` is exported and additive, so AH/S-69.T2
extends the same registry with its own CR/QA/adversarial-reviewer
templates rather than building a parallel one.

Because `Resolve` takes only a `context.Context` (no second parameter),
per-invocation data (the node's id, its declared footprint, its
`depends_on` edges and the five pass-through fields) travels on ctx via
`TemplateContext` / `WithTemplateContext`. A nil ctx, a ctx with no
attached `TemplateContext`, or a `TemplateContext` with no id are all
typed refusals, never a zero-value `DagNode`.

Templates set ONLY the DECIDED `DagNode` field set -- no field was added
to `dag.go` for this ticket:

| Kind | mutable_scope | min_task_class | capabilities (added) | node_requirements (added) | risk_class |
|---|---|---|---|---|---|
| implement | ticket footprint | code | -- | -- | classifier |
| review | empty (read-only) | review | `review`, `family:distinct-from-author` | -- | Normal |
| adversarial | empty (read-only) | review | `review`, `adversarial` | -- | Normal |
| qa | empty (read-only) | review | -- | -- | Normal |
| ci | empty (read-only) | code | -- | `clean-room` | Normal |
| integrate | target subtree glob | code | -- | -- | Normal floor, classifier may elevate |

`implement` and `integrate` close over a `FootprintClassifier` at
construction (constructor injection, per this ticket's own HOW-2); the
production value (`defaultClassifier`) calls the SAME
`classifyFootprint` the DAG planner uses (risk.go), never a parallel
re-derivation. `review`/`adversarial`/`qa`/`ci` always report
`MutableScope = nil` regardless of what the caller's `TemplateContext`
carried -- these kinds never touch files, so a declared footprint would
misstate what the node does.

**Disclosed gap (K/S-23.T6):** the ticket contract names a "K/S-23.T6
FlowDecision" function this ticket should call to populate a DagNode's
gate set. K/S-23.T6 (`internal/conductor/flow.go`) ships `ParseVerdict`,
`Consensus` and `LoopStop` -- three pure functions over a MULTI-REVIEWER
verdict set collected at review-decision time, none of which produces a
gate set, and no such function exists under any other name. `DagNode`
also carries no gate-set field (S-59.T4's frozen field set). No template
here calls any of the three: doing so with synthetic single-entry input
just to satisfy the contract's wording would be fabricated wiring, not a
real integration. A risk class's gate set remains retrievable on demand
via `GateSetForRiskClass(node.RiskClass)` (or, with an overlay,
AH/S-69.T1's `EffectiveGateSet`) at whichever consumer needs it -- it is
never stored on the node itself.

The ">=2 distinct repositories" High-classification rule (risk.go) still
has no multi-repository carrier: `TemplateContext`, like `TicketInput`
before it, carries one footprint and one id, not a repository-id list,
so `defaultClassifier` still resolves through `singleRepository()`.

Consumer: `TemplateRegistry.By` + `WithTemplateContext` have no
production caller yet -- the composition root that would assemble a
real multi-node `ExecutionDag` from registered templates is AC/S-60.T3's
completion gate (`internal/build/testonly-allow.json` names both symbols
against that ticket).

## Reclassification at every checkpoint (R-21.146)

`Reclassify(ctx, planned RiskClass, actual ChangeFootprint,
leaseScopePrefixes []string, probeRoot string) (RiskClass, *Escalation,
outOfScope []string, error)` re-runs the classifier over the ACTUAL
footprint -- `ChangeFootprint{ChangedPaths, DependencyImpactPaths}`, the
candidate tree's real diff (AC/S-59.T3) plus the plan input's resolved
dependency impact -- at every lease checkpoint and once more before
acceptance.

**Monotonic:** within one attempt the class only increases. A derived
class at or below `planned` returns `planned` UNCHANGED (`esc == nil`,
never a downgrade). An increase returns a non-nil `*Escalation{From, To,
TriggeringPaths, InvalidatedGateSet, RequiredGateSet,
RequiredScopePrefixes}`: evidence gathered under `InvalidatedGateSet`
(the planned class's gates) is invalidated, `RequiredGateSet` (the
derived class's gates) applies, and `RequiredScopePrefixes` must be held
before the attempt continues.

**Scope containment** (R-21.146 with R-21.168): `outOfScope` names the
changed paths NOT covered by the lease's normalized scope prefixes,
independent of the derived class -- empty for an in-scope diff, every
offending path otherwise.

The gate that denies and requeues on escalation or containment violation
is AC/S-60.T3's; the checkpoint call site is AF/S-65.T2's — neither is
implemented here.

## Lifecycle stages and the risk-gate overlay (AH/S-69.T1)

`LifecycleStages()` returns the 13 R-16.13 DECIDED dev-shop stages, in
written order, as DATA (never prose prompts): intent, scope/understand,
plan, decompose+lease, implement, CR, QA, adversarial (risk-dependent),
integrate, clean-node CI, release/CD gate, accept, learn.
`ParseLifecycleStage` is fail-closed (`ErrUnknownLifecycleStage`, no
permissive zero value). `ReclassificationStages()` returns exactly
`{StagePlan, StageImplement, StageAccept}` -- R-21.146's three
mandatory reclassification points (plan time, every lease checkpoint
reached during implement, and immediately before acceptance) expressed
over the same enum.

**ONE risk model, R-16.70(b):** the RiskClass -> GateSet mapping has
exactly one representation -- `riskgates.go`'s `GateSetForRiskClass`
table and its `GateItem` enum, both AC/S-59.T4's. This section owns
only a TIGHTENING-ONLY overlay on top:

- `RiskGateOverlay` (`riskgates_overlay.go`) is a `map[RiskClass]GateSet`
  of per-class ADDITIONS only -- a removal is not representable in the
  type, so `EffectiveGateSet(rc, ov)` is structurally always a superset
  of `GateSetForRiskClass(rc)` and of the R-21.182 Critical floor's own
  gate set, for every overlay value.
- `EffectiveGateSet(rc, ov)` unions the table's result with `ov[rc]`,
  table order then overlay order, de-duplicated.
- `ParseGateItem`/`BuildRiskGateOverlay` validate raw gate-step names
  against `riskgates.go`'s own maximal set (`criticalGates`), never a
  second enumeration.

**`[policy.risk_gates]` config (08 §3, hot reload class):**
`internal/policy` cannot import this package (`internal/jobs` already
imports `internal/conductor` -> `internal/hooks/egress` ->
`internal/secrets` -> `internal/policy`, so the reverse edge cycles).
`internal/policy/risk_gates_config.go` therefore parses the overlay's
SHAPE only (a table; recognised class keys `low`/`normal`/`high`/
`critical`; string-list values) into a raw `map[string][]string`;
`jobs.BuildRiskGateOverlay` is where gate-step-name validation actually
happens, called from `cmd/cascade/daemon_unix_policy.go`'s
`validateRiskGateOverlay` BEFORE the config-reload swap
(validate-before-write) -- one layer above `internal/policy`, not
inside it.

**Monotonic reclassification (`risk_reclass.go`):** `RaiseRiskClass(prior,
observed, ov)` returns the higher of the two (severity order); an
observed class at or below prior is CLAMPED, never an error and never a
lowering. An increase returns `RiskEscalation{From, To,
InvalidatedGateSet, RequiresExpandedLease: true}`, naming
`EffectiveGateSet(prior, ov)` as the gate set the escalation
invalidates. `RiskEscalation` is a DISTINCT type from
`risk_reclassify.go`'s pre-existing `Escalation` (a different call site,
prior/observed vs. planned/actual, with a different field set the
ticket contract itself specified) -- the two cannot share a name in one
package without a field-set collision.

**`cascade policy risk explain <path...>`** (`cmd/cascade/policy_risk.go`)
classifies the R-21.182 union footprint (`--pre`, `--post`,
`--symbol-reach`, bare positional paths counting as both pre- and
post-image) through `ClassifyFootprint` (exported wrapper over this
package's own `classifyFootprint`) and reports `EffectiveGateSet`'s
result with each gate's provenance (table vs overlay) and `floor:
critical` with the matching path when the Critical floor resolved the
class. It ships CLI-only, running entirely locally (no daemon dial) --
the contract's JSON-RPC mirror was left unshipped rather than shipped
either unauthenticated or in violation of `internal/policy`'s own
handler/verb-registry invariant test; see the S-69.T1 journal.

## Completion gate and evidence ledger

The completion gate enforces "an agent saying done is evidence, not the
completion decision": `CompletionPolicy.Transition` is the only path
that may move a job across a policy-reserved edge (`running` ->
`verifying` -> `reviewing` -> `accepted`/`rejected`), and it checks
seven things before allowing one, in the order the evidence actually
needs (not the order the numbers in the ruling suggest, since the risk
reclassification step must run BEFORE the completeness check): caller
identity, state ordering, the evidence hash chain, risk reclassification
plus lease-scope containment, evidence completeness, the Critical-class
human-approval check, checkpoint binding, and an atomic commit at the
recorded ledger cursor.

**Evidence ledger** (`evidence.go`, `migration_evidence.go`): a per-job,
append-only table (`jobs_evidence`) of seven `EvidenceKind` values
(`build`, `lint`, `tests`, `review`, `adversarial`, `ci_attestation`,
`human_approval`) and four `ProducerCapability` values
(`controller-run`, `attestor`, `human-approval`, `agent-claim`). Rows
chain by a per-job `prev_hash` (SHA-256 over the canonical JSON of the
preceding row, a 32-zero-byte genesis), verified end to end by
`EvidenceLedger.Verify` before every gate evaluation. A repeated
`idempotency_key` is a no-op returning the existing row; there is no
UPDATE or DELETE path anywhere in this file's API. `agent-claim` rows
are recorded but never satisfy a gate on their own -- the completeness
check (`missingEvidence`) skips them explicitly.

**Producer authorization** (`evidence_authz.go`): every `Append`
presents `{execution_id, lease repo/scope, lease epoch}`. `ProducerAuthz`
refuses (typed `ErrEvidenceProducerDenied`, no row written) unless the
calling process is the controller (the same single-process identity
seam `LeaseManager` uses) AND the execution id resolves to a real
`jobs_execution` row AND the presented epoch survives the AC/S-59.T2
`LeaseFencer.Fence` check. `attestor_identity`'s required prefix
(`node:`, `daemon:`, or `approval:`) is derived from the row's
`EvidenceKind`, never chosen by the caller.

**Approval-token binding** (R-21.164): the human_approval check binds a
token to `{job_id, tree_hash, policy_version}` by reusing the real
I/S-18.T3 `ApprovalQueue.ConsumeToken`'s own action-hash binding --
`ApprovalScopeAction` hex-SHA256-encodes the three fields into a
64-character `Action` string (the queue's display-sanitization ceiling
rejects anything longer), so a token minted for a different job, tree
hash, or policy version fails the queue's own re-hash comparison. There
is no second scope field or second ledger; this is the SAME ledger
`internal/repo`'s inferred-fact store proved out for its own subjects
(R-21.202's satisfied-by-producer posture), reused rather than
duplicated.

**Live stall-detector wiring** (R-16.73, R-21.202, R-40.X18): every
denial publishes exactly one `jobs.gate.denied{job_id, session_id,
ticket_id, reason, attempt}` event on the real event bus (namespace
`jobs.gate`); a successful transition publishes none. R/S-39.T5's
`supervision.Detector` subscribes to this exact stream and normalizes
it into `StallSignal{Kind: gate-denied, ...}` -- proved end to end in
`gate_stall_integration_test.go` (real denials, real bus, real
detector), the live counterpart R/S-39.T5 itself could only verify
synthetically.

**Caller identity**: since `internal/policy` cannot import
`internal/jobs` (the conductor->egress->secrets->policy cycle), the
gate cannot call into a live `policy.Engine` to verify its caller.
`PolicyEngineIdentity{EngineID}` is compared against the `EngineID`
`CompletionPolicy` was constructed with -- a documented, ticket-local
trust boundary; AF/S-66.T1's hook-pack registration is the real
production caller that will thread the real engine id through.

## Ownership boundaries

This domain ships the schema, records, state machine, typed store, DAG
planner, risk classifier, job templates, lifecycle stages, the
tightening-only risk-gate overlay, the `policy risk explain` CLI, and
(P1-E29-W6-S60-T3) the completion-gate engine and evidence ledger
above. It does not implement: the lease fence check/reclaim's own
composition (AC/S-59.T2 ships `Fence`/`Reclaim`, wired here only as a
consumer), the worktree manager (AC/S-59.T3), the scheduler/admission
(AC/S-59.T5), the DAG-assembly CLI/RPC surface (AC/S-60.T1), the
completion-gate hook-pack registration that gives `CompletionPolicy` a
real production caller (AF/S-66.T1), or the `ci_attestation` writer
(AF/S-65.T4).
