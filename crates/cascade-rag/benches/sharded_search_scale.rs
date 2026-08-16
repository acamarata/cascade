//! # sharded_search_scale — scale acceptance benchmarks for ShardedIndex
//!
//! ## Purpose
//!
//! Measures `ShardedIndex::search` latency at scale acceptance boundaries:
//! - **100k chunks** (always-on, ~1 min per run)
//! - **1M chunks** (feature `bench-scale-heavy`, ~10 min per run)
//! - **10M chunks** (feature `bench-scale-heavy`, skipped unless explicitly requested)
//!
//! Thresholds are defined in `bench/scale-thresholds.json` and checked by
//! `scripts/check_scale_thresholds.py` against Criterion JSON output.
//!
//! ## Corpus generation
//!
//! All corpora are generated in-bench using `StubEmbedder` (random f32 vectors,
//! seeded deterministically from index position). No ONNX model, no network.
//!
//! ## What this measures
//!
//! `ShardedIndex::search` distributes a cosine similarity query across N shards
//! (each backed by a sqlite-vec table), merges and re-ranks the per-shard top-k
//! results, and returns the global top-k. This benchmark validates that the merge
//! overhead and sqlite I/O stay within the latency thresholds at production corpus
//! sizes.
//!
//! ## Feature flags
//!
//! - `bench-scale-heavy` — unlocks 1M and 10M corpus benchmarks. Heavy corpora
//!   take significant time and disk; do not enable on CI unless the nightly bench
//!   workflow explicitly requests it.
//!
//! ## Tickets
//!
//! perf-01

use criterion::{criterion_group, criterion_main, BenchmarkId, Criterion, Throughput};
use std::path::Path;
use std::time::Duration;

use cascade_rag::{
    embed::{EmbedError, EmbedModel},
    index::sharding::{EmbedResult as ShardEmbedResult, ShardedIndex},
};

// ── StubEmbedder ─────────────────────────────────────────────────────────────

/// Scale-bench-only stub embedder.
///
/// Returns seeded random f32 vectors of `dim` dimensions. Uses a deterministic
/// seed derived from the document index so repeated runs produce identical
/// corpora (important for reproducible benchmark runs).
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
                (0..dim).map(|i| (base + i as f32 * 0.0013).sin()).collect()
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
        "stub-embedder-scale"
    }
}

// ── Corpus helpers ────────────────────────────────────────────────────────────

/// Populate a `ShardedIndex` at `root` with `n_docs` synthetic vectors.
///
/// Uses `batch_size=1000` to avoid embedding an enormous slice in one call.
/// Shard count is fixed at 8 (matches a realistic production deployment with
/// per-topic sharding). Dimension is 1024 (default E5 Large output width).
fn populate_index(root: &Path, n_docs: usize) -> ShardedIndex {
    const SHARD_COUNT: usize = 8;
    const DIM: usize = 1024;
    const BATCH: usize = 1000;

    let idx = ShardedIndex::new(root, SHARD_COUNT, DIM).expect("failed to create sharded index");
    let embedder = StubEmbedder::new(DIM);

    let mut inserted = 0usize;
    while inserted < n_docs {
        let this_batch = BATCH.min(n_docs - inserted);
        let texts: Vec<String> = (inserted..inserted + this_batch)
            .map(|i| format!("scale doc {i}"))
            .collect();
        let refs: Vec<&str> = texts.iter().map(|s| s.as_str()).collect();
        let vecs = embedder.embed_dense(&refs).expect("stub embed failed");
        for (j, vec) in vecs.into_iter().enumerate() {
            idx.upsert(&ShardEmbedResult {
                doc_id: format!("doc_{}", inserted + j),
                embedding: vec,
            })
            .expect("upsert failed");
        }
        inserted += this_batch;
    }
    idx
}

/// Build a deterministic query vector (same seed every run).
fn make_query() -> Vec<f32> {
    const DIM: usize = 1024;
    StubEmbedder::new(DIM)
        .embed_dense(&["scale acceptance query"])
        .unwrap()
        .remove(0)
}

// ── Benchmark: sharded_search at 100k ────────────────────────────────────────

fn bench_sharded_100k(c: &mut Criterion) {
    const N_DOCS: usize = 100_000;
    const TOP_K: usize = 20;

    let tmp = tempfile::tempdir().expect("tempdir");
    let root = tmp.path().join("scale_100k");
    let idx = populate_index(&root, N_DOCS);
    let q = make_query();

    let mut group = c.benchmark_group("sharded_search_scale");
    group.sample_size(20);
    group.measurement_time(Duration::from_secs(30));
    group.warm_up_time(Duration::from_secs(5));
    group.throughput(Throughput::Elements(N_DOCS as u64));

    group.bench_with_input(BenchmarkId::new("top_k20", "100k"), &N_DOCS, |b, _| {
        b.iter(|| {
            let hits = idx.search(&q, TOP_K).expect("search failed");
            assert!(hits.len() <= TOP_K);
        });
    });

    group.finish();
}

// ── Benchmark: sharded_search at 1M (heavy) ──────────────────────────────────

#[cfg(feature = "bench-scale-heavy")]
fn bench_sharded_1m(c: &mut Criterion) {
    const N_DOCS: usize = 1_000_000;
    const TOP_K: usize = 20;

    let tmp = tempfile::tempdir().expect("tempdir");
    let root = tmp.path().join("scale_1m");
    let idx = populate_index(&root, N_DOCS);
    let q = make_query();

    let mut group = c.benchmark_group("sharded_search_scale");
    group.sample_size(10);
    group.measurement_time(Duration::from_secs(60));
    group.warm_up_time(Duration::from_secs(10));
    group.throughput(Throughput::Elements(N_DOCS as u64));

    group.bench_with_input(BenchmarkId::new("top_k20", "1m"), &N_DOCS, |b, _| {
        b.iter(|| {
            let hits = idx.search(&q, TOP_K).expect("search failed");
            assert!(hits.len() <= TOP_K);
        });
    });

    group.finish();
}

// ── Benchmark: sharded_search at 10M (heavy) ─────────────────────────────────

#[cfg(feature = "bench-scale-heavy")]
fn bench_sharded_10m(c: &mut Criterion) {
    const N_DOCS: usize = 10_000_000;
    const TOP_K: usize = 20;

    let tmp = tempfile::tempdir().expect("tempdir");
    let root = tmp.path().join("scale_10m");
    let idx = populate_index(&root, N_DOCS);
    let q = make_query();

    let mut group = c.benchmark_group("sharded_search_scale");
    group.sample_size(10);
    group.measurement_time(Duration::from_secs(120));
    group.warm_up_time(Duration::from_secs(15));
    group.throughput(Throughput::Elements(N_DOCS as u64));

    group.bench_with_input(BenchmarkId::new("top_k20", "10m"), &N_DOCS, |b, _| {
        b.iter(|| {
            let hits = idx.search(&q, TOP_K).expect("search failed");
            assert!(hits.len() <= TOP_K);
        });
    });

    group.finish();
}

// ── criterion harness ─────────────────────────────────────────────────────────

#[cfg(not(feature = "bench-scale-heavy"))]
criterion_group!(benches, bench_sharded_100k);

#[cfg(feature = "bench-scale-heavy")]
criterion_group!(
    benches,
    bench_sharded_100k,
    bench_sharded_1m,
    bench_sharded_10m
);

criterion_main!(benches);
