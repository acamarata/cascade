# Storage

Cascade's storage layer is five family interfaces (`pkg/provider`: Store,
VectorStore, BlobStore, Cache, Queue), each implemented once per profile.
02-TARGET-STRUCTURE's Profiles contract splits drivers along that line:

| Family     | local profile        | server profile               |
|------------|-----------------------|-------------------------------|
| Store      | `providers/sqlite`    | `providers/postgres`         |
| VectorStore| `providers/localvector` | `providers/pgvector`       |
| Cache      | (in-process)          | Redis — S-38.T6 (not yet)    |
| BlobStore  | `providers/fs`        | S3 — S-38.T7 (not yet)       |

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

Redis (Cache) and S3 (BlobStore) are genuinely absent from the server
profile until S-38.T6/T7 land — there is no stub standing in for them.

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
the concrete driver construction — the only place both `providers/postgres`
and `providers/pgvector` are imported together — lives in `cmd/cascade`
(`profile_server.go`, gated `//go:build postgres`; `profile_server_stub.go`
is its `!postgres` twin, which refuses `--profile server` by name when the
binary was not built with `-tags=postgres`). `internal/runtime/
profile_server.go` holds the profile-agnostic pieces (DSN resolution, the
migration closure, and the `ServerProfile` type + context accessors) that
`cmd/cascade` assembles into the real drivers.

### Docker conformance lane

`.github/workflows/ci.yml` runs three required jobs against a real
`pgvector/pgvector:pg16` service container (a Postgres 16 image with the
pgvector extension available): `storetest-under-docker` (the Store family
conformance suite, closing the P1-E02-W1-S03-T5 allowed-fail leg),
`pgvector-storetest-under-docker` (the VectorStore family conformance
suite), and `server-profile-assembly` (the live migration proof). None of
these run against a hand-rolled fake Postgres — Art.2's real-counterpart
rule for an external wire contract.
