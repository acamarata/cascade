# `cascade github ci`

Wait for GitHub Actions checks to go green, and optionally merge the pull
request once they do. Both verbs are `github`-noun commands (07-CLI-COMMAND-
TREE §github, note 5, R-14.76) — plugin-contributed, not `cascade ci` core
verbs (`cascade ci run`/`status`, see `ci.md`, stay core per R-16.31).

```
cascade github ci wait [--repo <owner/repo>] [--ref <sha|branch>] [--timeout 30m]
cascade github ci merge-on-green [--repo <owner/repo>] [--ref <sha|branch>] [--pr <number>] [--yes]
```

> **Status.** Both verbs are mounted in the shipped binary (see "How these
> verbs are mounted" below). `cascade github ci wait` runs; `cascade github ci
> merge-on-green` refuses today with a typed `unavailable` error naming the
> one missing prerequisite — see "What merge-on-green does today".

## `cascade github ci wait`

Blocks until every **required** check on `--repo`'s `--ref` (a branch name
or a commit SHA) reports `success`, then exits zero.

| Flag | Meaning |
|---|---|
| `--repo` | `owner/repo` (default: the current checkout's `origin` remote) |
| `--ref` | branch or head SHA to watch |
| `--timeout` | how long to wait before giving up (default `30m`) |
| `--required-check` | repeatable: the check names that must succeed (default: every non-skipped job of the run, once the run itself completes) |
| `--allow-skipped` | repeatable: required check names whose `skipped` conclusion counts as green |

It needs a GitHub API token in `CASCADE_GITHUB_TOKEN`, `GITHUB_TOKEN` or
`GH_TOKEN`, and refuses with a typed error naming all three when none is set.
The `cascade-github` plugin's own vault-stored token is held by a plugin
process this build cannot launch, so it is not an alternative today.

It polls the same real, never-pay-guarded `internal/ci.Client` (S-51.T2)
`cascade ci status` reads from — never a call into the `cascade-github`
plugin process. Required-check cardinality defaults to every non-skipped
job on the matched run; a config-level override for an explicit check-name
list is a composition-root task, not yet wired (see `waitmerge_wait.go`'s
own scope note).

Exit behavior:

| Condition | Result |
|---|---|
| every required check reports `success` | exit 0 |
| the run itself is still `in_progress` | keep waiting, however good the jobs that already reported look |
| a required check has not reported yet | keep waiting (missing is never green) |
| a required check concluded `skipped` and is not in `--allow-skipped` | exit non-zero, immediately |
| one poll fails transiently (a 502, a dropped connection) | retried with exponential backoff until `--timeout` |
| the repository routes to the local gate, or no never-pay policy is configured | exit non-zero before any request |
| any check reports `failure`, `cancelled` or `timed_out` | exit non-zero, immediately — no further polling |
| any check reports an unrecognized conclusion | exit non-zero, immediately (fail-closed, 06 §5.20) |
| `--timeout` expires first | exit non-zero with a timeout message |

## `cascade github ci merge-on-green`

Runs the same wait, then — only on green — merges `--pr`.

| Flag | Meaning |
|---|---|
| `--repo`, `--ref` | as above |
| `--pr` | pull request number to merge |
| `--yes` | skip the interactive confirmation (still subject to the grant check below) |

This is an **L3** action (06 §5.15: external side effect). It refuses
outright — never auto-advances, per R/S-39.T2's L0/L1 auto-advance ceiling —
unless an explicit `merge-on-green=true` policy grant is present for the
calling subject (`internal/policy.Engine`, the real classifier + deny-list +
grant seam, I/S-17.T1–T4). Every decision, allow or deny, is logged as a
`policy.decide` audit event (I/S-18.T2). Absent the grant:

```
$ cascade github ci merge-on-green --pr 42
error: policy denied: ci: merge-on-green refused for agent:lane-a:
no merge-on-green=true grant (...)
```

On an allow verdict, the merge is dispatched as
`cascade-github.prs.merge` through the real plugin-host call path
(`internal/plugins/ci_waitmerge_wiring.go`, R-21.270): a JSON-RPC request
over the O/S-31.T3 `ProcessRuntime` `Handle`'s stdio transport — never
`model.execute`, which is the ONLY model door for plugins
(02-TARGET-STRUCTURE.md). A stale wait result (one resolved for a different
`ref` than the merge targets) is refused before any evaluation runs.

## Windows tier-2

`cascade github ci wait`'s poll loop needs no daemon, but on Windows it
still refuses with a documented, typed error rather than attempting to run.
A `cascade doctor` probe for that tier-2 status is **not wired** (the same
wording `plugins/github/README.md` uses); adding it to `internal/doctor` is
a follow-up, not part of this ticket.

## What merge-on-green does today

The plugin-host prerequisite is checked FIRST, before any authorization work:
without a live `cascade-github` process there is nothing to dispatch
`prs.merge` through, and every process-tier launch is refused at
`process.ProcessRuntime.Launch`'s trust gate (`internal/plugins/dispatch.go`'s
`ProvisionElevated`) because no mechanism marks a manifest trusted yet. So:

```
$ cascade github ci merge-on-green --repo acamarata/cascade --ref main --pr 42 --yes
error: unavailable: plugins: `cascade github ci merge-on-green` needs a live
cascade-github process to dispatch prs.merge through, and this build has no
trust-elevation path that can launch one
```

That is the honest answer, not a placeholder success. The rest of the path —
the L3 classification, the distinct grant, the audit pair, the data-class
ceiling and the moved-head re-check — is implemented and tested; it becomes
reachable the day trust elevation lands, with no change to this surface.

## How these verbs are mounted

`cascade-github` is a **process-tier** plugin (`runtime = "process"`), and the
builtin registry's cobra mount (`internal/plugins/registry.go`'s
`BuiltinRegistry.NewCobraCommand`) serves `runtime = "builtin"` only. The
process tier has its own mount, `cmd/cascade/plugin_process_mount.go`, which
turns a manifest command name into a command path:

| manifest `provides.commands` name | command |
|---|---|
| `github-repos` | `cascade github repos` |
| `github-issues` | `cascade github issues` |
| `github-prs` | `cascade github prs` |
| `github-ci-wait` | `cascade github ci wait` |
| `github.ci.merge-on-green` | `cascade github ci merge-on-green` |
| `github-ci-watch-add` | `cascade github ci watch add` |
| `github-ci-watch-list` | `cascade github ci watch list` |
| `github-ci-watch-remove` | `cascade github ci watch remove` |

Segments are separated by `.` when the name contains one (so a leaf verb may
itself contain hyphens) and by `-` otherwise. The rule is documented for
plugin authors in `.github/wiki/Plugin-Author-Guide.md`. The `ci wait`/`ci
merge-on-green`/`ci watch add|list|remove` verbs are all HOST-implemented
(`cmd/cascade/github_ci_cmd.go`, `cmd/cascade/github_ci_watch_cmd.go`): their
`RunE` reaches `internal/ci`/`internal/runtime`, never the plugin process.
The three T1 verbs have no host implementation, so they return the same
typed process-tier refusal `merge-on-green` does.

## `cascade github ci watch add|list|remove`

Watches a repository's CI results, routing real failure conclusions
(`failure`/`cancelled`/`timed_out` — never a wait-loop timeout or a context
cancellation) observed by `cascade github ci wait` into the
`fleet.attention` queue, visible via `cascade fleet attention list`.

```
cascade github ci watch add <owner/repo> [--branch <glob>] [--workflow <glob>]
cascade github ci watch list
cascade github ci watch remove <owner/repo>
```

| Flag | Meaning |
|---|---|
| `--branch` | only route failures on branches matching this glob (`path.Match` semantics); default: every branch |
| `--workflow` | only route failures of workflows whose NAME matches this glob (`path.Match`); default: every workflow. Job names are never matched here — they appear in the routed entry's `failed_jobs` |

**Idempotent**: `add` on an already-watched repo updates its branch/workflow
filters (exit 0, delta reported) rather than creating a second entry;
`remove` of an unwatched repo exits 0 reporting a no-op.

**Persistence**: entries live in `config.toml`'s `ci.watch` key — a single
array of inline tables, e.g. `ci.watch = [{repo = "acamarata/cascade",
branch = "main", workflow = "build-*"}]` — a hot-reloadable key (`[ci.watch]`
is not in `hotreload.go`'s cold-sections list, matching `[ci.policy]`/
`[ci.local]`'s own precedent).

**How a failure reaches the queue**: two producers, one core
(`internal/ci.RouteFailure`).

1. `cascade github ci wait` AND `cascade github ci merge-on-green` both go
   through the single `waitAndRoute` hook, which calls
   `internal/ci.RouteWaitFailure` on every result. It routes ONLY when
   `WaitOnGreen`'s error carries `KindConflict` — its own taxonomy for "the
   RUN concluded something other than success" — never on a wait-loop
   timeout (`KindTimeout`) or context cancellation (`KindCanceled`), neither
   of which is itself a CI conclusion.
2. `internal/ci.RouteCIResults` subscribes to the `ci_results` event-bus
   namespace and routes every completed-run event that failed. It is
   implemented and tested against a real event bus but not yet STARTED: the
   daemon composition root has no daemon-lifetime context to cancel it with
   (recorded in `internal/build/testonly-allow.json`).

**The routing decision is the RUN's conclusion**, not a per-job one: any
conclusion other than `success` routes (so a `startup_failure` run with zero
jobs, and a `failure` run whose jobs are all success/skipped, both route),
and an unrecognised conclusion routes fail-closed rather than being dropped.
A run that has not concluded never routes.

On a match one item is queued, whose `source_ref` is the run's identity
`ci:<owner>/<repo>:<run_id>` — also the queue's dedup key, so one item per
failed run no matter how many times it is observed. The full detail (repo,
ref, run_id, conclusion, sorted `failed_jobs`, the computed
`https://github.com/<repo>/actions/runs/<run_id>` link and the
`cascade://ci/<owner>/<repo>/runs/<run_id>` deep link) is returned to the
caller and readable from the `ci_run`/`ci_job` rows for the same `run_id`.
A repo listed in `[ci.policy.repos].private` is filed at PROJECT scope
rather than global, and the push runs through `supervision.RoutePush` so the
data-class check applies. Routing is best-effort observability: a routing
failure is logged and never changes the verb's own exit code.

**Windows tier-2**: `watch add` refuses through `ci.PlatformRefusal`, the
same build-tag-split verdict `wait`'s own gate consults (never a runtime
branch), and without opening the runtime store as a probe side effect;
`list`/`remove` stay available everywhere (plain config-file operations, no
daemon dependency).

**A hand-written `[[ci.watch]]` array-of-tables header** loads (both TOML
shapes decode identically) but cannot be updated by the line-oriented
writer: `watch add` then refuses with a typed `KindInvalidInput` error
naming the supported `watch = [{...}]` inline form and leaves the file
untouched.

**Honest gaps**: no `fleet.attention.push` RPC method exists yet (only
`list`/`get`/`ack` are registered), so the production pusher opens the same
runtime store `merge-on-green`'s policy engine already opens directly from
the CLI process rather than dialing the daemon; the `ci_results` subscriber
is not started by any composition root yet; `cascade doctor` does not yet
report `watch add`'s Windows tier-2 status. Full accounting in
`internal/ci/attention.go`'s header note and `plugins/github/README.md`.
