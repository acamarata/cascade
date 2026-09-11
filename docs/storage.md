# Storage

Cascade's storage layer is five family interfaces (`pkg/provider`: Store,
VectorStore, BlobStore, Cache, Queue), each implemented once per profile.
02-TARGET-STRUCTURE's Profiles contract splits drivers along that line:

| Family     | local profile         | server profile              |
|------------|------------------------|------------------------------|
| Store      | `providers/sqlite`     | `providers/postgres`        |
| VectorStore| `providers/localvector`| `providers/pgvector`        |
| Cache      | `internal/storage/cache` | `providers/redis`         |
| Queue      | `internal/storage/queue` | `providers/redis`         |
| BlobStore  | `providers/fs`         | `providers/s3`               |

A driver that satisfies its family's `internal/storage/storetest` suite is
correct by construction against the interface contract — the suite is
shared across every driver for that family, local or server.

## Server profile

The server profile (`--profile server` / `CASCADE_PROFILE=server`) composes
the Postgres `Store` driver (`providers/postgres`) and the pgvector
`VectorStore` driver (`providers/pgvector`). Both are pure Go (no CGO),
built on [jackc/pgx/v5](https://github.com/jackc/pgx) via its
`database/sql`-compatible `stdlib` adapter — the same `database/sql`
pattern `providers/sqlite` uses, just against a different dialect.

It also composes the Redis `Cache` and `Queue` drivers (`providers/redis`,
S-38.T6) and the S3 `BlobStore` driver (`providers/s3`, S-38.T7) — every
family 02-TARGET-STRUCTURE's Profiles contract names for the server
profile is now a real driver (Art.1.4), never a stub.

### Postgres Store driver (`providers/postgres`)

- Schema: a single `kv(namespace, key, value)` table, `PRIMARY KEY
  (namespace, key)`, mirroring `providers/sqlite`'s shape. Both `namespace`
  and `key` are declared `TEXT COLLATE "C"` — **load-bearing**, not
  decorative: a fresh Postgres database's default collation is the
  server's OS locale, which sorts punctuation with far lower weight than a
  byte-wise comparison, silently breaking `Scan`'s prefix-range query for
  any prefix containing punctuation. `"C"` is Postgres's built-in
  byte-order collation and matches `providers/sqlite`'s (and Go's own)
  string ordering exactly.
- Transactions: real Postgres transactions (`sql.Tx`, default isolation).
  `CompareAndSwap` does an explicit read-compare-write inside the
  transaction, so a conflicting concurrent write is caught by the
  application logic, not relied on isolation level alone.
- Error mapping: every native Postgres SQLSTATE code is classified into
  the `pkg/cascade` taxonomy (`postgres_errors.go`) — never a generic
  "unavailable" for a conflict, permission, or quota failure.
- Credential custody: a DSN's password is never echoed into a log, metric
  label, or error message. `redactDSN` uses `net/url.URL.Redacted()` (the
  standard-library redaction path) and falls back to a fixed placeholder
  for a DSN that fails to parse at all.

### pgvector VectorStore driver (`providers/pgvector`)

- Schema: a single `vectors(namespace, id, embedding, metadata)` table.
  `embedding` is an **unconstrained** `vector` column (no fixed
  dimension), so different namespaces may carry different embedding
  dimensionalities in the same physical table.
- pgvector is an **extension**, not a guaranteed capability: `Open` runs
  `CREATE EXTENSION IF NOT EXISTS vector` and refuses with a typed
  `cascade.KindUnsupported` error (`pgvector.ErrExtensionMissing`) if the
  server cannot satisfy it — never a silent fallback to a non-vector code
  path, which would make similarity search quietly wrong instead of
  loudly absent.
- Similarity: cosine distance (`<=>`), converted to a `1 - distance`
  score so higher means more similar, matching `provider.VectorStore`'s
  documented contract. Mixing dimensionalities within one namespace is
  refused by Postgres itself (`SQLSTATE 22000`), which this driver maps to
  `cascade.KindInvalidInput` — a caller-side mistake, not a backend
  outage.
- Metadata filtering: `provider.VectorQuery.Filter` is applied server-side
  via JSONB containment (`metadata @> filter::jsonb`); an empty filter
  matches every row.

### Redis Cache + Queue drivers (`providers/redis`)

- Wire library: [redis/go-redis/v9](https://github.com/redis/go-redis)
  (BSD-2-Clause), a pure-Go RESP client — no CGO, matching every other
  driver in this module.
- Cache: Redis's own native per-key `SET ... EX` expiry implements the
  family's TTL contract directly (`ttl=0` maps to no expiration); `Flush`
  enumerates a namespace's keys via cursor-based `SCAN` (never the
  server-blocking `KEYS`) before deleting them.
- Queue: the same at-least-once, visibility-timeout contract as the local
  `internal/storage/queue` driver, proven by the same
  `storetest.RunQueueTests` suite. "Ready" ordering and inflight tracking
  live in two Redis sorted sets (score = a monotonic sequence for ready
  order, or a visibility deadline for inflight) rather than an in-process
  index — the server itself is the shared state. A receipt resolves to its
  message in both directions (receipt→id and id→receipt) so a stale
  receipt from before redelivery is explicitly invalidated at redelivery
  time, rather than relying on Redis's own per-key TTL as the source of
  staleness truth.
