//! [`IngestPipeline`] — the central parse → chunk → embed → store coordinator.
//!
//! # Responsibilities
//! 1. SHA-256 content-hash check — unchanged files are skipped (idempotent).
//! 2. Old-chunk eviction — changed files have their prior chunks deleted before
//!    re-indexing so the DB never holds stale vectors.
//! 3. Chunker dispatch — selects the right [`Chunker`] based on file extension.
//! 4. Batch embedding — dense + sparse in batches of 32.
//! 5. Atomic transaction — all DB writes for one file commit or roll back together.
//! 6. Progress callback — optional per-chunk notification for UI progress bars.
//!
//! SPORT: MASTER-CRATES.md → cascade-rag::IngestPipeline

use std::path::Path;
use std::sync::Arc;
use std::time::{Instant, SystemTime, UNIX_EPOCH};

use rusqlite::{params, Connection};
use tracing::{debug, info, instrument, warn};

use cascade_types::error::{CascadeError, Result};

use crate::chunk::Chunk;
use crate::embed::{store, EmbedModel};
use crate::index::state::{ChangeKind, IndexStateStore};
use crate::parse::ParseDispatcher;

use super::chunker::{chunker_for_path, file_mtime, hex_sha256};
use super::types::{IngestConfig, IngestResult, IngestStats};

// ── Pipeline ──────────────────────────────────────────────────────────────────

/// Progress callback type: called once per chunk with `(source_path, chunk_index, total_chunks)`.
type ProgressCb = Box<dyn Fn(&Path, usize, usize) + Send + Sync>;

/// Central coordinator for the parse → chunk → embed → index pipeline.
///
/// # Purpose
/// Connects all RAG building blocks behind a single `ingest_file` call.
/// All DB writes for one file are atomic.
///
/// # Inputs
/// - `path`: absolute or relative path to the file to ingest.
/// - DB connection wraps `rag_sources`, `rag_chunks`, and embedding tables.
/// - `embed`: any [`EmbedModel`] implementation (real BGE-M3 or mock for tests).
///
/// # Outputs
/// Returns [`IngestResult`] describing what happened (skip, new, updated).
///
/// # Constraints
/// - The connection must have `PRAGMA foreign_keys = ON` if FK cascade-deletes
///   are desired (the pipeline issues `DELETE FROM rag_sources WHERE id = ?` and
///   relies on cascades to clean `rag_chunks` + embeddings).
/// - Not `Clone` (holds the DB connection by value).
///
/// SPORT: MASTER-CRATES.md → cascade-rag::IngestPipeline
pub struct IngestPipeline {
    pub(super) conn: Connection,
    embed: Arc<dyn EmbedModel>,
    pub(super) config: IngestConfig,
    parser: ParseDispatcher,
    /// Optional progress callback: called once per chunk with (source_path, chunk_index, total_chunks).
    progress: Option<ProgressCb>,
}

impl IngestPipeline {
    /// Create a new pipeline.
    ///
    /// # Parameters
    /// - `conn` — rusqlite connection; migrations must already have been applied.
    /// - `embed` — embed model to use for dense + sparse vector production.
    /// - `config` — tuning parameters.
    pub fn new(conn: Connection, embed: Arc<dyn EmbedModel>, config: IngestConfig) -> Self {
        Self {
            conn,
            embed,
            config,
            parser: ParseDispatcher::default(),
            progress: None,
        }
    }

    /// Attach a progress callback.  Called once per chunk after it is indexed.
    /// Signature: `fn(source_path, chunk_index, total_chunks)`.
    pub fn with_progress<F>(mut self, f: F) -> Self
    where
        F: Fn(&Path, usize, usize) + Send + Sync + 'static,
    {
        self.progress = Some(Box::new(f));
        self
    }

