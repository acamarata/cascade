# CI acceptance

The `reviewing` to `accepted` edge requires a passed acceptance re-run for the job's current checkpoint. Passing streamed evidence alone cannot accept a job.

## Exact checkpoint and tree

`Dispatcher.AcceptanceRerun` checks the requested checkpoint against `CurrentCheckpoint` before creating any run. A stale checkpoint returns `ErrCheckpointStale`; a different tree returns `ErrTreeHashMismatch`. Acceptance uses the recorded checkpoint commit's tree, independent of subsequent index or worktree edits. A stream-only snapshot or a snapshot tree that differs from the commit tree is refused. Declared untracked files do not become acceptance inputs; tests that require an uncommitted path fail until it is committed.

Target selection reads the stored checkpoint tree. High and Critical risk, a stale plan, or an uncomputable plan requires the full kind set. Concurrent acceptance re-runs of one checkpoint return `ErrAlreadyRunning`; a finished re-run returns its cached result without adding run rows.

## Completion gate

Production sets both `CompletionPolicyDeps.Acceptance` and `RequireAcceptance`. Construction fails if acceptance is required without a verifier. The accepted edge checks evidence and approval, then synchronously verifies acceptance before committing the state change. A verifier error, failed verdict, empty request checkpoint, wrong checkpoint or wrong tree denies with `ci-acceptance-not-passed`, publishes one `jobs.gate.denied` event and leaves the stored job reviewing.

`completion.check` reads the checkpoint and tree from the daemon's single CI dispatcher. A missing checkpoint, checkpoint lookup error or empty stored risk class refuses the accepted edge without calling the transition. Earlier edges retain their existing behavior. The daemon verifier uses that same dispatcher and waits for every acceptance sub-job to terminate, with a 30-minute limit measured by the injected clock. Timeout cannot pass acceptance.

Gate registration refuses a daemon verifier whose dispatcher differs from the checkpoint source, before registering the hook pack or RPC handler.

## Evidence producer fence

The completion hook pack uses `FleetRuntime.Leases()` as its producer authorization fencer. It resolves the controller binding on every authorization call and permits controller work only when `nodes.ResolveRole` returns `RoleController` without error. Stale lease epochs and non-controller roles cannot append trusted evidence. The hook pack creates no separate lease authority.
