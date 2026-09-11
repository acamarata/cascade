# providers/s3/testdata — s3-storetest-under-docker lane provenance

This directory has no fixtures. It exists to record the provenance and the
honest scope of the `redis-storetest-under-docker`-style S3 lane
(`.github/workflows/ci.yml`'s `s3-storetest-under-docker` job), per
12-QUALITY-CONSTITUTION.md Art.2, matching
`providers/pgvector/testdata/README.md`'s precedent.

## What the lane runs (P1-E17-W4-S38-T7)

- **Image:** `minio/minio:latest`, started with `server /data` and a
  fixed root user/password, then a bucket created against it before the
  Go test step runs (MinIO does not pre-create a bucket on boot).
- **Command:** `go test -tags=integration -count=1 -race
  ./providers/s3/... -v`.
- **Env:** `CASCADE_TEST_S3_ENDPOINT` (the server's own `http://host:port`
  root), `CASCADE_TEST_S3_BUCKET`, `CASCADE_TEST_S3_ACCESS_KEY`,
  `CASCADE_TEST_S3_SECRET_KEY` — never a literal committed anywhere; CI
  supplies them as job-level env pointing at the service container's own
  fixed (non-secret, test-only) credentials.
- **Job status:** REQUIRED from the first commit — this driver has no
  prior stub/allowed-fail history; it did not exist before this ticket.

## What this lane proves

`internal/storage/storetest.RunBlobStoreTests` passes for real against a
live MinIO server: `Put`/`Get`/`Delete`/`Exists`, the idempotent-Put case
(same content twice yields the same Hash, no duplicate storage), the
key-not-found error path (`cascade.KindNotFound`), and delete-of-absent
being a no-op. It also proves the unreachable-endpoint and missing-bucket
error paths against the real server (never simulated).

## What this lane does NOT prove

Anything about the local `providers/fs` driver (unaffected by this
ticket), or the sync engine / `sync` CLI / cross-machine acceptance
(S-38.T1/T2/T3/T5 — out of this ticket's scope). See `docs/storage.md`
§Server profile for the full picture.
