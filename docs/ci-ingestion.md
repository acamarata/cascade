# CI ingestion

`internal/ci` ingests GitHub Actions workflow run results into the
`ci_results` cascade.db domain (R-16.75) via a REST polling client. There is
no webhook receiver in this build; see "Webhooks" below.

## Configuration

| Setting | Source | Notes |
|---|---|---|
| GitHub token | operator-supplied credential, read through the vault | never hardcoded; the polling client never logs it |
| Polling interval | cadence owned by the scheduler that wires this package's `Client` in (not yet composed in this build) | |
| Supported workflow event types | every event a `GET /actions/runs` page returns; the normalizer does not filter by `event` | |

## Status vocabulary

`ci_run`/`ci_job`/`ci_step.status`: `queued`, `in_progress`, `completed`, or
`unknown` for anything else (fail-closed).

`conclusion`: `success`, `failure`, `cancelled`, `skipped`, `timed_out`, the
empty string (still running), or `unknown` for anything else, including
GitHub's own `action_required`/`neutral`/`stale` values -- those are
documented by GitHub but are not members of this domain's canonical
conclusion enum.

## Webhooks

Inbound GitHub webhook delivery is **not implemented** and is not planned for
this release. It is recorded as the deferral `DEF-P2-github-webhooks` in
`.claude/planning/p1/phase/deferrals.yaml`: this build's daemon IPC listener
is a UNIX-socket HTTP endpoint, which GitHub cannot deliver a webhook to, so
P1 ingests CI results through the authenticated polling client only. No CLI
command, config key, or other document in this repository claims webhook
ingestion exists.

## Health

A future `cascade doctor` check reports the ci-poll egress class's
registration state and, on Windows, that the polling scheduler refuses to
run without a daemon present -- this build does not yet compose the polling
client into a running scheduler (see `internal/ci/domain.go`'s package doc
comment for the composition-root wiring this leaves to a later ticket).
