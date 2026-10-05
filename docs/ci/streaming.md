# Streaming CI

A leased job checkpoints at each commit. Each checkpoint runs the CI kinds its risk class requires as sub-jobs against that exact commit, while the agent keeps working. This page describes the dispatcher in `internal/ci` (`stream.go`, `stream_dispatch.go`, `stream_resume.go`, `stream_local.go`).

## A checkpoint is a commit

`Dispatcher.Checkpoint(ref, model)` is bound to `JobRef.CheckpointCommit`, never to the live HEAD, the worktree index or the worktree files.

- The tree hash is `git rev-parse <CheckpointCommit>^{tree}`, read from the object store.
- The worktree manager's snapshot hashes the index, which can hold edits staged after the commit. Its tree is used only when the job declares untracked paths, and only after the dispatcher proves the tree differs from the commit tree by declared paths alone. Any other difference is `ErrTreeHashMismatch`.
- Changed paths come from `ChangedPathsTree(repoRoot, base, tree)`, a `git diff-tree` between the base commit and the captured tree. A commit made later cannot change them.
- Risk is re-derived with `jobs.Reclassify` over those paths. Risk only rises: the class the lease's current attempt ran under is the floor, so a caller passing a stale lower class cannot lower it.
- The lease scope is checked against the same changed paths. A path outside the scope returns `ErrScopeViolation`, raises one attention item and dispatches nothing, at every risk class.
- Target selection runs over a detached checkout of the commit (`git worktree add --detach`, hooks disabled, removed after planning). Affected targets, `affected_cmd` and the freshness check therefore read only the commit tree.

## Stream-only snapshots

A job may declare untracked paths (R-21.147). The snapshot then includes exactly those paths and is stream-only. Stream runs may use it. An acceptance dispatch refuses it with `ErrTreeHashMismatch`. Acceptance always uses a commit-only checkpoint, and an acceptance run for a job that declared an untracked path fails with `ErrTreeHashMismatch` naming the path, until the path is committed.

## Identity, fence, attempts

- `CheckpointIDFor(job, commit, tree)` is `hex(sha256(job|commit|tree))`.
- Order of `Checkpoint`: fence, sensitivity floor, snapshot, checkpoint id, tombstone the previous attempt, changed paths, reclassify, scope, select targets, one outbox intent per required kind, then the sub-jobs in parallel.
- The fence is the lease manager's own `Fence`. A stale epoch returns `jobs.ErrLeaseFenced` unchanged, raises one attention item, and writes no `ci_run`, outbox or attempt row.
- The epoch is presented again before each sub-job starts and before its result is recorded. A lease reclaimed in between tombstones the attempt, records no `ci_run_stream` marker, publishes no terminal event and fires no `OnTerminal`; `Resume` counts the rows as dropped.
- A sub-job result (`ci_run`, `ci_job`, `ci_step`) is written in one transaction, so a completed job always has its step rows. An attempt with no recorded dispatch (a crash right after it opened) is not treated as done: a retried `Checkpoint` dispatches it once.
- A job whose tier is less restrictive than the stored tier gets `ErrSensitivityLowered` before any effect. The tier is `provider.SensitivityTier`; it reaches the sub-job, the `ci_run_stream` row and both event payloads.
- `ci_stream_attempt` keeps one current attempt per lease. A new checkpoint tombstones the previous one. A result for a tombstoned attempt returns `ErrLateResult` and changes no dispatcher row. Repeating the current checkpoint is a no-op; repeating a superseded one is `ErrCheckpointStale`.
- The table stores, besides the contract's columns, a `dispatches` JSON column: the recorded dispatch requests (ref, snapshot, plan, kinds) that `Resume` needs to re-dispatch after a restart.

## Required kinds

`plan.Requirement` comes from the risk class's gate set: format, lint (static and lint gates), compile (build), unit (targeted tests), integration (integration checks), and architecture plus security (affected or full integration CI, High and Critical). The sets are additive, so a risk rise dispatches a larger set.

## Outbox and Resume

Each required kind has one `ci_dispatch` row in the jobs outbox, keyed by the derived idempotency key over the job, the attempt generation and a hash of (attempt, kind, acceptance). The order is intent, sub-job, effect, confirm. A row is confirmed only after the sub-job's `ci_run` and `ci_job` rows are committed.

`Dispatcher.Resume` replays the unconfirmed rows after a crash:

| Row | Action |
| --- | --- |
| intent, attempt current | re-dispatch once; the executor reuses the reserved `ci_run`, so one run exists per (attempt, kind) |
| effect | confirm only |
| attempt tombstoned | drop (closed with the confirm transition; nothing ran) |
| confirmed | never touched: a terminal sub-job is never re-dispatched |

Before replaying, `Resume` re-records the intents of every recorded dispatch of a current attempt. `RecordIntent` is a no-op on an existing key, so this closes the window between recording a dispatch and recording its intents.

## Results and events

A result writes its `ci_run` and `ci_job` rows through `Execute`, and a `ci_run_stream` row (`via_stream`, `sensitivity`, `checkpoint_id`) through `UpsertRunSourceStream`. `RunViaStream` reads the flag; an absent row is false. `ci.run.completed` carries `via_stream`, true for stream runs and false for `cascade ci run`.

Two events are published on the `ci_results` namespace, each with `{job_id, checkpoint_id, tree_hash, kind, acceptance, via_stream: true, sensitivity, run_id, repo_id, executor_kind}`:

- `ci.checkpoint.dispatched`, once per kind when its sub-job is handed to the executor (`run_id` is 0);
- `ci.checkpoint.terminal`, once per terminal sub-job after its `ci_run` row commits.

`OnTerminal` callbacks fire at the same point. `CIResultEvent.RunID` and `RepoID` select the row.

## The local executor and its clean room

`NewLocalSubJobExecutor` materializes the snapshot tree into a fresh run directory (`git read-tree` into a private index, then `git checkout-index`) and runs the kind's commands there with `Execute`. It never runs a sub-job in the live worktree.

Before each run the controller, not the job, runs `go mod download` then `go mod verify` in the run directory with the ambient environment and `GOTOOLCHAIN=local`. A verify failure refuses the run. The run itself gets:

- a fresh `HOME`, `TMPDIR` and `GOCACHE` under the run directory (and a fresh `GOPATH`);
- `GOFLAGS=-mod=readonly`, `GOPROXY=off`, `GOSUMDB=off`, `GOTOOLCHAIN=local`;
- the shared `GOMODCACHE`.

These are applied after the environment allowlist and win over `[ci.local]` env keys. A module missing from the cache fails the run; a run never fetches one. The run records `Environment.IsolationClass = "clean-checkout-same-uid"`.

Residual: the shared module cache is writable by the same user the run executes as. A job that deliberately writes into it can poison later runs. The cache content is verified by the controller's `go mod verify` before each run, and the same-uid write is a disclosed limit, never a trust input.
