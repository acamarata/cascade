# Agentic Dev Shop Guide

Operator-facing guide to the agent drivers that satisfy `pkg/provider.AgentProvider`
(each one under `providers/agents/<name>`) — how each lane is configured, what it
can and cannot do, and how an operator enables anything that ships disabled by
default.

## Agent Drivers

### Local Model Lane (`providers/agents/local`)

The local-model lane dispatches in-process over the `pkg/provider.ModelProvider`
seam — typically a locally-served Ollama instance — with no subprocess and no new
egress class. It sits in the same lane pool a plugin or vendor-hosted driver
occupies.

**Supported task classes.** `classify`, `extract` and `summarize` always dispatch,
regardless of which model is configured. `code`, `reason`, `review` and
`arbitrate` are gated on the `authoring` capability, which is resolved **per
model id**, never per driver.

**Authoring is disabled by default.** A freshly configured local lane advertises
no authoring capability for any model: there is no constructor option, config
setting, or environment variable that grants it. The only way a model id ever
gains `authoring` is a passing run of the named qualification fixture at
`providers/ollama/testdata/qualification/` against that specific model id. The
result is recorded in the `config` storage domain under
`agents.local.authoring_qualified.<model_id>` as `{passed, fixture_hash,
model_id, recorded_at}` — a TOML setting it is not; `08-INIT-CONFIG-SPEC.md` §3
gains no `[agents.local]` section from this.

**Enabling authoring for a model.**
1. Run the qualification fixture against the model id you intend to use. The
   fixture is a small, fixed set of prompt/expected-substring cases; a model
   passes only when every case passes.
2. The passing result is recorded against that exact model id. A different
   model id, or the same model id after the fixture itself changes (the
   recorded `fixture_hash` no longer matches the current fixture), reads as
   unqualified again — fail closed, not fail open.
3. Before relying on the qualified model for `code`/`reason`/`review`/
   `arbitrate` work, the operator should independently verify a sample of its
   output on real tasks — the fixture is a floor, not a substitute for that
   review.

**What cannot grant authoring.** No constructor parameter, no CLI flag, no
config file edit, and no environment variable can set a model id's authoring
capability outside a passing fixture run. `authoring`, and every capability this
driver advertises, is also on the automation-safety hard denylist so an
automated behavioral proposal can never flip it without an operator's explicit
elevation, once that denylist mechanism ships (Epic AI).

See `docs/cli/run.md` for the `cascade run --task` help text, generated from
`cmd/cascade/run.go`'s cobra Long/Example text, which states the same
task-class support inline at the CLI.

## Completion gate

`internal/fleet/hookpacks`'s `"completion-gate"` pack (`RegisterCompletionHookPack`,
`completion_gate.go`) turns the harness's own `TaskCompleted` and `Stop` lifecycle
hooks into a live call against `policy.completion_check` — the R-16.12 binding that
an agent's own "I'm done" claim is evidence, never the completion decision itself.
The rendered hook command posts to the daemon's `fleet.sessions.completion_check`
JSON-RPC method and waits (unlike the fire-and-forget hydration/session hooks
below), so a denial reaches the harness synchronously as a non-zero exit carrying
the real deny reason on stderr.

At daemon startup (`cmd/cascade/hooks.go`'s `wireCompletionHookPack`) the pack is
bound to a real `*jobs.CompletionPolicy` (`internal/jobs`, `P1-E29-W6-S60-T3`) —
the same completion-gate engine and evidence ledger `Jobs-DAG-Leases.md`'s own
"Completion gate and evidence ledger" section documents. Every outcome but a clean
pass is a denial:

- the policy's own check fails (missing evidence, an out-of-scope footprint, an
  expired approval, a checkpoint mismatch) — the real reason string, verbatim;
- the check does not finish inside `completion_timeout` (10s by default, no
  `[fleet.hooks]` config section exists yet to change it) — `"completion check
  timed out"`;
- the daemon-side payload cannot be parsed, or names a job id no row backs.

This is the opposite default direction from the best-effort hydration/session
hooks `internal/fleet/hookpacks/handler.go` registers for `R-16.6`: those fail
OPEN (a slow or unreachable daemon must never block an ordinary tool call, so
their rendered command ends `|| true`). The completion gate fails CLOSED in every
direction once it applies — see the next section for exactly when it applies.

## Completion gate scope

The completion-gate hook is installed into the **user's own** CC harness
configuration, so it fires on every session that harness runs — not only sessions
Cascade itself dispatched. A global fail-closed default would deny an ordinary
human's task completions whenever the daemon happens to be down, mid-upgrade, or
simply busy. R-21.176 resolves this by having the hook decide **scope** before it
ever forms an opinion (`internal/fleet/hookpacks/completion_scope.go`'s
`ResolveJobID`):

