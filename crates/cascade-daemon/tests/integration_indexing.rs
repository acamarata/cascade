//! Integration tests for the parallel indexing pipeline (T-P4-E04-03).
//!
//! Purpose: verify that IndexingPipeline correctly processes ingest and evict
//! signals via the WorkerPool, and that the worker_queue_depth is 0 after
//! all work completes.
//!
//! Tests:
//!  1. pipeline_ingest_signals_processed — 20 WatchSignal::Ingest events are all
//!     processed (ingest or gracefully skipped) within 5 seconds.
//!  2. pipeline_evict_no_panic — WatchSignal::Evict events do not panic.
//!  3. pipeline_queue_depth_zero_after_drain — queue_depth is 0 after shutdown.
//!  4. worker_pool_queue_depth_zero_idle — idle pool always reports depth 0.
//!
//! Note: IndexingPipeline calls IngestPipeline under spawn_blocking. For files
//! that don't exist, ingest_file returns CascadeError::Io — the pipeline logs
//! and continues. We write real temp files to exercise the happy path.
//!
//! SPORT: MASTER-COMPONENTS.md → cascade-daemon IndexingPipeline (T-P4-E04-03)

use std::fs;
use std::sync::Arc;
use std::time::{Duration, Instant};

use tempfile::TempDir;
use tokio::sync::mpsc;
use tokio_util::sync::CancellationToken;

use cascade_daemon::indexer::{IndexingPipeline, worker_queue_depth};
use cascade_daemon::rag_watcher::WatchSignal;
use cascade_rag::IndexManager;
use cascade_rag::embed::MockEmbedModel;
use cascade_rag::workers::{WorkerPool, WorkerPoolConfig};

// ── Helpers ────────────────────────────────────────────────────────────────────

fn make_pool(workers: usize) -> Arc<WorkerPool> {
    let cfg = WorkerPoolConfig {
        max_embed_workers: workers,
        embed_batch_size: 4,
        max_queue_depth: 16,
    };
    // Use 1024-dim to match the default DB schema (store_dense validates dim).
    Arc::new(WorkerPool::new(cfg, Arc::new(MockEmbedModel::new(1024))))
}

async fn make_index_manager(tmp: &TempDir) -> Arc<IndexManager> {
    IndexManager::open(tmp.path())
        .await
        .expect("IndexManager::open")
}

// ── Tests ─────────────────────────────────────────────────────────────────────

/// Sanity: IngestPipeline directly writes sources to DB opened from IndexManager path.
#[tokio::test]
async fn direct_ingest_pipeline_sanity() {
    use cascade_rag::ingest::{IngestConfig, IngestPipeline};
    use cascade_rag::embed::MockEmbedModel;
    use rusqlite::Connection;
    use std::sync::Arc;

    let tmp = TempDir::new().unwrap();
    let index_mgr = make_index_manager(&tmp).await;

    let db_path = index_mgr.db_path().to_path_buf();
    let p = tmp.path().join("sanity.md");
    fs::write(&p, "# Sanity\nHello world\n").unwrap();

    let embed = Arc::new(MockEmbedModel::new(1024));
    let result = tokio::task::spawn_blocking(move || {
        let conn = Connection::open(&db_path).expect("open db");
        conn.execute_batch("PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;").expect("pragmas");
        let pipeline = IngestPipeline::new(conn, embed, IngestConfig::default());
        pipeline.ingest_file(&p)
    }).await.expect("spawn_blocking").expect("ingest_file");

    eprintln!("sanity result: {:?}", result);
    assert!(!result.skipped, "should not skip a new file");
    assert!(result.chunks_created > 0, "should have chunks");

    let sources = index_mgr.list_sources().await.unwrap();
    eprintln!("sources after direct ingest: {}", sources.len());
    assert_eq!(sources.len(), 1, "should have 1 source");
}

