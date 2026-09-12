# Sync engine

The sync engine (`internal/sync`) is the server-local-node synchronization
core: domain classes, per-domain cursors, a chunked ssh transfer, and the
sync egress class. Merge strategies and the CLI/RPC surface land in later
tickets; this document covers the engine core only.

## Domain classes

Every syncable record family maps onto one of the closed twelve cascade.db
storage domains (`internal/storage`). The registry (`internal/sync/domains.go`)
never creates a new domain — it is a mapping over the existing set:

| Family | Storage domain | Sync class |
|---|---|---|
| config | `config` | synced |
| registry | `config` | synced |
| accounts | `config` | server-primary (metadata only) |
| phase-state | `config` | synced |
| memory | `memory` | synced |
| conversation | `context` | synced |
| blobs | `blobs` | synced |

The sync-class enum is `local-only | synced | server-primary`, plus
`synced-append` for plugin-scoped domains only. A domain or plugin storage
key that is never explicitly registered is **local-only and never syncs** —
there is no default merge. The vault (`secrets` domain) is not, and cannot
be, a registered domain: the exclusion is structural, not a runtime check.

Plugin-scoped domains declare their class through the plugin manifest v2
`storage.sync` key (default `local-only`).

## Sensitivity filter (pre-serialization)

Every record also carries its own immutable sensitivity tier. Before a
record is ever serialized for transfer, `internal/sync/filter.go` checks:

1. the record's domain+subkind is registered and not local-only;
2. the record's own tier is not `local-only` or `restricted`.

A record failing either check is never serialized. It is not silently
dropped: its exclusion is journaled (record id, reason, policy version)
and the domain's cursor advances past it — the cursor only ever advances
over records that were durably applied or explicitly excluded. An
excluded record is re-scanned whenever the policy or classification
version advances past the one it was excluded under.

## Cursors

Per-domain-subkind cursors persist through the storage `Store`
abstraction, monotonic and resumable across process restarts. A cursor
regress (a new position at or below the current one) is refused as a
typed conflict, never silently accepted as an implicit resync.

## Chunked transfer

The wire format (`internal/sync/chunk.go`) frames a stream into fixed
chunks: magic, version, stream id, sequence, total, payload length, and a
transport-layer BLAKE3 hash of the payload. `FuzzSyncChunkDecode` proves
the decoder never panics on malformed input and fails closed on every
truncated, reordered, or corrupted shape.

The transfer loop (`internal/sync/transfer.go`) rides whatever
`io.ReadWriteCloser` the caller supplies — production callers hand it the
S-36.T3 ssh tunnel's forwarded channel; no new dialer or ssh stack is
introduced here. Receipt is strictly sequential: a chunk out of order,
from the wrong stream, or truncated is refused immediately, and the
receiver reports exactly how many chunks it durably processed so a
dropped connection resumes from that point rather than restarting or
silently completing with missing data.

Blob content (`internal/sync/staging.go`) lands in a staging file first.
A blob is admitted to its final content-addressed path only when the
recomputed BLAKE3 digest of the staged bytes equals the *declared*
content address — a value established independently of the transfer, not
one the transfer itself supplied. A mismatch discards the staged bytes;
the final path is created only by one atomic rename, so an interrupted or
corrupt transfer never becomes visible as a complete file. Resuming an
interrupted staged transfer requires a durable, consistent sidecar
cursor; its absence or inconsistency is an explicit refusal to resume,
never a silent restart.

## Sync egress class

The sync engine registers `EgressClassSync` (`internal/hooks/egress`,
§D-30) at its own package init. Every outbound sync payload transits the
substitution and sensitivity pass on this class before it reaches the
wire, in addition to (never instead of) the pre-serialization filter
above.

## Fail-closed summary

| Condition | Behavior |
|---|---|
| Unregistered domain/subkind | Refused, treated as local-only |
| Record tier local-only or restricted | Refused, journaled, cursor still advances |
| Cursor regress | Refused as a typed conflict |
| Truncated/reordered/corrupt chunk | Refused, transfer stops at the last verified chunk |
| Blob digest mismatch | Staged bytes discarded, final path never created |
| Missing/inconsistent resume state | Refused to resume, never guessed |
| Unresolvable sensitivity tier | Resolves to `restricted` (fail closed) |