- **No job id in the payload** — an ordinary human session, the common case (a
  Cascade-dispatched driver's environment carries `CASCADE_JOB_ID`;
  `pkg/provider.driverEnvAllowlistBase` — a human session simply never sets it).
  The hook allows with **no opinion**: `policy.completion_check` is never called,
  and nothing is journaled as a denial.
- **A job id is present but the resolver cannot be reached** (the daemon is
  unreachable, or its store is unavailable) — scope itself cannot be established.
  This is **also** no-opinion, never a denial: an unreachable daemon must not
  block a human's completion just because a stray `CASCADE_JOB_ID` happened to be
  set in the environment.
- **A job id is present and resolves** — job-scoped. From here the fail-closed
  path applies unconditionally: a real policy denial or an unknown job id (the
  id was presented but no row backs it) both deny and publish `jobs.gate.denied`
  — the SAME event `internal/jobs.CompletionPolicy.Transition`'s own denials
  publish, so `internal/fleet/supervision`'s stall detector (R-16.73) sees a
  hook-level denial exactly as it sees a policy one. A timeout instead
  publishes `jobs.gate.timeout`. Every denial's journaled record (job id,
  session id, hook event, reason, timestamp) is written before the response
  returns.

Because an unreachable daemon on a job-scoped completion is legitimately
concerning (an active job cannot be gated at all until the daemon answers) without
being a denial, `cascade doctor`'s `CompletionGateDoctorCheck`
(`internal/fleet/hookpacks/doctor_check.go`) reports `StatusWarn` when its
injected `LivenessProbe` finds the daemon unreachable while a job is active, in
addition to its unconditional `StatusError` when the `"completion-gate"` pack
itself is not registered for both `TaskCompleted` and `Stop`. As of this writing
the check is built and tested but not yet mounted into `cascade doctor`'s own
process-local registry (a real architecture gap: `cascade doctor` runs as its own
fresh process and never observes the live daemon's `hookpacks.DefaultRegistry`
state — tracked as an `UNOWNED` `internal/build/testonly-allow.json` entry naming
`cmd/cascade/doctor_completion_gate.go` pending that design).

## CI requirement model

`internal/ci`'s `RequirementModel` (`requirements.go`) is the one CI
requirement model in the tree (R-16.71): a `CIRequirement` records which of
seven check classes — Format, Lint, Compile, Unit, Integration, Architecture,
Security — a given invocation must run. Its zero value is deliberately
fail-closed: an all-false `CIRequirement{}` is read by `Requires` as
`AllRequired`, never as "nothing required", so a forgotten or zeroed field
can never silently skip a check.

**Affected-target computation** (`affected.go`/`affected_go.go`/
`affected_cmd.go`) narrows a `CIRequirementPlan`'s `Targets` to the packages
a change set actually touches:

- **Go stack** — a real `go list` subprocess builds the worktree's direct
  import graph in one call (production imports AND `_test.go` import edges
  — `.Imports`/`.TestImports`/`.XTestImports` all fold into the same
  per-package edge list, so a package imported only by another package's
  test file is still selected), then `Affected` walks the reverse of that
  graph from each changed file's real (also `go list`-resolved) owning
  package to collect every transitively-affected local package. Every
  changed path resolves to a target or forces the full fallback below —
  see `.github/wiki/CI-and-Attestation.md § Target selection` for the exact
  fail-closed mapping table (go.mod/go.sum/vendor/deleted packages/testdata).
- **Any other stack** — the operator-configured `[ci].affected_cmd` runs
  through the platform shell with the changed-path list on its stdin (one
  path per line) and its stdout lines become target names.
- **Fallback** — an unrecognised stack, or a non-Go stack with no
  `affected_cmd` configured, returns `[]Target{TargetAll}`: conservative
  correct, never a false skip.

**Target selection** (`selection.go`, R-21.173) is layered on top:
`SelectTargets(ctx, m RequirementModel, changed, candidateTreeHash,
riskClass)` calls `m.Affected`, then forces `Selection=full`
(`[]Target{TargetAll}`) whenever the affected set could not be computed
(including the independent guard against a producer bug or misconfiguration
that returns an empty set for a non-empty change list — see the wiki page
below), is stale against the caller's candidate tree hash (compared to the
worktree's own real `git rev-parse HEAD^{tree}`), or the risk class is
High/Critical — `Selection=affected` with the real target list is returned
only for Low/Normal risk with a fresh, concrete set. Streaming CI dispatch
may run against a partial affected set; acceptance always re-runs through
this same decision function, never a re-derived copy of it. Full detail:
`.github/wiki/CI-and-Attestation.md`.