/// Verify that 20 Ingest signals are all handled without panic within 5 seconds.
///
/// Files that don't exist will produce logged errors (CascadeError::Io) which
/// the pipeline isolates per file. Files that do exist are ingested normally.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn pipeline_ingest_signals_processed() {
    let tmp = TempDir::new().unwrap();
    let pool = make_pool(2);
    let index_mgr = make_index_manager(&tmp).await;
    let embed: Arc<dyn cascade_rag::embed::EmbedModel> = Arc::new(MockEmbedModel::new(1024));

    let (tx, rx) = mpsc::channel::<WatchSignal>(64);
    let shutdown = CancellationToken::new();
    let shutdown2 = shutdown.clone();

    // Write 20 real files into the tmp dir.
    let n = 20usize;
    let mut paths = Vec::with_capacity(n);
    for i in 0..n {
        let p = tmp.path().join(format!("file_{i}.md"));
        fs::write(&p, format!("# Document {i}\nContent for document number {i}.\n")).unwrap();
        paths.push(p);
    }

    // Spawn pipeline.
    let pipeline = IndexingPipeline::new(
        rx,
        Arc::clone(&pool),
        Arc::clone(&index_mgr),
        embed,
        shutdown.clone(),
    );
    let handle = tokio::spawn(pipeline.run());

    // Send all 20 ingest signals.
    for path in &paths {
        tx.send(WatchSignal::Ingest(path.clone())).await.unwrap();
    }
    // Drop tx so the pipeline can drain and exit naturally when we cancel.
    drop(tx);

    // Poll until the index has processed all signals (or 30 s timeout).
    // Each file ingest involves: channel recv → spawn_blocking → rusqlite open
    // → IngestPipeline::ingest_file → chunking → embedding → DB write.
    // Allow generous time on slow CI environments.
    let deadline = Instant::now() + Duration::from_secs(30);
    loop {
        if Instant::now() > deadline {
            break; // allow assertion below to fail
        }
        // Check how many sources are indexed.
        let sources = index_mgr.list_sources().await.unwrap_or_default();
        if sources.len() >= n {
            break;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }

    // Shut down the pipeline.
    shutdown2.cancel();
    handle.await.expect("pipeline task did not panic");

    // After drain, queue depth must be 0.
    assert_eq!(worker_queue_depth(&pool), 0, "queue_depth must be 0 after shutdown");

    // All 20 files must be indexed.
    let sources = index_mgr.list_sources().await.unwrap_or_default();
    assert_eq!(
        sources.len(), n,
        "expected {n} indexed sources, got {}",
        sources.len()
    );
}

/// Evict signals for non-existent files must not panic.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn pipeline_evict_no_panic() {
    let tmp = TempDir::new().unwrap();
    let pool = make_pool(2);
    let index_mgr = make_index_manager(&tmp).await;
    let embed: Arc<dyn cascade_rag::embed::EmbedModel> = Arc::new(MockEmbedModel::new(1024));

    let (tx, rx) = mpsc::channel::<WatchSignal>(16);
    let shutdown = CancellationToken::new();
    let shutdown2 = shutdown.clone();

    let pipeline = IndexingPipeline::new(rx, pool, index_mgr, embed, shutdown);
    let handle = tokio::spawn(pipeline.run());

    // Send evict for a path that does not exist — must not panic.
    for i in 0..5u32 {
        let ghost = tmp.path().join(format!("ghost_{i}.md"));
        tx.send(WatchSignal::Evict(ghost)).await.unwrap();
    }

    tokio::time::sleep(Duration::from_millis(200)).await;
    shutdown2.cancel();
    handle.await.expect("pipeline must not panic on phantom evict");
}

/// After shutdown, queue depth must be 0.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn pipeline_queue_depth_zero_after_drain() {
    let tmp = TempDir::new().unwrap();
    let pool = make_pool(2);
    let index_mgr = make_index_manager(&tmp).await;
    let embed: Arc<dyn cascade_rag::embed::EmbedModel> = Arc::new(MockEmbedModel::new(1024));

    let (tx, rx) = mpsc::channel::<WatchSignal>(32);
    let shutdown = CancellationToken::new();
    let shutdown2 = shutdown.clone();

    // Write a file to ingest.
    let p = tmp.path().join("drain_test.md");
    fs::write(&p, "# Drain test\nSome content.\n").unwrap();

    let pipeline = IndexingPipeline::new(
        rx,
        Arc::clone(&pool),
        index_mgr,
        embed,
        shutdown,
    );
    let handle = tokio::spawn(pipeline.run());

    tx.send(WatchSignal::Ingest(p)).await.unwrap();
    // Allow pipeline to process.
    tokio::time::sleep(Duration::from_millis(300)).await;

    shutdown2.cancel();
    handle.await.expect("pipeline task did not panic");

    assert_eq!(
        worker_queue_depth(&pool), 0,
        "queue_depth must be 0 after drain"
    );
}

/// Idle pool always reports depth 0 — also verifies the helper function.
#[test]
fn worker_pool_queue_depth_zero_idle() {
    let pool = make_pool(2);
    assert_eq!(worker_queue_depth(&pool), 0, "idle pool must report depth=0");
}
