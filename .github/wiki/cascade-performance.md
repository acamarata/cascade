# Cascade Performance

This page covers benchmark results for the cascade-rag hot paths, a tuning guide for the main configuration knobs, and an explanation of the CI regression gate that guards against performance regressions on pull requests.

---

## Benchmark Results

Benchmarks are run with [Criterion.rs](https://github.com/bheisler/criterion.rs) against a `StubEmbedder` (seeded random vectors, no ONNX model) so the numbers reflect parallelism overhead and data-structure costs, not model inference latency. All timing is wall-clock p50 (median) on an Apple M-series developer laptop (`aarch64-apple-darwin`). CI runners (`ubuntu-24.04 x86_64`) will produce different absolute numbers; the 20% regression threshold in the bench-check job accounts for this.

To reproduce locally:

```bash
cargo bench -p cascade-rag
```

### Embedding throughput (embed_batch)

Corpus sizes of 100, 1000, and 5000 documents. "Parallel" uses the `WorkerPool` thread fan-out; "single thread" is a sequential baseline on the same `StubEmbedder`.

| Benchmark | p50 latency | Throughput |
|---|---|---|
| embed_batch / parallel / 100 docs | 173 µs | ~579 K docs/s |
| embed_batch / single_thread / 100 docs | 191 µs | ~523 K docs/s |
| embed_batch / parallel / 1000 docs | 650 µs | ~1.54 M docs/s |
| embed_batch / single_thread / 1000 docs | 2.02 ms | ~494 K docs/s |
| embed_batch / parallel / 5000 docs | 3.14 ms | ~1.59 M docs/s |
| embed_batch / single_thread / 5000 docs | 10.5 ms | ~475 K docs/s |

At 1000+ documents the worker pool is 3x faster than the sequential path. Below ~200 documents the thread-spawn overhead narrows the gap.

### Sharded index search latency (sharded_search)

10,000 pre-indexed documents. Latency is p50 search time across shard counts and top-k values.

| Shards | top_k=5 | top_k=20 | top_k=50 |
|---|---|---|---|
| 1 | 16.5 ms | 16.3 ms | 16.2 ms |
| 4 | 6.54 ms | 6.52 ms | 6.53 ms |
| 8 | 14.4 ms | 14.5 ms | 14.4 ms |

Four shards cut latency to roughly 40% of single-shard time on this 8-core machine. Eight shards underperform four shards at 10k docs because the per-shard work (2500 docs each) is too small to saturate the extra threads; the cross-shard merge overhead dominates. See the tuning guide below for shard-count recommendations at different corpus sizes.

### Query cache hit vs miss (query_cache)

| Path | p50 latency |
|---|---|
| Cache hit | 4.2 µs |
| Cache miss (index lookup) | 951 µs |

A cache hit is ~225x faster than a miss. For interactive use cases where users run similar queries repeatedly (common in coding assistants), a well-sized cache makes a substantial difference.

### RRF fusion (rrf_merge)

Fusing two ranked lists with k=60.

| Input size (items per list) | p50 latency | Throughput |
|---|---|---|
| 100 | 9.5 µs | ~10.5 M items/s |
| 1000 | 102 µs | ~9.8 M items/s |

RRF is fast enough that it is never the bottleneck in a typical search pipeline.

### Chunking latency (chunking)

Time to chunk a document of N characters. Three chunker types.

| Chunker | 50 chars | 200 chars | 1000 chars |
|---|---|---|---|
| Hierarchical | 348 ns | 816 ns | 21.6 µs |
| Semantic | 823 ns | 2.3 µs | 24.4 µs |
| Markdown | 2.9 µs | 9.6 µs | 25.6 µs |

Hierarchical is the fastest for short inputs. All three converge at larger document sizes where the linear scan over text dominates.

---

## Architecture Overview

The cascade-rag pipeline has three layers designed to handle large-corpus indexing without blocking the UI daemon.

```
Documents
    |
    v
WorkerPool (embed_batch)
    |
    |-- N worker threads (configurable)
    |   each calls EmbeddingProvider::embed_dense + embed_sparse
    |
    v
ShardedIndex (sharded_search)
    |
    |-- shard_count sub-indexes, each holding a fraction of the corpus
    |   search fans out in parallel, results merged
    |
    v
CachedIndex (query_cache)
    |
    |-- LRU cache keyed on (query_text, top_k)
    |   hit -> return cached result
    |   miss -> ShardedIndex::search -> insert into cache -> return
    |
    v
rrf_merge
    |
    |-- fuses dense-vector results + FTS5 keyword results
    |   using Reciprocal Rank Fusion (k=60)
    |
    v
Ranked results
```

Each layer is independently configurable. You can run with shards=1 and cache disabled for small projects, or tune all three for a large shared corpus.

---

## Tuning Guide

### max_embed_workers

| Setting | Type | Default |
|---|---|---|
| `max_embed_workers` | `usize` | `num_cpus` |

Controls how many threads the `WorkerPool` uses for parallel embedding.

- Set to `num_cpus - 2` on shared or battery-constrained machines to leave headroom for other work.
- On a dedicated CI machine or desktop, leaving it at `num_cpus` is fine.
- Values above `num_cpus` do not help because the bottleneck shifts to thread contention.

### embed_batch_size

| Setting | Type | Default |
|---|---|---|
| `embed_batch_size` | `usize` | `64` |

Number of documents sent to the embedding model in a single call.

- For ONNX-backed models (BGE-M3), use 32-128. Values above 128 rarely improve throughput and can increase peak memory.
- For the `StubEmbedder` (tests/benchmarks), any value is fine.
- Reducing this value lowers peak memory at the cost of more model calls.

### shard_count

| Setting | Type | Default |
|---|---|---|
| `shard_count` | `usize` | `4` |

Number of sub-indexes the corpus is split across. Higher shard counts speed up search by parallelizing across shards but add per-shard overhead on queries.

- **< 50,000 docs:** 4 shards. More shards hurt because per-shard work is too small.
- **50,000 to 500,000 docs:** 8 shards. The parallel speedup pays off.
- **> 500,000 docs:** 16 shards, but measure before committing. At this scale, index sharding may matter less than hardware (NVMe vs HDD).

### query_cache_capacity

| Setting | Type | Default |
|---|---|---|
| `query_cache_capacity` | `usize` | `512` |

Maximum number of (query, top_k) pairs held in the LRU query cache.

- 512 entries is appropriate for most single-user setups. Each entry stores a `Vec<FusedHit>` (roughly 2-8 KB depending on top_k and result metadata), so the full cache is under 4 MB at the default.
- Reduce to 128 or 256 on memory-constrained machines (e.g., 8 GB total RAM).
- Increase above 512 if you have many distinct queries and enough RAM; the cache hit rate in a typical coding-assistant workload is already high at 512.

### query_cache_ttl_secs

| Setting | Type | Default |
|---|---|---|
| `query_cache_ttl_secs` | `u64` | `60` |

Time-to-live for each cache entry, in seconds. Entries older than this are evicted on the next cache access.

- **Interactive use:** 30-120 seconds. Cached results stay fresh across a short session.
- **CI / batch use:** 0 to disable TTL eviction (only capacity eviction applies). Disabling TTL avoids stale-hit confusion when the index does not change during a run.
- **After a full reindex:** the daemon invalidates the cache automatically. TTL is a fallback for partial updates.

---

## Incremental Indexing

Cascade uses [Blake3](https://github.com/BLAKE3-team/BLAKE3) content hashes to detect which files have changed since the last index run. On each `cascade index` invocation:

1. Each file's content is hashed.
2. The hash is compared to the stored hash in the index metadata.
3. Only files with a changed hash (new, modified, or renamed) are re-chunked and re-embedded.
4. Deleted files are removed from the index.

This makes incremental runs fast: indexing a 500-file repository after a two-file edit typically finishes in under a second.

**When to run `cascade index --full`:**

- After major content reorganization where many files move or are renamed. The incremental path detects changes by hash, not path, so moves are handled correctly, but a full run produces a cleaner shard distribution.
- After changing `shard_count` in your configuration. A changed shard count invalidates the existing shard layout; the daemon will warn you and request a full run.
- If you suspect index corruption. A full run rebuilds all shard files from scratch.

---

## Regression Gate

The `bench-check` CI job runs on every pull request and compares benchmark p50 latency against `bench/baselines.json`.

Any benchmark that regresses by more than 20% causes the job to fail. The 20% threshold is chosen to absorb hardware variance between developer machines (Apple Silicon) and CI runners (Linux x86_64) while still catching meaningful regressions introduced by a PR.

To update baselines after an intentional performance change:

```bash
# From the repo root
./scripts/record_baselines.sh

# Review the updated file
cat bench/baselines.json

# Commit alongside the code change
git add bench/baselines.json
git commit -m "bench: update baselines for <description of change>"
```

Include the updated `bench/baselines.json` in the same PR as the code change. The CI job will compare against the new baseline after the PR merges.

The regression gate only runs on `pull_request` events, not on direct pushes to `main`, to avoid noise from commit-to-commit measurement variance on the main branch.

---

## Snapshot Safety and Rollback

The index is written in a copy-on-write manner. When an incremental or full reindex completes, the new shard files are written to a staging area and then atomically swapped into place. A partial or failed reindex does not corrupt the live index.

To roll back to the previous index state:

```bash
cascade index --rollback
```

This swaps the staging and live directories back. The rollback is only available until the next successful reindex (which cleans up the old staging area).

---

*See also: [Architecture](Architecture.md) | [RAG Setup](RAG-Setup.md) | [CLI Reference](CLI-Reference.md)*
