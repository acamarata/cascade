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

### Claude Driver (`providers/agents/claude`)

The Claude driver is a headless **one-shot** lane: each `Spawn` execs the CC harness
binary once, in its leased git worktree and in its own OS process group, and returns
once the child has started. `Collect` blocks until that run has exited and its whole
process group is gone.

**Entitlement guard.** `Spawn` resolves the active profile first and refuses with the
`ErrEntitlement` sentinel, starting no process at all, when the profile's
`ProgrammaticEntitlement` is false. An empty `SpawnEnv.Driver` refuses the same way,
and so does an empty `spec.WorktreeRoot` (the error names the field).

**Spawn seams.** Each job carries its own output filter (`spec.Filter`); a nil filter
refuses with `ErrEgressSubstitutionFailed` before anything runs. The child's working
directory is `spec.WorktreeRoot`, which must equal the root of the tree the lease
names: a different value refuses with `ErrWorktreeRootMismatch` rather than picking
one of the two. A non-empty `spec.JobID` is the job id `Spawn` returns (an id already
live in the driver refuses with a conflict); otherwise the driver mints one.
`Executable()` reports the absolute, symlink-resolved harness path `New` resolved;
`New` refuses a relative or missing path.

**Environment custody.** The child's environment is exactly `SpawnEnv.Driver`, which
the host shim builds from `provider.DriverEnvAllowlist(vendorAuthVar)`; the driver
never reads the daemon's own environment. The driver refuses a key outside that
allowlist, a duplicate key, a `HOME` that differs from the profile's runtime home
(`<state>/agents/profiles/<profile-id>/home`, which must be a real 0700 directory),
and a `vendorAuthVar` value (the harness's config-directory variable) that is not
under that home. `SpawnEnv.Tool` must fit `provider.ToolEnvAllowlist()`, which never
carries `vendorAuthVar`. The harness starts its own tool and hook subprocesses and
passes them its environment; the driver cannot substitute `SpawnEnv.Tool` there (see
LIMIT).

**Worktree guards and pre-spawn scan.** Before any child exists the driver refuses a
runtime home that overlaps the leased tree, any symlink under the tree's `.claude/`
directory, and any symlink whose target leaves the tree. It then runs
`provider.PreSpawnScan` over the whole tree (the child's working directory is the
tree root, so every file is exposed whatever the job's scope says); a hit aborts
with `ErrPreSpawnSecretFound`.

**Hook-pack injection.** The operator's hook pack is copied into the tree's
`.claude/` directory with every created directory checked and every file opened
without following symlinks, so a planted link cannot redirect the copy outside the
tree. A `settings*.json` file (any letter case) already under the tree's `.claude/`
that the pack does not provide refuses the spawn with `ErrForeignHarnessSettings`
before any harness process runs: repo-owned hooks there would run with the vendor
auth environment. The probe and the job run with `--setting-sources project`, so
the pack's `settings.json` is the only settings file the harness loads.

**Credentials.** A profile names one credential class. Class `oauth_token` puts
`CLAUDE_CODE_OAUTH_TOKEN` beside the config-dir variable; class `api_key` puts
`ANTHROPIC_API_KEY` there and the argv carries `--bare`. Any other key variable, a key
variable that does not match the class, or one without the config-dir variable refuses
the spawn before a child exists. Tool environments never carry either.

**Harness probes.** The driver runs `--version` (a failure or an unparsable version
refuses the spawn; there is no configured fallback) and a one-turn stream-json probe
whose `system/init` event must match protocol revision 1 (see
`testdata/README.md`). An MCP server that is not `connected`, a plugin entry that is
not a plain `{name, path}`, a plugin directory that loaded nothing, or a permission
mode that does not match the tool policy refuses the spawn. The probe is killed as
soon as its init line arrives.

**Invocation.** `-p --output-format json --no-session-persistence --session-id <uuid>
--strict-mcp-config --setting-sources project`, one `--add-dir` per in-scope prefix,
`--permission-prompts none`, the tool-policy flags (`none` and `readonly` disallow
the write and execute tools and keep the default permission mode; only `all` gets
`--permission-mode bypassPermissions`), deny rules for the harness's file tools on the runtime home,
then each `DriverDefaults` flag only when it is set, `--bare` and `--max-budget-usd`
only for api_key lanes, and the prompt last after a literal `--`. A flag below its
minimum harness version refuses the spawn. A flag the probed `--help` does not list
(`--append-subagent-system-prompt-file`, `--max-turns` on 2.1.273) is unsupported: a
`SubagentKernelPromptFile` or `MaxTurns` value refuses the spawn with
`ErrHarnessIncompatible` before any process runs. The session name is the
ticket id (else the job id); the session id is `provider.SessionIDFor` of the job id; stdin is the null device, so `Message` on a known job
answers `ErrMessageUnsupported` and writes nothing to the child.

**Output custody.** Every stdout and stderr chunk passes through the job's
`spec.Filter` before anything buffers it. A filter error withholds that
chunk and all later output, cancels the child through the process-group ladder, and
fails the run with `ErrEgressSubstitutionFailed`. Output past 16 MiB per stream fails
the run the same way.

**Collection.** `Collect` decodes stdout as the single result object, in memory
(`session_id`, `structured_output`, and `total_cost_usd` recorded as a
`cli-observation` estimate, never billing truth). The result yields no tool event:
per-tool rows come only from authenticated callback frames. The persisted journal is the closed field set only: tool name,
duration, exit code, worktree-relative paths, result hash, event kind, sequence, data
class and protocol version. No prompt, model output or raw transcript is kept, and a
raw-stream request returns `ErrTranscriptPersistenceForbidden`. The driver also opens
a per-spawn callback channel: a unix socket announced, with a 128-bit token, in the
0600 `provider.CallbackFileName` file under `.claude/`; frames are
`provider.EncodeCallbackFrame` lines carrying a `provider.CallbackPayload`. A frame without that token is refused before it is parsed. An
authenticated frame must fit the closed shapes (tool name `[A-Za-z0-9_.:-]{1,128}`,
result hash empty or 64 lowercase hex, clean relative paths of at most 4096 bytes with
no `..` or control characters); its tool name and paths pass through the
job's filter before they are kept. A frame that breaks a shape, a filter error, a
sequence regression, or more than 10000 rows or 16 MiB of frames fails the run and
stops the job. The P/S-34.T1 hook pack does not read the token file or post to this
channel yet, so a real run records no callback rows until it does.

**Canary.** Every spawn plants a random credential canary in the runtime home. If it
appears in the run's output, stderr, callback frames, journal, artifact names or the
content of any file the job created or modified, in exact, base64 or hex form, the run
fails closed; the error never contains it. A modified path that cannot be read, or is
not a regular file, directory or symlink, fails the run the same way.

**Cancel.** `Cancel` sends SIGTERM to the process group, waits `CancelGrace`, sends
SIGKILL to the group, and reports cancelled only once the group is confirmed empty;
otherwise it returns `ErrCancelUnconfirmed` and the job stays cancelling while the
driver finishes the ladder. A clean exit reads `accepted` (R-14.316). A `Cancel`
that arrives after the harness has exited sends no signal and leaves the run's own
verdict in place.

**LIMIT (read this before assuming the egress is filtered).** Worktree minimization
and the pre-spawn scan are the controls on what the CC binary can read. The
`OutputFilter` covers driver **stdio only**: the harness opens its own TLS connection
to the vendor and reads worktree files the substitution pass never sees. The
harness's own tool subprocesses inherit its environment, including the config-dir
variable and the key variable. After the harness exits, the driver inspects only the members still in the
job's process group and fails the run when one carries the config-dir variable. It does not see
a descendant that called `setsid()` and left the group (known limit), nor a tool
subprocess that exits while the harness is running. No document or help text may
claim this driver's egress is filtered or its tool subprocesses isolated. The residual
is the recorded deferral `DEF-P2-driver-tool-isolation` (see `docs/security-posture.md`,
Agent driver custody): `SpawnEnv.Tool` is journaled by name only and is not delivered
to tool subprocesses.

**Wiring.** The host shim that fills `SpawnEnv`, `WorktreeRoot`, `JobID` and the
per-spawn filter, binds the real lease and profile registry, and constructs the driver
in the running daemon is the AD/S-62.T3 ticket; until it lands the driver runs only under
tests and the opt-in conformance run.

### Harness binding table

`internal/providers/agents` binds each external harness to the plugin that owns
it. The host shim resolves a lane through it; a harness whose plugin is absent
or disabled is unavailable and never replaced by another one.

| Harness | Owning plugin | Lane |
|---|---|---|
| claude | `cascade-claude` | `agent:claude` |

The local model lane is not a harness and is never registered here. Field map
and refusal rules for the spawn envelope: `docs/reference/spawn-envelope.md`.

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
