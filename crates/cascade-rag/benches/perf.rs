//! Criterion.rs benchmark suite for cascade-rag hot paths.
//!
//! # Purpose
//!
//! Measures three hot paths optimised in E-04:
//!
//! 1. **embed_batch** — [`WorkerPool::embed_batch`] at corpus sizes [100, 1000, 5000].
//!    Uses [`StubEmbedder`] (random f32 vecs, no ONNX) to isolate parallelism
//!    overhead versus a single-threaded baseline.
//!
//! 2. **sharded_search** — [`ShardedIndex::search`] at top_k [5, 20, 50] across
//!    shard counts [1, 4, 8] with 10k pre-indexed docs.
//!
//! 3. **query_cache_hit / query_cache_miss** — [`CachedIndex::search`] with a
//!    100% cache-hit rate versus a 0% miss-only path.
//!
//! 4. **rrf_merge** — pure RRF fusion at k=60, input_count=[100, 1000].
//!
//! # Constraints
//!
//! - All benchmarks run in `--test` (dry-run) mode to validate correctness
//!   without timed measurement.  Full timing: `cargo bench -p cascade-rag`.
//! - No ONNX model downloads; [`StubEmbedder`] returns seeded random vectors.
//! - Total runtime on a standard developer laptop: < 90 seconds.
//!
//! # Tickets
//!
//! T-P4-E04-17

use criterion::{criterion_group, criterion_main, BenchmarkId, Criterion, Throughput};
use std::path::Path;
use std::sync::Arc;
use std::time::Duration;

use cascade_rag::{
    chunk::{Chunk, ChunkerConfig, Chunker},
    chunk::semantic::SemanticChunker,
    chunk::markdown::MarkdownChunker,
    chunk::hierarchical::HierarchicalChunker,
    embed::{EmbedModel, EmbedError},
    index::{CachedIndex, sharding::{EmbedResult as ShardEmbedResult, ShardedIndex}},
    retrieve::rrf::{rrf_merge, FusedHit, RankedList},
    workers::{RawDoc, WorkerPool, WorkerPoolConfig},
};

// ── StubEmbedder ────────────────────────────────────────────────────────────────

/// Benchmark-only stub embedder.
///
/// Returns seeded random f32 vectors of `dim` dimensions.  The work per
/// document is non-trivial (fills `dim` floats via `sin` to prevent the
/// compiler from optimizing it away), so parallelism benchmarks measure actual
/// fan-out overhead rather than thread-spawn latency.
///
/// # Constraints
///
/// - NOT for use in non-test / non-bench code.
/// - Not exported from the crate — this module is `benches/` only.
struct StubEmbedder {
    dim: usize,
}

impl StubEmbedder {
    fn new(dim: usize) -> Self {
        Self { dim }
    }
}

impl EmbedModel for StubEmbedder {
    fn embed_dense(&self, texts: &[&str]) -> std::result::Result<Vec<Vec<f32>>, EmbedError> {
        let dim = self.dim;
        Ok(texts
            .iter()
            .map(|t| {
                let seed = t.bytes().fold(0u64, |acc, b| acc.wrapping_add(b as u64));
                let base = (seed % 1000) as f32 * 0.001;
                // Non-trivial work to prevent compiler elision.
                (0..dim)
                    .map(|i| (base + i as f32 * 0.0013).sin())
                    .collect()
            })
            .collect())
    }

    fn embed_sparse(
        &self,
        texts: &[&str],
    ) -> std::result::Result<Vec<Vec<(u32, f32)>>, EmbedError> {
        Ok(texts
            .iter()
            .map(|t| {
                t.split_whitespace()
                    .take(32)
                    .enumerate()
                    .map(|(i, word)| {
                        let h = word.bytes().fold(2166136261u32, |acc, b| {
                            acc.wrapping_mul(16777619).wrapping_add(b as u32)
                        });
                        (h, 1.0 / (i as f32 + 1.0))
                    })
                    .collect()
            })
            .collect())
    }

    fn dim(&self) -> usize {
        self.dim
    }

    fn model_id(&self) -> &str {
        "stub-embedder"
    }
}

// ── Fixture corpus ───────────────────────────────────────────────────────────

/// Generate `n` synthetic documents of approximately `target_chars` characters.
fn make_docs(n: usize, target_chars: usize) -> Vec<String> {
    let sentence = "The quick brown fox jumps over the lazy dog. ";
    let repeat = (target_chars / sentence.len()).max(1);
    (0..n)
        .map(|i| {
            let base: String = sentence.repeat(repeat);
            format!("doc_{i}: {base}")
        })
        .collect()
}

/// Build a [`Vec<RawDoc>`] from a string slice.
fn raw_docs(texts: &[String]) -> Vec<RawDoc> {
    texts
        .iter()
        .enumerate()
        .map(|(i, t)| RawDoc::new(i, t.clone()))
        .collect()
}

