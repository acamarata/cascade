# CI executors

Every CI sub-job (one requirement kind of one checkpoint) is placed by the executor resolver in `internal/ci` before it runs. The resolver decides where the code runs and what it may see. It never pushes, creates a ref or uploads anything.

## Resolution order

`Resolver.Resolve` tries three executor kinds in order and returns the first that may run the job:

1. **local**: a fresh checkout of the snapshot tree on the controller. Chosen when a local runner is registered, the job needs no capability beyond `clean-room`, its toolchain fingerprint (if it names one) matches the controller's, and its source is trusted.
2. **node**: an enrolled node, placed through `Fleet.Lease` with the job's sensitivity tier, intent `ci:<kind>`, task class `code`, quality `standard`, deadline `batch`, and a `DispatchRef` carrying the job, repository, project, `Requests: 1` and the placement capabilities (the required set plus `toolchain:<fp>`). Only `ErrNoCapableTier`, `ErrDataClassDenied`, `ErrSensitivityConstraint` and `ErrNoAllowedLanes`, matched by identity, fall through to the next step. Any other lease error fails the sub-job. Node eligibility is the fleet's own sensitivity and node trust filters.
3. **github-actions**: a hosted run, only under the hosted guard below.

If none applies the result is `ErrNoCapableExecutor`, or `ErrExternalSourceOnController` when untrusted source was refused locally and nothing else could take it.

## Capability set

The capability set is closed: `clean-room`, `docker`, `xcode`, `gpu`. Requirement kinds map onto it:

| kind | required | toolchain |
| --- | --- | --- |
| format, lint | none | no |
| compile, unit | none | yes |
| integration, security, architecture | clean-room | no |

The local executor advertises `clean-room` only. Docker, xcode and gpu are never local.

## Toolchain fingerprint

`ComputeToolchainFingerprint` is lowercase hex sha256 of `go version`, `uname -sm` and the sorted `[ci].toolchain_versions` list (default empty). The two probes run once per process. The daemon installs the list once, at start, so a changed `[ci].toolchain_versions` is applied at daemon start; re-installing it on config reload arrives with P1-NODE-08. On Windows the fingerprint is refused with `ErrPlatformUnsupported` (tier 2). Whenever the fingerprint cannot be computed, a toolchain-bound job (compile, unit) ends as the explicit non-pass below instead of retrying.

## Runner registry

A `RunnerSet` maps an executor kind to its `Runner`. A kind with no registered runner is never resolved and is journaled as skipped on every resolution. The daemon registers exactly the local runner. Node and GitHub Actions runners arrive with P1-NODE-08 and P1-NODE-09.

## Hosted guard

GitHub Actions is chosen only when all of these hold:

- a github-actions runner is registered;
- the job's tier is `public`;
- the repository's `origin` is `https://github.com/<owner>/<repo>[.git]` or `git@github.com:<owner>/<repo>[.git]`;
- the GitHub reader says the repository is public. An error, an unknown answer or no reader at all is private. The daemon has no reader today, so hosted is always refused there;
- the checkpoint commit already exists on GitHub.

Both reads go through the `ci-poll` egress gate and the never-pay route policy, so a repository `[ci.policy]` marks private is refused before any request leaves the process.

## Source trust

Code of external source is never verified on the controller (ADR-0009 section 6). Anything but `trusted` provenance, including an empty value, is external. External code may run only on an ephemeral executor: a node worktree is not one, so it is refused there too, and only the hosted runner remains. The daemon treats a job of its own job store as trusted (the owner's repository). A job it cannot find fails the sub-job.

## Provenance

Every result produced through the resolver carries an `Environment`: the runner kind, the isolation class (`clean-checkout-same-uid`, `node-worktree` or `hosted`), whether the environment is ephemeral, the node id and the toolchain fingerprint. The resolver sets it; a runner cannot override it.

## Non-pass outcome

A sub-job no executor may run, or one whose runner cannot run its kind (for example a kind with no `[ci.local]` command), ends as an explicit non-pass: one `ci_run` completed with conclusion `skipped`, named like the stream run so a redispatch reuses it, one `ci_job` naming the reason, `Passed=false` and executor kind `unresolved`. It is never a pass and never runs another command in its place.

## Local clean room residual

The local executor runs as the same user as the daemon (R18 M6). Its clean room is a fresh checkout with a private HOME, TMPDIR and build cache, but the process can still reach anything that user can, including the shared module cache. That is why external source never runs locally.
