# Telemetry

What this release ships for telemetry, and what it does not.

## Disposition

P1 ships two things: this document, and the `[telemetry]` config section
in `config.toml`. Nothing else. There is no telemetry endpoint, no
transport, no collector, no sampling, and no emission code anywhere in
this tree. Telemetry endpoint and transport are explicitly deferred to a
later release. Until that lands, enabling `telemetry.enabled` changes
nothing observable: no config format the shipped binary sends anything,
because the code path that would send it does not exist.

## Shipped default

Telemetry is off by default. A fresh install, a fresh `config.toml`, and
every profile resolve `telemetry.enabled = false` with zero user action.

```toml
[telemetry]
enabled = false   # opt-in only; shipped default off
```

The only supported opt-in paths are:

- an explicit edit of `telemetry.enabled = true` in `config.toml`, or
- the init wizard's telemetry prompt (step 7), whose default answer is
  No.

There is no third path. Environment variables cannot enable telemetry;
see below.

## Hard-disable

`CASCADE_TELEMETRY=0` forces `telemetry.enabled = false` regardless of
whatever `config.toml` says. This is an unconditional override: it wins
over an explicit `enabled = true` in the file.

`CASCADE_TELEMETRY` has no enable-side effect. No value of it turns
telemetry on. Setting it to `1`, `true`, or anything other than the
disable value changes nothing; the only defined behavior is disabling.

## Never force-enable

The generic per-section environment override
(`CASCADE_<SECTION>__<KEY>`, here `CASCADE_TELEMETRY__ENABLED`) may only
narrow telemetry from on to off. It can never widen telemetry from off to
on. A `CASCADE_TELEMETRY__ENABLED=true` set in an environment where
`config.toml` has telemetry off is refused: the resolved value stays
`false`.

The rule, stated once: no environment input, of any shape, can be the
reason telemetry ends up enabled. Enabling is always a config-file edit
or a wizard answer that a person made on purpose.

## Config reference

| Key | Type | Default | Reload class |
|---|---|---|---|
| `telemetry.enabled` | bool | `false` | hot |

The section is hot-reloadable: a daemon picks up a `telemetry.enabled`
edit on the next config reload, under the same whole-file
validate-before-apply rule every other hot section uses. An invalid value
(anything other than a boolean) is rejected with a typed config error
naming `telemetry.enabled`; the file is not silently coerced or ignored.

## What is not here

- No telemetry endpoint or transport. Deferred.
- No collector, sampler, or metrics emitter for telemetry specifically.
  (In-product operational metrics such as `cascade top`'s live resource
  view are a separate, unrelated surface and are not telemetry.)
- No CLI verb. There is no `cascade telemetry ...` command. The only user
  surface over this key is the general-purpose `cascade config
  get/set/list`.
- No egress registration. The egress inventory names a telemetry class
  for future use, but nothing transits under it in this release: nothing
  is collected, and nothing leaves the machine.

## Crash reporting

Crash reporting, when it exists, follows the same posture: opt-in only,
off by default. No crash reporter ships in this release; this section
states policy for if and when one is built, not a shipped capability.

## Outcome telemetry (Cascade Intelligence)

This is a SEPARATE thing from the `[telemetry]` section above: a
purely local, on-disk record of one's own job outcomes that never
leaves the machine, is not the `[telemetry]` opt-in transport, and has
no relationship to the "What is not here" list above (no endpoint, no
egress class, no collector). It exists so a future learning pass can
read aggregate signal about how the local daemon's own jobs have gone.

**Domain and migration owner.** The two tables live inside the existing
`jobs` domain (`jobs_` table prefix) under their own migration SetID
`"learn"` (internal/learn/migration.go), not a new storage domain. The
jobs domain has exactly one reservation ledger, `jobs_reservation`,
owned by a separate ticket -- this package never creates a second one.

**Allowlisted fields only.** `jobs_telemetry_outcomes` persists exactly:
`task_class, repo_id, language, component, risk_class, lane_tier,
node_id, scope_ref, context_size_tokens, retrieval_strategy,
duration_ms, queue_time_ms, retry_count, ci_failure_count,
rework_cycles, final_outcome, rollback_at, regression_detected,
cost_tokens, quota_units`. No raw transcript, prompt text, tool output,
or error string is ever persisted, journaled, or hashed-and-kept. A
struct field whose name matches Prompt/Text/Content/Message/Input/
Query/Response is a build-time test failure
(`TestNoPromptTextInvariant`), and a planted credential-shaped value in
any field refuses the write closed (`TestCredentialCanary`). The check
uses the secrets detector's default registry (patterns only, so an
opaque hex id still passes): every class it recognises is refused in
every outcome and finding input (`TestCredentialCanaryEveryRegistryClass`).