    /// Ingest a single file.
    ///
    /// # Algorithm
    /// 1. Read file bytes; compute SHA-256.
    /// 2. Check `rag_sources`; if hash matches, return `skipped = true`.
    /// 3. Begin transaction.
    /// 4. If a prior entry exists: delete it (cascades to chunks + embeddings).
    /// 5. Parse → chunk → batch-embed → store FTS + embeddings.
    /// 6. Upsert `rag_sources`.
    /// 7. Commit.
    ///
    /// # Errors
    /// Returns `CascadeError::Io` on file-read failure, `CascadeError::ParseFailed`
    /// on parse errors, or `CascadeError::Other` wrapping DB/embed errors.
    #[instrument(skip(self), fields(path = %path.display()))]
    pub fn ingest_file(&self, path: &Path) -> Result<IngestResult> {
        // ── 1. Read + hash ────────────────────────────────────────────────────
        let bytes = std::fs::read(path).map_err(|e| CascadeError::Io {
            path: path.to_path_buf(),
            operation: "read",
            source: e,
        })?;

        let hash = hex_sha256(&bytes);
        let mtime = file_mtime(path);
        let path_str = path.to_string_lossy().to_string();

        // ── 2. Idempotency check ──────────────────────────────────────────────
        if let Some((source_id, stored_hash)) = self.query_source(&path_str)? {
            if stored_hash.as_deref() == Some(&hash) {
                debug!(path = %path.display(), "content hash unchanged — skipping");
                return Ok(IngestResult {
                    source_id,
                    chunks_created: 0,
                    skipped: true,
                });
            }
            // Hash changed: evict old data inside the upcoming transaction.
            debug!(path = %path.display(), "content hash changed — re-ingesting");
        }

        // ── 3–7. Parse → chunk → embed → store (in one transaction) ──────────
        self.ingest_bytes(path, &bytes, &hash, mtime, &path_str)
    }

    /// Ingest multiple files with per-file error isolation.
    ///
    /// Parse/IO failures on one file are collected and returned after processing
    /// all files — they do not abort the batch.
    pub fn ingest_files<'a, I>(
        &self,
        paths: I,
    ) -> (Vec<IngestResult>, Vec<(std::path::PathBuf, CascadeError)>)
    where
        I: IntoIterator<Item = &'a Path>,
    {
        let mut results = Vec::new();
        let mut errors = Vec::new();
        for path in paths {
            match self.ingest_file(path) {
                Ok(r) => results.push(r),
                Err(e) => {
                    warn!(path = %path.display(), error = %e, "ingest failed — continuing batch");
                    errors.push((path.to_path_buf(), e));
                }
            }
        }
        (results, errors)
    }

    /// Delta-scan a set of candidate files, skipping unchanged ones.
    ///
    /// # Algorithm
    /// 1. For each `path` in `candidates`:
    ///    a. If `incremental = false`, always ingest.
    ///    b. Otherwise call [`IndexStateStore::classify`]:
    ///       - `FastSkip` / `HashSkip` → increment `stats.skipped`, continue.
    ///       - `Changed` → ingest, call `state_store.set_hash()`, increment `stats.ingested`.
    /// 2. After the loop call `state_store.evict_deleted()` — removes rows for
    ///    files no longer on disk.
    /// 3. Returns [`IngestStats`] with aggregate counters and total wall-clock duration.
    ///
    /// Per-file errors are logged and counted separately (not in `stats`).
    ///
    /// # Parameters
    /// - `candidates` — paths to examine; caller controls discovery/globbing.
    /// - `state_store` — [`IndexStateStore`] opened at the same index root.
    pub fn ingest_delta<'a, I>(&self, candidates: I, state_store: &IndexStateStore) -> IngestStats
    where
        I: IntoIterator<Item = &'a Path>,
    {
        let start = Instant::now();
        let mut stats = IngestStats::default();

        let paths: Vec<&'a Path> = candidates.into_iter().collect();
        stats.scanned = paths.len();

        for path in &paths {
            if !self.config.incremental {
                // Full re-index mode — skip delta check.
                match self.ingest_file(path) {
                    Ok(_r) => {
                        if let Ok((_, Some(hash))) = state_store.classify(path) {
                            let _ = state_store.set_hash(path, &hash);
                        }
                        stats.ingested += 1;
                    }
                    Err(e) => {
                        warn!(path = %path.display(), error = %e, "delta ingest failed");
                    }
                }
                continue;
            }

            // Incremental mode.
            match state_store.classify(path) {
                Ok((ChangeKind::FastSkip, _)) | Ok((ChangeKind::HashSkip, _)) => {
                    stats.skipped += 1;
                }
                Ok((ChangeKind::Changed, hash_opt)) => match self.ingest_file(path) {
                    Ok(_r) => {
                        let hash = hash_opt.unwrap_or_else(|| {
                            crate::index::state::blake3_file(path).unwrap_or_default()
                        });
                        if !hash.is_empty() {
                            let _ = state_store.set_hash(path, &hash);
                        }
                        stats.ingested += 1;
                    }
                    Err(e) => {
                        warn!(path = %path.display(), error = %e, "delta ingest failed");
                    }
                },
                Err(e) => {
                    warn!(path = %path.display(), error = %e, "classify failed — treating as changed");
                    match self.ingest_file(path) {
                        Ok(_r) => {
                            stats.ingested += 1;
                        }
                        Err(e2) => {
                            warn!(path = %path.display(), error = %e2, "fallback ingest failed");
                        }
                    }
                }
            }
        }

        // Evict deleted files.
        match state_store.evict_deleted() {
            Ok(n) => {
                stats.evicted = n;
            }
            Err(e) => {
                warn!(error = %e, "evict_deleted failed");
            }
        }

        stats.duration = start.elapsed();
        stats
    }
}

