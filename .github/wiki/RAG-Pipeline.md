# RAG Pipeline

Cascade includes a local RAG (Retrieval-Augmented Generation) pipeline for semantic search over your instruction corpus. All processing runs on your machine. No data is sent to any external service.

---

## Overview

The pipeline has three tiers:

| Tier | Method | Description |
|---|---|---|
| T1 FTS5 | Keyword | SQLite FTS5 full-text search over tokenized text |
| T2 Dense | Embeddings | BGE-M3 ONNX model, 1024-dimensional float vectors |
| T3 RRF | Fusion | Reciprocal Rank Fusion merges T1 and T2 result lists |

Each tier runs independently. The RRF step weights and merges them into a single ranked list.

---

## How it works

1. **Ingest** - the daemon watches `.cascade/` directories for changes. When a file changes, the indexer reads it, splits it into chunks, and queues the chunks for indexing.

2. **Chunking** - text is split by heading boundaries (`##` sections), with a max chunk size of 512 tokens. Overlap between chunks is 64 tokens.

3. **Embedding** - each chunk is embedded using the BGE-M3 model loaded from an ONNX file bundled with the binary. On first run, the model is extracted to `~/.cascade/models/`. Embedding runs in a background thread pool.

4. **Storage** - embeddings are stored in SQLite with the `sqlite-vec` extension. FTS5 tokens are stored in a separate virtual table in the same database.

5. **Query** - a search query runs through both FTS5 and dense paths. RRF fuses the two ranked lists and returns the top-k results.

---

## Opt-in behavior

RAG indexing is on by default. To disable it:

```bash
cascade config set rag.enabled false
cascade daemon restart
```

With RAG disabled, `cascade search` still works but uses keyword matching only (no embeddings).

---

## Disk usage

| Component | Typical size |
|---|---|
| BGE-M3 ONNX model | ~560 MB (downloaded once, stored in `~/.cascade/models/`) |
| SQLite database | ~2–20 MB per 100 instruction files |
| FTS5 index | ~1–5 MB per 100 files |

The ONNX model is only loaded into memory when the daemon is running. On machines with less than 4 GB RAM, you can switch to a lighter model or disable dense embeddings entirely.

---

## Configuration

```toml
[rag]
enabled = true
embedding_model = "bge-m3"
fts_weight = 0.4
dense_weight = 0.6
top_k = 10
```

See the [Configuration](Configuration.md) page for all RAG keys.

**Adjusting fusion weights:**

If you find keyword results more relevant than embedding results (common for short, precise queries), increase `fts_weight`:

```bash
cascade config set rag.fts_weight 0.6
cascade config set rag.dense_weight 0.4
```

If you find embedding results better (common for conceptual queries), do the opposite.

---

## Running a search

```bash
cascade search "how do I handle authentication"
cascade search "Tailwind component conventions" --top 5
cascade search "database migration pattern" --format json
```

Results show the matched text, the source file and tier, and the relevance score.

---

## Benchmarks

The RAG pipeline is benchmarked with Criterion.rs. Target latencies on an M-series Mac:

| Operation | p50 |
|---|---|
| FTS5 query | < 5 ms |
| Dense embedding (single chunk) | ~50 ms (CPU) |
| RRF fusion over 100 results | < 1 ms |
| End-to-end search (top 10) | ~60 ms |

CI runs a regression gate: if a benchmark degrades more than 20% versus the baseline, the build fails. See [Performance](cascade-performance.md) for detailed benchmark data.

---

## Troubleshooting

**Search returns no results**

Check that indexing completed:

```bash
cascade status
# look for: index: N documents
```

If the count is 0, try forcing a reindex:

```bash
cascade daemon restart
```

**Model download is slow**

BGE-M3 (~560 MB) downloads on first run. The download uses the same network as any `cargo install`. Once cached in `~/.cascade/models/`, it is not re-downloaded.

**High memory usage**

The ONNX model keeps ~800 MB resident when loaded. If this is a problem, disable dense search:

```bash
cascade config set rag.dense_weight 0.0
cascade config set rag.fts_weight 1.0
```

This falls back to pure FTS5, which is fast and uses minimal memory.

See also: [Performance](cascade-performance.md) · [Configuration](Configuration.md) · [Troubleshooting](Troubleshooting.md)
