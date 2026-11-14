# Retrieval RPC methods

Methods over recall and the retrieval index. Access classes are listed in the
generated RPC method reference. For the retrieval pipeline itself
see [retrieval](../retrieval.md). Params of the query methods are decoded
leniently: an empty or `null` body means all defaults, a malformed body is
`invalid-input`.

### `recall.query`

Searches indexed corpora and returns fused, ranked results. The daemon also
answers the same handler under the name `cascade_search`, described
below.

Params: `query`, `scope` (the asking session's scope reference; required),
`corpus` (optional list of corpus ids), `entitlement`, `k` and `cite`. `k` is
10 when omitted and at most 500. A daemon with no recall service answers
`unavailable`.

Result: `query`, `results`, `citations`, `withheld`, `legs` and, only when
`cite` is true, `rendered`. `results` and `citations` are empty lists, never
`null`. Each result carries `rank`, `chunk_id`, `path`, `corpus_id`, `trust`,
`score`, `raw_score` and the `strategies` that found it. `withheld` counts
results held back from this caller.

### `cascade_search`

An alias of `recall.query`. It takes the same params, returns the same result
and fails the same way.

### `recall.what`

Searches across the file, conversation and memory domains at once and fuses
them by rank.

Params: `query` (required; blank is `invalid-input`), `k` (10 when omitted;
negative or over 500 is `invalid-input`), `scope`, `entitlement`, and the
scope-resolution context `cwd`, `user`, `machine`, `branch`, `task`, `session`
and `explicit_overrides`. A daemon with no service answers `unavailable`.

Result: `query`, `results`, `citations`, `withheld`, `legs` and, when set,
`truncated`, `rendered` and `errors`. Each result carries `rank`, `domain`
(`file`, `conversation` or `memory`), `id`, `path`, `corpus_id`, `trust`,
`score`, `raw_score`, `strategies` and a `snippet` of at most 240 characters.
`errors` maps a domain to why it did not answer. The call fails with
`unavailable` only when no configured domain answered at all.

### `recall.index.rebuild`

No params. Re-indexes every registered source from a clean slate, rewriting
the text index, the vector index when an embedder is configured, and the
catalog. Returns `CorporaIndexed`, `ChunksWritten`, `ChunksDeleted`,
`Converged` (true when the run changed nothing) and `Marker`, the tree marker
it recorded. The result fields carry no JSON tags, so they appear with these
capitalised names. This is the explicit repair path; the daemon never runs it
on its own.

### `recall.index.verify`

No params. Compares the catalog with both index legs without changing
anything. Returns `Missing` (catalog chunks with no text document),
`Orphaned` (text documents with no catalog record), `VectorIncomplete`
(corpora whose vector count differs from the catalog; empty when no vector
store is configured), `MarkerStatus` (current or drifted), `StoredMarker` and
`CurrentMarker`. Capitalised names, as for rebuild.

### `recall.index.update`

No params. Re-ingests only what changed since the stored marker, using
`git diff --name-status` against the commit in that marker. When the diff
cannot be taken, for example against an unknown commit, the update reports no
changes. Returns `FilesChanged`, `ChunksWritten`, `ChunksDeleted`,
`Converged` and `Marker`.

### `recall.index.migrate`

No params. Applies the retrieval schema migrations to the daemon database, with a `backups`
directory beside it set as the backup location. Returns `Applied`
(false when nothing was pending), `FromVersion` and `ToVersion`. A database
that cannot be opened is `unavailable`.

The four `recall.index.*` methods are registered only when the daemon has a
store.