- Error paths: unreachable server, missing/unset env-ref, auth failure,
  and command timeouts/cancellation all map to the `pkg/cascade` taxonomy
  by the error's structured shape (never its message text), mirroring
  `providers/postgres/postgres_errors.go`'s classify-by-structure
  precedent; a Redis URL's credentials are redacted before they can reach
  any error message, the same as a Postgres DSN's.
- Untagged unit-test lane: `_test.go` files (all but `integration_test.go`)
  run against [alicebob/miniredis/v2](https://github.com/alicebob/miniredis)
  (MIT), a real-RESP-protocol, pure-Go in-memory server — giving this
  package the same no-docker coverage lane `providers/postgres` reserves
  for pure-function tests, without a hand-rolled fake dialect. The REAL,
  docker-provisioned server run is `integration_test.go`'s job (Art.2).

### S3 BlobStore driver (`providers/s3`)

- Wire library: [minio/minio-go/v7](https://github.com/minio/minio-go)
  (Apache-2.0), a pure-Go S3 REST client speaking any S3-compatible
  endpoint — no CGO, matching every other driver in this module.
- Content addressing: the SAME BLAKE3-256 contract as the local
  `providers/fs` fs BlobStore (`github.com/zeebo/blake3`), proven
  equivalent by the same `storetest.RunBlobStoreTests` suite. `Put`
  buffers the content in memory while hashing (so the final,
  content-addressed object key is known before the single S3 PUT — no
  temp-then-rename dance is needed the way `providers/fs` needs one,
  since a single S3 PUT to a given key is already atomic). Object keys
  mirror `providers/fs`'s two-character sharding-prefix directory layout,
  just as S3 key segments instead of filesystem path segments.
- Error paths: unreachable endpoint, missing/unset env-ref, auth failure,
  missing/inaccessible bucket, and request timeouts/cancellation all map
  to the `pkg/cascade` taxonomy by the error's structured S3 `Code` field
  (never its message text), mirroring `providers/redis`'s classify-by-
  structure precedent; the endpoint is redacted before it can reach any
  error message.
- Untagged unit-test lane: `_test.go` files (all but `integration_test.go`)
  run against [johannesboyne/gofakes3](https://github.com/johannesboyne/gofakes3)
  (MIT), a real S3 REST API implementation over an in-memory backend —
  giving this package the same no-docker coverage lane
  `providers/redis` reserves for miniredis, without a hand-rolled S3
  dialect. The REAL, docker-provisioned MinIO server run is
  `integration_test.go`'s job (Art.2).

### [storage] DSN configuration (env-refs only)

Per 08-INIT-CONFIG-SPEC §3, the `[storage]` cold section stores a DSN as
an **env-ref name**, never a literal connection string — a secret-shaped
literal in that position is refused (`internal/runtime.RefuseSecretLiteral`)
rather than silently accepted. `internal/runtime.ResolveDSNEnvRef` resolves
the named environment variable and fails closed (typed
`cascade.KindInvalidInput`) when it is unset.

### Live migration proof

`internal/storage/migrate`'s DSL applies against Postgres through
`migrate.PostgresEmitter{}`, proven live (not only as generated text) by
`providers/postgres/integration_test.go`'s `TestPostgresLiveMigration` and
`internal/runtime`'s `TestServerProfileAssembly`: ordered apply,
`schema_version` bookkeeping, `MinimumReaderVersion` downgrade refusal, and
idempotent re-apply all run against a real server. `internal/runtime.
PostgresMigrator` is the composition-root closure that wires this into a
`providers/postgres.Driver`'s `WithMigrator` injection seam.

### Composition root

`providers/**` may import `pkg/**` only, never `internal/**` (Art.10.2), so
the concrete driver construction — the only place `providers/postgres`,
`providers/pgvector`, `providers/redis` and `providers/s3` are imported
together — lives in `cmd/cascade` (`profile_server.go`, gated `//go:build
postgres`; `profile_server_stub.go` is its `!postgres` twin, which
refuses `--profile server` by name when the binary was not built with
`-tags=postgres`). The tag's name predates the Redis/S3 legs (it was
introduced for Postgres/pgvector alone) and now also gates both, since
this file is the server profile's one composition site.
`internal/runtime/profile_server.go` holds the profile-agnostic pieces
(env-ref/env-ref-family resolution, the migration closure, and the
`ServerProfile` type + context accessors, now carrying `Cache`, `Queue`
and `Blob` fields alongside `Store`/`Vector`) that `cmd/cascade` assembles
into the real drivers. `internal/runtime` never imports `providers/**`
(Art.10.2), so the Redis/S3-specific proof that `assembleServerProfile`
actually wires a live server lives at `cmd/cascade`, not in
`internal/runtime`'s own tests.

### Docker conformance lane

`.github/workflows/ci.yml` runs five required jobs against real servers:
`storetest-under-docker` and `pgvector-storetest-under-docker` (the Store
and VectorStore families, against a real `pgvector/pgvector:pg16`
server), `server-profile-assembly` (the live migration proof),
`redis-storetest-under-docker` (the Cache and Queue families, against a
real `redis:7-alpine` service container), and `s3-storetest-under-docker`
(the BlobStore family, against a real `minio/minio` server — run as a
plain job step rather than a `services:` container, since MinIO needs a
`server /data` command argument GitHub Actions services cannot supply).
None of these run against a hand-rolled fake — Art.2's real-counterpart
rule for an external wire contract.