// ── Private helpers ───────────────────────────────────────────────────────────

impl IngestPipeline {
    /// Run the full parse→chunk→embed→store pipeline for one file, inside a transaction.
    fn ingest_bytes(
        &self,
        path: &Path,
        bytes: &[u8],
        hash: &str,
        mtime: Option<i64>,
        path_str: &str,
    ) -> Result<IngestResult> {
        // ── Parse ─────────────────────────────────────────────────────────────
        let doc_text = self
            .parser
            .dispatch(path)
            .map_err(|e| CascadeError::ParseFailed {
                path: path.to_path_buf(),
                detail: e.to_string(),
            })?;

        // ── Chunk ─────────────────────────────────────────────────────────────
        let chunker = chunker_for_path(path, &self.config.chunker_config);
        let chunks = chunker
            .chunk(path, &doc_text.text)
            .map_err(|e| CascadeError::Other(format!("chunk error: {e}")))?;

        // ── Begin transaction ─────────────────────────────────────────────────
        self.conn
            .execute_batch("BEGIN")
            .map_err(|e| CascadeError::Other(e.to_string()))?;

        let result = self.ingest_in_txn(path, path_str, bytes, hash, mtime, &chunks);

        match result {
            Ok(r) => {
                self.conn
                    .execute_batch("COMMIT")
                    .map_err(|e| CascadeError::Other(e.to_string()))?;
                Ok(r)
            }
            Err(e) => {
                let _ = self.conn.execute_batch("ROLLBACK");
                Err(e)
            }
        }
    }