**Opaque identifiers.** The writer checks every identifying column
before any SQL runs and refuses a value that breaks its rule with a
`KindInvalidInput` error, leaving no row behind
(`TestIdentifyingValuesRefusedBeforeStore`). `repo_id` and `node_id`
are opaque ids: up to 64 letters, digits, underscore or hyphen, so a
path, URL, e-mail address or host name cannot match. `job_id` adds dot
and colon; a job id that does not fit (a free-text planner id, an id
over 64 characters) is stored as an opaque `job-` digest id by both
writers, while the `jobs_usage` join keeps the raw id. `task_class`,
`risk_class`, `lane_tier`, `retrieval_strategy` and `component` are
bounded labels: up to 64 letters, digits, underscore, plus or hyphen,
with no whitespace, dot, slash, colon or at-sign, so a host name or IP
address cannot match. `scope_ref` follows the opaque-id rule (the
writer only ever produces `scope-` plus a hex digest), so no percent,
equals or plus escape can carry a locating string. `language` and `final_outcome` are closed
enums, and a finding `count` is never negative.
Empty values are allowed (a neutral default stores nothing
identifying). `TestTelemetryNoLongStrings` scans every column of both
tables and fails on any stored string over 64 characters that is not
a declared enum value or an id. The reconciler always stores a job's
non-empty mutable scope as an opaque `scope-` digest id, and maps a
class string that does not fit the label rule or carries a credential
to `unknown`, so one odd job cannot stop a reconcile pass. A job whose
id is credential-shaped is refused by the writer: the pass skips it,
records every other terminal job, and then returns a `KindInvalidInput`
error carrying only the count (`TestReconcileNeverWedgesOnOneBadJob`).

**Errors never carry a rejected value.** A validation error names the
field and the rule, never the value, because the value may be the
secret or locating string the rule exists to keep out. The credential
scan runs over every string input before any enum decoder or shape
rule, and the enum decoders and the finding writer echo nothing
either (`TestValidationErrorsOmitRawValues`).

**Structured findings.** Tool failures, review findings, adversarial
findings, and human corrections are rows of `jobs_telemetry_finding`:
`{family, category, severity, count}` over closed enums with
fail-closed decode. There is no free-text parameter anywhere on the
finding-writing path.

**scope_ref.** `jobs_telemetry_outcomes` carries `scope_ref` (an opaque
id for the scope the job ran under) so repo and component identity
never leaks into a globally scoped learned config.

**Export.** Both telemetry tables, plus `jobs_reservation` (the one
reservation ledger), are excluded from `cascade backup export` by
construction: `Export` (internal/storage/export.go) reads only the
shared kv table, never an arbitrary SQL table, and it derives its
excluded-table set from `storage.JobsDomainExcludedTables()`, which
names all three (R-21.162): a source table on that list is never read.

**Retention.** `[learn].retention.max_age_days` (int, default 90, hot
reload class) bounds `jobs_telemetry_outcomes`' age. `RetentionSweep.Run`
re-reads it on every call, checks the shape of every registered table
that exists in the database (a registered table with no such integer
column refuses the whole run with `KindInvalidInput` before any
delete), then in ONE transaction deletes each registered child's rows
for the outcome rows it retires, and then the aged rows of every
registered table. Any error rolls the whole batch back. Two functions
register tables, both callable from a package `init()`:

- `learn.RegisterRetentionTable(table, timeColumn string) error`
  registers a table swept by age against `timeColumn`.
  `jobs_telemetry_outcomes` registers itself with `created_at`.
- `learn.RegisterRetentionChild(table, parentIDColumn string) error`
  registers a table keyed by `jobs_telemetry_outcomes.id`.
  `jobs_telemetry_finding` registers itself via `outcome_id`, and every
  extension table of the additive rule (a new table keyed by the
  parent row id) joins the same way, so a foreign key from a child
  never blocks the parent delete.

A malformed identifier, a duplicate, or a learn-owned table with no
such column (`jobs_telemetry_finding` has no time column) refuses
with `KindInvalidInput`. A table registered by age that has a foreign
key to `jobs_telemetry_outcomes` refuses the first sweep the same way,
before any delete, and the error names `RegisterRetentionChild`. The `learn-retention` runnable this drives is
not registered on a live daemon scheduler in this release; that wiring,
and the matching `learn-outcome-reconcile` runnable, land with the
ticket that owns the daemon's scheduler composition root. Until then
`RetentionSweep` and `OutcomeReconciler` are tested code with no
production caller.

| Key | Type | Default | Reload class |
|---|---|---|---|
| `learn.retention.max_age_days` | int | `90` | hot |
