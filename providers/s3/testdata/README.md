# providers/s3/testdata — s3-storetest-under-docker lane provenance

This directory has no fixtures. It exists to record the provenance and the
honest scope of the `redis-storetest-under-docker`-style S3 lane
(`.github/workflows/ci.yml`'s `s3-storetest-under-docker` job), per
12-QUALITY-CONSTITUTION.md Art.2, matching
`providers/pgvector/testdata/README.md`'s precedent.

## What the lane runs (P1-E17-W4-S38-T7)

- **Image:** `cgr.dev/chainguard/minio@sha256:71674988a1c7ddd5724928633199152b11e4ddefd6c6ce2d60772ff4a8f22ca9`,
  pinned by its multi-arch index digest only (never a tag, never
  `:latest`). The upstream registry no longer serves anonymous pulls;
  cgr.dev does. A separate step pulls it first, so a pull failure fails
  the lane on its own line. It starts with `server /tmp/data` (the image
  runs as a non-root user with no `/data`) and a fixed root
  user/password, and a final health check fails the lane with the server
  logs if it never reports ready.
- **Bucket setup:** in Go, not in a client container.
  `bucket_setup_test.go`'s `ensureTestBucket` builds a minio-go client
  with the same options as `s3.Open` and creates the bucket when absent.
  `realS3` calls it once per test binary, and any setup error fails every
  live test; it never skips them.
- **Command:** `go test -tags=integration -count=1 -race
  ./providers/s3/... -v`, teed to `s3-test.log`.
- **Counted gate:** a step after the tests counts the top-level
  `--- PASS:` lines of the five named live tests
  (`TestS3BlobStoretestUnderDocker`, `TestOpen_UnreachableServer_Real`,
  `TestOpen_MissingBucket_Real`, `TestEnsureTestBucket`,
  `TestEnsureTestBucket_FailurePath`) and requires exactly 5, plus 0
  top-level `--- SKIP` lines. A run where the env-refs went missing and
  the tests skipped therefore fails the lane.
- **Env:** `CASCADE_TEST_S3_ENDPOINT` (the server's own `http://host:port`
  root), `CASCADE_TEST_S3_BUCKET`, `CASCADE_TEST_S3_ACCESS_KEY`,
  `CASCADE_TEST_S3_SECRET_KEY` — never a literal committed anywhere; CI
  supplies them as step-level env pointing at the server container's own
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