/// Build a [`WorkerPool`] with the stub embedder and a fixed thread count.
fn make_pool(workers: usize, dim: usize) -> WorkerPool {
    let model: Arc<dyn EmbedModel> = Arc::new(StubEmbedder::new(dim));
    let cfg = WorkerPoolConfig {
        max_embed_workers: workers,
        embed_batch_size: 64,
        max_queue_depth: 16,
    };
    WorkerPool::new(cfg, model)
}

/// Populate a [`ShardedIndex`] at `root` with `n_docs` random vectors.
fn populate_sharded(root: &Path, shard_count: usize, dim: usize, n_docs: usize) -> ShardedIndex {
    let idx = ShardedIndex::new(root, shard_count, dim)
        .expect("failed to create sharded index");
    let embedder = StubEmbedder::new(dim);
    // Produce n_docs docs in batches of 500 to avoid calling embed_dense with
    // an enormous slice.
    let batch_size = 500;
    let mut inserted = 0usize;
    while inserted < n_docs {
        let this_batch = batch_size.min(n_docs - inserted);
        let texts: Vec<String> = (inserted..inserted + this_batch)
            .map(|i| format!("bench doc {i}"))
            .collect();
        let text_refs: Vec<&str> = texts.iter().map(|s| s.as_str()).collect();
        let vecs = embedder.embed_dense(&text_refs).expect("stub embed failed");
        for (j, vec) in vecs.into_iter().enumerate() {
            let doc_id = format!("doc_{}", inserted + j);
            idx.upsert(&ShardEmbedResult { doc_id, embedding: vec })
                .expect("upsert failed");
        }
        inserted += this_batch;
    }
    idx
}

/// Build a query vector using the stub embedder.
fn query_vec(dim: usize) -> Vec<f32> {
    StubEmbedder::new(dim)
        .embed_dense(&["benchmark query"])
        .unwrap()
        .remove(0)
}

// ── Benchmark: embed_batch ───────────────────────────────────────────────────

fn bench_embed_batch(c: &mut Criterion) {
    const DIM: usize = 1024;
    const CORPUS_SIZES: &[usize] = &[100, 1000, 5000];

    let mut group = c.benchmark_group("embed_batch");
    group.measurement_time(Duration::from_secs(10));
    group.warm_up_time(Duration::from_secs(3));

    for &n in CORPUS_SIZES {
        let docs_text = make_docs(n, 200);

        // Parallel pool (all CPUs).
        group.throughput(Throughput::Elements(n as u64));
        group.bench_with_input(
            BenchmarkId::new("parallel", n),
            &n,
            |b, _| {
                let pool = make_pool(0, DIM); // 0 = num_cpus
                let docs = raw_docs(&docs_text);
                b.iter(|| {
                    let results = pool.embed_batch(docs.clone());
                    assert_eq!(results.len(), n);
                });
            },
        );

        // Single-threaded baseline.
        group.bench_with_input(
            BenchmarkId::new("single_thread", n),
            &n,
            |b, _| {
                let pool = make_pool(1, DIM);
                let docs = raw_docs(&docs_text);
                b.iter(|| {
                    let results = pool.embed_batch(docs.clone());
                    assert_eq!(results.len(), n);
                });
            },
        );
    }

    group.finish();
}

// ── Benchmark: sharded_search ────────────────────────────────────────────────

fn bench_sharded_search(c: &mut Criterion) {
    const DIM: usize = 1024;
    const N_DOCS: usize = 10_000;
    const SHARD_COUNTS: &[usize] = &[1, 4, 8];
    const TOP_KS: &[usize] = &[5, 20, 50];

    let tmp = tempfile::tempdir().expect("tempdir");
    let q = query_vec(DIM);

    let mut group = c.benchmark_group("sharded_search");
    group.measurement_time(Duration::from_secs(10));
    group.warm_up_time(Duration::from_secs(2));

    for &shards in SHARD_COUNTS {
        let root = tmp.path().join(format!("shards_{shards}"));
        let idx = populate_sharded(&root, shards, DIM, N_DOCS);

        for &top_k in TOP_KS {
            let id = format!("shards={shards}/top_k={top_k}");
            group.bench_function(&id, |b| {
                b.iter(|| {
                    let hits = idx.search(&q, top_k).expect("search failed");
                    assert!(hits.len() <= top_k);
                });
            });
        }
    }

    group.finish();
}

// ── Benchmark: query_cache_hit / query_cache_miss ─────────────────────────────