    /// All DB writes inside the open transaction.
    fn ingest_in_txn(
        &self,
        path: &Path,
        path_str: &str,
        bytes: &[u8],
        hash: &str,
        mtime: Option<i64>,
        chunks: &[Chunk],
    ) -> Result<IngestResult> {
        // ── Enable FK so CASCADE deletes work ─────────────────────────────────
        self.conn
            .execute_batch("PRAGMA foreign_keys = ON")
            .map_err(|e| CascadeError::Other(e.to_string()))?;

        // ── Evict any prior entry ─────────────────────────────────────────────
        // vec0 virtual tables ignore FK cascades, so embedding rows must be
        // deleted explicitly while the chunk rows still exist for the
        // chunk_id subquery. Then the rag_sources delete cascades chunks+FTS.
        let prior_id: Option<i64> = self
            .conn
            .query_row(
                "SELECT id FROM rag_sources WHERE file_path = ?1",
                params![path_str],
                |r| r.get(0),
            )
            .ok();
        if let Some(prior) = prior_id {
            crate::embed::store::delete_embeddings_by_source(&self.conn, prior)
                .map_err(|e| CascadeError::Other(e.to_string()))?;
        }
        self.conn
            .execute(
                "DELETE FROM rag_sources WHERE file_path = ?1",
                params![path_str],
            )
            .map_err(|e| CascadeError::Other(e.to_string()))?;

        // ── Insert new rag_sources row ────────────────────────────────────────
        let now_secs = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map(|d| d.as_secs() as i64)
            .ok();

        self.conn
            .execute(
                "INSERT INTO rag_sources \
                 (file_path, content_hash, mtime, byte_size, indexed_at, schema_version) \
                 VALUES (?1, ?2, ?3, ?4, ?5, ?6)",
                params![
                    path_str,
                    hash,
                    mtime,
                    bytes.len() as i64,
                    now_secs,
                    self.config.schema_version,
                ],
            )
            .map_err(|e| CascadeError::Other(e.to_string()))?;

        let source_id = self.conn.last_insert_rowid();

        // ── Insert chunks + embed in batches ──────────────────────────────────
        let total_chunks = chunks.len();
        let batch_size = self.config.embed_batch_size.max(1);

        info!(
            path = %path.display(),
            source_id,
            chunks = total_chunks,
            "ingesting"
        );

        let mut chunks_written = 0usize;

        for batch_start in (0..total_chunks).step_by(batch_size) {
            let batch_end = (batch_start + batch_size).min(total_chunks);
            let batch = &chunks[batch_start..batch_end];

            // Collect texts for embedding.
            let texts: Vec<&str> = batch.iter().map(|c| c.text.as_str()).collect();

            // Dense embeddings.
            let dense_vecs =
                self.embed
                    .embed_dense(&texts)
                    .map_err(|e| CascadeError::EmbeddingFailed {
                        provider: self.embed.model_id().to_string(),
                        detail: e.to_string(),
                    })?;

            // Sparse embeddings.
            let sparse_vecs =
                self.embed
                    .embed_sparse(&texts)
                    .map_err(|e| CascadeError::EmbeddingFailed {
                        provider: self.embed.model_id().to_string(),
                        detail: e.to_string(),
                    })?;

            // Insert each chunk in this batch.
            for (i, chunk) in batch.iter().enumerate() {
                let chunk_index = batch_start + i;

                // Insert rag_chunks row.
                self.conn
                    .execute(
                        "INSERT INTO rag_chunks \
                         (source_id, chunk_index, chunk_text, char_start, line_start, line_end, \
                          parent_chunk_id, heading_path, schema_version) \
                         VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9)",
                        params![
                            source_id,
                            chunk_index as i64,
                            chunk.text,
                            chunk.char_start as i64,
                            chunk.line_start as i64,
                            chunk.line_end as i64,
                            chunk.parent_chunk_id,
                            chunk.heading_path.as_deref(),
                            self.config.schema_version,
                        ],
                    )
                    .map_err(|e| CascadeError::Other(e.to_string()))?;

                let chunk_id = self.conn.last_insert_rowid();

                // FTS5 is synced automatically by the rag_fts5_ai trigger on
                // rag_chunks INSERT (migration 0005).  No explicit index_chunk call
                // needed here — that function also inserts a sentinel source row
                // (id=0) which would corrupt source-count invariants.

                // Store embeddings (outside the per-connection BEGIN/COMMIT since
                // we are already inside a transaction — store_dense/store_sparse
                // use plain execute without wrapping their own txn here).
                store::store_dense(&self.conn, chunk_id, &dense_vecs[i]).map_err(|e| {
                    CascadeError::EmbeddingFailed {
                        provider: "store_dense".into(),
                        detail: e.to_string(),
                    }
                })?;

                store::store_sparse(&self.conn, chunk_id, &sparse_vecs[i]).map_err(|e| {
                    CascadeError::EmbeddingFailed {
                        provider: "store_sparse".into(),
                        detail: e.to_string(),
                    }
                })?;

                chunks_written += 1;

                if let Some(cb) = &self.progress {
                    cb(path, chunk_index, total_chunks);
                }
            }
        }

        Ok(IngestResult {
            source_id,
            chunks_created: chunks_written,
            skipped: false,
        })
    }

    /// Return `(source_id, content_hash)` for an existing source, or `None`.
    fn query_source(&self, path_str: &str) -> Result<Option<(i64, Option<String>)>> {
        let mut stmt = self
            .conn
            .prepare_cached("SELECT id, content_hash FROM rag_sources WHERE file_path = ?1")
            .map_err(|e| CascadeError::Other(e.to_string()))?;

        let mut rows = stmt
            .query(params![path_str])
            .map_err(|e| CascadeError::Other(e.to_string()))?;

        if let Some(row) = rows
            .next()
            .map_err(|e| CascadeError::Other(e.to_string()))?
        {
            let id: i64 = row.get(0).map_err(|e| CascadeError::Other(e.to_string()))?;
            let hash: Option<String> =
                row.get(1).map_err(|e| CascadeError::Other(e.to_string()))?;
            Ok(Some((id, hash)))
        } else {
            Ok(None)
        }
    }
}
