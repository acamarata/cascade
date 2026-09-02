# providers/postgres/testdata — storetest-under-docker lane provenance

This directory has no fixtures. It exists to record the provenance and the
honest scope of the `storetest-under-docker` CI job
(`.github/workflows/ci.yml`), per this ticket's (P1-E02-W1-S03-T5)
`docs_updates` and 12-QUALITY-CONSTITUTION.md Art.2 (real-counterpart
verification claims must be accurate, never implied beyond what actually
ran).

## What the lane runs

- **Image:** `postgres:17`, pulled from the official DockerHub Postgres
  image (`docker.io/library/postgres`), as a GitHub Actions `services:`
  container on the `build-test`-family `ubuntu-latest` runner.
- **Command:** `go test -tags=postgres -count=1 ./providers/postgres/...`,
  which runs `driver_test.go`'s `TestPostgresStore_Conformance` — the
  shared `internal/storage/storetest.RunStoreTests` suite driven against
  `providers/postgres`'s stub `Driver`.
- **Job flag:** `continue-on-error: true`.
- **Added:** 2026-09-02, by P1-E02-W1-S03-T5.

## W1 status: lane-plumbing-only (allowed-fail)

`providers/postgres/store.go` in W1 is a total build-tagged stub
(`//go:build postgres`): every `provider.Store` method returns
`cascade.ErrUnsupported` without touching the wire — `Open` never dials
the container at all. Every `storetest.RunStoreTests` sub-test therefore
FAILS against it, by construction, every time this job runs.

That failure is expected and is why the job carries
`continue-on-error: true` with the job-level comment `allowed-fail W1 —
Q/S-38.T4 carries the passing integration AC` in `ci.yml`. This ticket's
allowed-fail precedent is 06-FORGE-SPEC.md §5.19 (D-2): "where a W1
ticket needs a later subsystem, the leg is allowed-fail and the LATER
ticket's AC carries the integration test."

**What this lane DOES prove in W1:** the `postgres:17` service container
is provisioned and reachable by the runner, and the stub compiles and
runs (fails, honestly) under `-tags=postgres` in that real environment —
lane plumbing only.

**What this lane does NOT prove in W1:** any real Postgres wire behavior.
The stub refuses before it ever reaches the wire, so no query, no
connection-pool behavior, and no error-code mapping is exercised here.
Claiming otherwise would violate Art.2 — this file exists precisely so no
later reader mistakes a green "container reachable" step for a passing
conformance run.

## Q/S-38.T4 (completion reference)

Q/S-38.T4 replaces `store.go`'s stub body with a real `jackc/pgx`-backed
driver in this same package, adds the native-Postgres-error-code ->
taxonomy-`Kind` mapping (`errors.go`, mirroring
`providers/sqlite/errors.go`'s per-code approach rather than collapsing
every failure to `KindUnavailable`), and removes this job's
`continue-on-error: true` once `storetest.RunStoreTests` passes for real
against this same lane. No other change to the job's shape (image,
command, service definition) is expected to be needed.