fn bench_query_cache(c: &mut Criterion) {
    const DIM: usize = 1024;
    const N_DOCS: usize = 1_000;
    const TOP_K: usize = 10;

    let tmp = tempfile::tempdir().expect("tempdir");

    // Populate the underlying ShardedIndex.
    let root_hit = tmp.path().join("cache_hit");
    let root_miss = tmp.path().join("cache_miss");
    let shard_idx_hit = populate_sharded(&root_hit, 2, DIM, N_DOCS);
    let shard_idx_miss = populate_sharded(&root_miss, 2, DIM, N_DOCS);

    let q = query_vec(DIM);

    // Cache-hit path: warm the cache with a single search, then measure.
    let cached_hit = CachedIndex::new(shard_idx_hit, 512, Duration::from_secs(60));
    // Prime the cache.
    cached_hit.search(&q, TOP_K).expect("prime search failed");

    // Cache-miss path: TTL=0 so every search is a miss.
    let cached_miss = CachedIndex::new(shard_idx_miss, 512, Duration::from_nanos(1));

    let mut group = c.benchmark_group("query_cache");
    group.measurement_time(Duration::from_secs(8));
    group.warm_up_time(Duration::from_secs(2));

    group.bench_function("hit", |b| {
        b.iter(|| {
            let hits = cached_hit.search(&q, TOP_K).expect("cache-hit search failed");
            assert!(hits.len() <= TOP_K);
        });
    });

    group.bench_function("miss", |b| {
        b.iter(|| {
            let hits = cached_miss.search(&q, TOP_K).expect("cache-miss search failed");
            assert!(hits.len() <= TOP_K);
        });
    });

    group.finish();
}

// ── Benchmark: rrf_merge ──────────────────────────────────────────────────────

fn bench_rrf_merge(c: &mut Criterion) {
    const K: f64 = 60.0;
    const INPUT_COUNTS: &[usize] = &[100, 1000];

    let mut group = c.benchmark_group("rrf_merge");
    group.measurement_time(Duration::from_secs(5));
    group.warm_up_time(Duration::from_secs(1));

    for &n in INPUT_COUNTS {
        // Build two ranked lists of n entries each with distinct chunk_ids.
        let fts_hits: Vec<(i64, f64)> = (0..n as i64)
            .map(|i| (i, 1.0 / (i + 1) as f64))
            .collect();
        let dense_hits: Vec<(i64, f64)> = (0..n as i64)
            .rev()
            .map(|i| (i, 1.0 / (n as i64 - i) as f64))
            .collect();

        group.throughput(Throughput::Elements(n as u64));
        group.bench_with_input(
            BenchmarkId::new("two_lists", n),
            &n,
            |b, _| {
                b.iter(|| {
                    let lists = vec![
                        RankedList { source: "fts5",  weight: 1.0, hits: &fts_hits },
                        RankedList { source: "dense", weight: 1.0, hits: &dense_hits },
                    ];
                    let fused = rrf_merge(&lists, K, 50);
                    assert!(!fused.is_empty());
                });
            },
        );
    }

    group.finish();
}

// ── Benchmark: chunking ───────────────────────────────────────────────────────

fn bench_chunking(c: &mut Criterion) {
    use std::path::PathBuf;

    const CORPUS_SIZES: &[usize] = &[50, 200, 1000];

    // Build fixture text of varying character counts.
    fn make_text(n_chars: usize) -> String {
        let sentence = "The quick brown fox jumps over the lazy dog. ";
        let repeat = (n_chars / sentence.len()).max(1);
        sentence.repeat(repeat)
    }

    let mut group = c.benchmark_group("chunking");
    group.measurement_time(Duration::from_secs(8));
    group.warm_up_time(Duration::from_secs(2));

    let path = PathBuf::from("bench_fixture.txt");
    let cfg = ChunkerConfig::default();

    for &n_chars in CORPUS_SIZES {
        let text = make_text(n_chars * 10); // scale by 10 to get non-trivial input

        // SemanticChunker
        group.bench_with_input(
            BenchmarkId::new("semantic", n_chars),
            &n_chars,
            |b, _| {
                let chunker = SemanticChunker::with_config(cfg.clone());
                b.iter(|| {
                    let chunks = chunker.chunk(&path, &text).expect("chunk failed");
                    assert!(!chunks.is_empty() || text.is_empty());
                });
            },
        );

        // MarkdownChunker
        group.bench_with_input(
            BenchmarkId::new("markdown", n_chars),
            &n_chars,
            |b, _| {
                let chunker = MarkdownChunker;
                b.iter(|| {
                    let _ = chunker.chunk(&path, &text).expect("chunk failed");
                });
            },
        );

        // HierarchicalChunker
        group.bench_with_input(
            BenchmarkId::new("hierarchical", n_chars),
            &n_chars,
            |b, _| {
                let chunker = HierarchicalChunker::new(cfg.clone());
                b.iter(|| {
                    let _ = chunker.chunk(&path, &text).expect("chunk failed");
                });
            },
        );
    }

    group.finish();
}

// ── criterion harness ────────────────────────────────────────────────────────

criterion_group!(
    benches,
    bench_embed_batch,
    bench_sharded_search,
    bench_query_cache,
    bench_rrf_merge,
    bench_chunking,
);
criterion_main!(benches);
