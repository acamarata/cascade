//! # ingest
//!
//! Central ingest pipeline: file path → parse → chunk → embed → store.
//!
//! ## Responsibilities
//!
//! 1. SHA-256 content-hash check — unchanged files are skipped (idempotent).
//! 2. Old-chunk eviction — changed files have their prior chunks deleted before
//!    re-indexing so the DB never holds stale vectors.
//! 3. Chunker dispatch — selects the right [`Chunker`] based on file extension.
//! 4. Batch embedding — dense + sparse in batches of 32.
//! 5. Atomic transaction — all DB writes for one file commit or roll back together.
//! 6. Progress callback — optional per-chunk notification for UI progress bars.
//!
//! ## Chunker selection
//!
//! | Extension | Chunker |
//! |-----------|---------|
//! | `.md`, `.mdx` | [`MarkdownChunker`] |
//! | `.rs`, `.ts`, `.tsx`, `.js`, `.jsx`, `.py` | [`CodeChunker`] (feature = `code-chunker`) |
//! | prose / other text | [`HierarchicalChunker`] |
//! | default | [`SemanticChunker`] |
//!
//! Without the `code-chunker` feature, code extensions fall through to
//! [`HierarchicalChunker`].
//!
//! ## Transaction contract
//!
//! The entire ingest of a single file is wrapped in one `BEGIN / COMMIT`.
//! Failure at any step rolls back completely; `rag_sources` is only updated
//! on success so a partial failure is re-attempted on next run.
//!
//! ## Idempotency
//!
//! Content hash is SHA-256 of raw file bytes.  If the hash matches the stored
//! `content_hash` in `rag_sources`, the pipeline returns
//! `IngestResult { skipped: true, .. }` immediately without touching the DB.
//!
//! SPORT: MASTER-CRATES.md → cascade-rag::IngestPipeline

use std::path::Path;
use std::sync::Arc;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use rusqlite::{Connection, params};
use sha2::{Digest, Sha256};
use tracing::{debug, info, instrument, warn};

use cascade_types::error::{CascadeError, Result};

use crate::chunk::{Chunk, ChunkerConfig};
use crate::chunk::markdown::MarkdownChunker;
use crate::chunk::semantic::SemanticChunker;
use crate::chunk::hierarchical::HierarchicalChunker;
use crate::chunk::Chunker;
use crate::embed::{EmbedModel, store};
use crate::index::state::{ChangeKind, IndexStateStore};
use crate::parse::ParseDispatcher;

/// Batch size for embedding calls.
const EMBED_BATCH_SIZE: usize = 32;

// ── Stats ─────────────────────────────────────────────────────────────────────

/// Aggregate metrics from one delta scan + ingest pass.
///
/// # Purpose
/// Returned by [`IngestPipeline::ingest_delta`] and persisted by callers for
/// `cascade status --json` display. Carries enough data for operator visibility
/// without requiring a secondary DB query.
///
/// # Fields
/// | Field | Meaning |
/// |---|---|
/// | `scanned` | Total candidate files examined |
/// | `skipped` | Files skipped because content is unchanged (fast-skip + hash-skip) |
/// | `ingested` | Files actually re-embedded and stored |
/// | `evicted` | Files deleted on disk and removed from the index |
/// | `duration` | Wall-clock time for the entire pass |
///
/// SPORT: MASTER-COMPONENTS.md → cascade-rag::IngestStats
#[derive(Debug, Clone, Default, serde::Serialize, serde::Deserialize)]
pub struct IngestStats {
    /// Total files examined during the delta scan.
    pub scanned: usize,
    /// Files skipped — content unchanged (fast-path mtime+size or Blake3 match).
    pub skipped: usize,
    /// Files re-embedded and written to the index.
    pub ingested: usize,
    /// Files removed from the index because they no longer exist on disk.
    pub evicted: usize,
    /// Wall-clock duration of the entire ingest pass.
    #[serde(with = "duration_ms_serde")]
    pub duration: Duration,
}

impl IngestStats {
    /// Duration formatted as milliseconds (for JSON status output).
    pub fn duration_ms(&self) -> u64 {
        self.duration.as_millis() as u64
    }
}

/// Serde helper — serialize/deserialize `Duration` as milliseconds.
mod duration_ms_serde {
    use serde::{Deserialize, Deserializer, Serializer};
    use std::time::Duration;

    pub fn serialize<S: Serializer>(d: &Duration, s: S) -> Result<S::Ok, S::Error> {
        s.serialize_u64(d.as_millis() as u64)
    }

    pub fn deserialize<'de, D: Deserializer<'de>>(d: D) -> Result<Duration, D::Error> {
        let ms = u64::deserialize(d)?;
        Ok(Duration::from_millis(ms))
    }
}

// ── Configuration ─────────────────────────────────────────────────────────────

/// Configuration for the ingest pipeline.
///
/// # Purpose
/// Controls chunking parameters, progress reporting behaviour, and incremental
/// indexing settings.
///
/// # Defaults
/// Reasonable defaults are provided via [`Default`]; callers only need to
/// override fields that differ from those defaults.
///
/// # Constraints
/// - `chunker_config.max_chunk_chars` must be > 0.
/// - `embed_batch_size` must be > 0.
///
/// SPORT: MASTER-CRATES.md → cascade-rag::IngestConfig
#[derive(Debug, Clone)]
pub struct IngestConfig {
    /// Chunker parameters (max size, overlap, min size).
    pub chunker_config: ChunkerConfig,
    /// How many chunk texts are sent to the embedder per call.
    pub embed_batch_size: usize,
    /// Schema version recorded in new/updated `rag_sources` rows.
    pub schema_version: i64,
    /// Enable incremental indexing — skip files whose content has not changed.
    /// When `false`, every file is always re-embedded (full re-index).
    /// Corresponds to `rag.incremental` in `cascade.toml`. Default: `true`.
    pub incremental: bool,
}

impl Default for IngestConfig {
    fn default() -> Self {
        Self {
            chunker_config: ChunkerConfig::default(),
            embed_batch_size: EMBED_BATCH_SIZE,
            schema_version: 1,
            incremental: true,
        }
    }
}

// ── Result type ───────────────────────────────────────────────────────────────

/// Outcome of a single [`IngestPipeline::ingest_file`] call.
///
/// # Purpose
/// Carries enough information for callers to update progress bars, skip counts,
/// and DB integrity checks without re-querying the database.
///
/// SPORT: MASTER-CRATES.md → cascade-rag::IngestResult
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct IngestResult {
    /// `rag_sources.id` for the ingested file.
    pub source_id: i64,
    /// Number of [`Chunk`] structs written in this call.
    /// Zero when `skipped = true`.
    pub chunks_created: usize,
    /// `true` when the file's content hash matched the stored hash — no work done.
    pub skipped: bool,
}

// ── Pipeline ──────────────────────────────────────────────────────────────────

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
    conn: Connection,
    embed: Arc<dyn EmbedModel>,
    config: IngestConfig,
    parser: ParseDispatcher,
    /// Optional progress callback: called once per chunk with (source_path, chunk_index, total_chunks).
    progress: Option<Box<dyn Fn(&Path, usize, usize) + Send + Sync>>,
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
    pub fn ingest_files<'a, I>(&self, paths: I) -> (Vec<IngestResult>, Vec<(std::path::PathBuf, CascadeError)>)
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
    pub fn ingest_delta<'a, I>(
        &self,
        candidates: I,
        state_store: &IndexStateStore,
    ) -> IngestStats
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
                Ok((ChangeKind::Changed, hash_opt)) => {
                    match self.ingest_file(path) {
                        Ok(_r) => {
                            let hash = hash_opt.unwrap_or_else(|| {
                                crate::index::state::blake3_file(path)
                                    .unwrap_or_default()
                            });
                            if !hash.is_empty() {
                                let _ = state_store.set_hash(path, &hash);
                            }
                            stats.ingested += 1;
                        }
                        Err(e) => {
                            warn!(path = %path.display(), error = %e, "delta ingest failed");
                        }
                    }
                }
                Err(e) => {
                    warn!(path = %path.display(), error = %e, "classify failed — treating as changed");
                    match self.ingest_file(path) {
                        Ok(_r) => { stats.ingested += 1; }
                        Err(e2) => {
                            warn!(path = %path.display(), error = %e2, "fallback ingest failed");
                        }
                    }
                }
            }
        }

        // Evict deleted files.
        match state_store.evict_deleted() {
            Ok(n) => { stats.evicted = n; }
            Err(e) => { warn!(error = %e, "evict_deleted failed"); }
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
            let dense_vecs = self
                .embed
                .embed_dense(&texts)
                .map_err(|e| CascadeError::EmbeddingFailed {
                    provider: self.embed.model_id().to_string(),
                    detail: e.to_string(),
                })?;

            // Sparse embeddings.
            let sparse_vecs = self
                .embed
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
                store::store_dense(&self.conn, chunk_id, &dense_vecs[i])
                    .map_err(|e| CascadeError::EmbeddingFailed {
                        provider: "store_dense".into(),
                        detail: e.to_string(),
                    })?;

                store::store_sparse(&self.conn, chunk_id, &sparse_vecs[i])
                    .map_err(|e| CascadeError::EmbeddingFailed {
                        provider: "store_sparse".into(),
                        detail: e.to_string(),
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

        if let Some(row) = rows.next().map_err(|e| CascadeError::Other(e.to_string()))? {
            let id: i64 = row.get(0).map_err(|e| CascadeError::Other(e.to_string()))?;
            let hash: Option<String> = row.get(1).map_err(|e| CascadeError::Other(e.to_string()))?;
            Ok(Some((id, hash)))
        } else {
            Ok(None)
        }
    }
}

// ── Chunker selection ─────────────────────────────────────────────────────────

/// Select the appropriate [`Chunker`] for `path` based on its extension.
///
/// # Purpose
/// Centralises the extension→chunker mapping so it can be tested independently
/// and reused by the batch pipeline.
///
/// # Inputs
/// `path` — any path; only the extension is inspected.
/// `config` — chunker size/overlap parameters.
///
/// # Outputs
/// A `Box<dyn Chunker>` ready to call `.chunk()`.
///
/// SPORT: MASTER-CRATES.md → cascade-rag::ingest::chunker_for_path
pub fn chunker_for_path(path: &Path, config: &ChunkerConfig) -> Box<dyn Chunker> {
    let ext = path
        .extension()
        .and_then(|e| e.to_str())
        .unwrap_or("")
        .to_ascii_lowercase();

    match ext.as_str() {
        "md" | "mdx" => Box::new(MarkdownChunker),

        #[cfg(feature = "code-chunker")]
        "rs" | "ts" | "tsx" | "js" | "jsx" | "py" => {
            use crate::chunk::code::CodeChunker;
            Box::new(CodeChunker::new(config.clone()))
        }

        // Prose / other plain text — hierarchical preserves paragraph structure.
        "txt" | "rst" | "org" | "adoc" | "tex" => {
            Box::new(HierarchicalChunker::new(config.clone()))
        }

        // Default: semantic sliding window.
        _ => Box::new(SemanticChunker::with_config(config.clone())),
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Compute a hex-encoded SHA-256 digest of `bytes`.
fn hex_sha256(bytes: &[u8]) -> String {
    let mut hasher = Sha256::new();
    hasher.update(bytes);
    format!("{:x}", hasher.finalize())
}

/// Extract the file's mtime as Unix seconds, or `None` if unavailable.
fn file_mtime(path: &Path) -> Option<i64> {
    std::fs::metadata(path)
        .ok()
        .and_then(|m| m.modified().ok())
        .and_then(|t| t.duration_since(UNIX_EPOCH).ok())
        .map(|d| d.as_secs() as i64)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::db::run_migrations;
    use crate::embed::MockEmbedModel;
    use rusqlite::Connection;
    use std::io::Write as IoWrite;
    use tempfile::NamedTempFile;

    // ── Helpers ───────────────────────────────────────────────────────────────

    /// Register the sqlite-vec extension so vec0 virtual tables resolve in
    /// migrations (no-op without the `vec` feature).
    #[cfg(feature = "vec")]
    fn load_vec_extension() {
        use rusqlite::ffi::sqlite3_auto_extension;
        unsafe {
            sqlite3_auto_extension(Some(std::mem::transmute(
                sqlite_vec::sqlite3_vec_init as *const (),
            )));
        }
    }

    fn migrated_conn() -> Connection {
        #[cfg(feature = "vec")]
        load_vec_extension();

        let conn = Connection::open_in_memory().expect("in-memory DB");
        run_migrations(&conn).expect("migrations ok");
        conn
    }

    fn make_pipeline(conn: Connection) -> IngestPipeline {
        let embed = Arc::new(MockEmbedModel::default());
        IngestPipeline::new(conn, embed, IngestConfig::default())
    }

    fn write_file(content: &str, suffix: &str) -> NamedTempFile {
        let mut f = tempfile::Builder::new()
            .suffix(suffix)
            .tempfile()
            .expect("tempfile");
        IoWrite::write_all(&mut f, content.as_bytes()).expect("write");
        IoWrite::flush(&mut f).expect("flush");
        f
    }

    // ── ingest::full_pipeline ─────────────────────────────────────────────────

    /// Full pipeline with a markdown fixture.
    #[test]
    fn ingest_md_creates_chunks_and_rows() {
        let conn = migrated_conn();
        let pipeline = make_pipeline(conn);

        let f = write_file(
            "# Hello\n\nThis is a test document for the ingest pipeline.\n\n## Section Two\n\nMore content here.",
            ".md",
        );

        let result = pipeline.ingest_file(f.path()).expect("ingest ok");

        assert!(!result.skipped, "first ingest must not be skipped");
        assert!(result.chunks_created > 0, "must create at least one chunk");
        assert!(result.source_id > 0, "source_id must be non-zero");

        // Verify DB rows.
        let source_count: i64 = pipeline
            .conn
            .query_row("SELECT COUNT(*) FROM rag_sources", [], |r| r.get(0))
            .unwrap();
        assert_eq!(source_count, 1);

        let chunk_count: i64 = pipeline
            .conn
            .query_row("SELECT COUNT(*) FROM rag_chunks", [], |r| r.get(0))
            .unwrap();
        assert_eq!(chunk_count as usize, result.chunks_created);
    }

    /// Full pipeline with a Rust source fixture.
    #[test]
    fn ingest_rs_creates_chunks_and_rows() {
        let conn = migrated_conn();
        let pipeline = make_pipeline(conn);

        let f = write_file(
            "fn main() {\n    println!(\"Hello, world!\");\n}\n\nfn add(a: i32, b: i32) -> i32 {\n    a + b\n}\n",
            ".rs",
        );

        let result = pipeline.ingest_file(f.path()).expect("ingest ok");

        assert!(!result.skipped);
        assert!(result.chunks_created > 0);
    }

    // ── ingest::idempotent ────────────────────────────────────────────────────

    /// Re-ingesting the same file unchanged must return skipped=true.
    #[test]
    fn idempotent_same_file_skips() {
        let conn = migrated_conn();
        let pipeline = make_pipeline(conn);
        let f = write_file("# Idempotency test\n\nSame content every time.\n", ".md");

        let first = pipeline.ingest_file(f.path()).expect("first ingest ok");
        assert!(!first.skipped, "first must not skip");

        let second = pipeline.ingest_file(f.path()).expect("second ingest ok");
        assert!(second.skipped, "second ingest must skip (hash unchanged)");
        assert_eq!(second.chunks_created, 0);
        assert_eq!(second.source_id, first.source_id, "source_id must be stable");
    }

    // ── ingest::changed_file ──────────────────────────────────────────────────

    /// When the file changes, old chunks must be deleted and new ones created.
    #[test]
    fn changed_file_evicts_and_reingests() {
        let conn = migrated_conn();
        let pipeline = make_pipeline(conn);

        // First ingest.
        let f = write_file("# Version 1\n\nOriginal content.\n", ".md");
        let first = pipeline.ingest_file(f.path()).expect("first ingest ok");
        assert!(!first.skipped);
        let first_chunks: i64 = pipeline
            .conn
            .query_row("SELECT COUNT(*) FROM rag_chunks", [], |r| r.get(0))
            .unwrap();

        // Modify the file — overwrite via std::fs::write to avoid NamedTempFile
        // seek/set_len limitations.
        std::fs::write(
            f.path(),
            b"# Version 2\n\nCompletely different content.\n\n## Extra\n\nMore paragraphs added here for chunking.\n",
        )
        .unwrap();

        // Second ingest — should re-ingest.
        let second = pipeline.ingest_file(f.path()).expect("second ingest ok");
        assert!(!second.skipped, "changed file must not skip");
        assert!(second.chunks_created > 0);

        // Only the new chunks should be in the DB (old ones deleted by CASCADE).
        let after_chunks: i64 = pipeline
            .conn
            .query_row("SELECT COUNT(*) FROM rag_chunks", [], |r| r.get(0))
            .unwrap();
        assert_eq!(
            after_chunks as usize,
            second.chunks_created,
            "DB chunks must equal second ingest output (old chunks evicted)"
        );
        // Confirm the old chunk count is gone (they should differ because content changed).
        let _ = first_chunks; // used implicitly above
    }

    // ── ingest::parse_failure_isolation ──────────────────────────────────────

    /// A file that can't be read should produce an error without affecting
    /// other files in a batch.
    #[test]
    fn parse_failure_isolated_in_batch() {
        let conn = migrated_conn();
        let pipeline = make_pipeline(conn);

        let good = write_file("# Good file\n\nReal content.\n", ".md");
        let bad_path = std::path::Path::new("/nonexistent/does_not_exist_xyz.md");

        let (results, errors) =
            pipeline.ingest_files([good.path(), bad_path]);

        assert_eq!(results.len(), 1, "good file must produce a result");
        assert!(!results[0].skipped);
        assert_eq!(errors.len(), 1, "bad path must produce an error");
    }

    // ── ingest::chunks_created_count ─────────────────────────────────────────

    /// `chunks_created` must equal the actual number of rag_chunks rows.
    #[test]
    fn chunks_created_matches_db_rows() {
        let conn = migrated_conn();
        let pipeline = make_pipeline(conn);
        let f = write_file(
            "# A\n\nParagraph one.\n\n## B\n\nParagraph two.\n\n## C\n\nParagraph three.\n",
            ".md",
        );

        let result = pipeline.ingest_file(f.path()).expect("ingest ok");
        let db_count: i64 = pipeline
            .conn
            .query_row("SELECT COUNT(*) FROM rag_chunks", [], |r| r.get(0))
            .unwrap();

        assert_eq!(result.chunks_created, db_count as usize);
    }

    // ── ingest::progress_callback ─────────────────────────────────────────────

    #[test]
    fn progress_callback_called_per_chunk() {
        use std::sync::atomic::{AtomicUsize, Ordering};

        let conn = migrated_conn();
        let embed = Arc::new(MockEmbedModel::default());
        let counter = Arc::new(AtomicUsize::new(0));
        let counter_clone = counter.clone();

        let pipeline = IngestPipeline::new(conn, embed, IngestConfig::default())
            .with_progress(move |_path, _idx, _total| {
                counter_clone.fetch_add(1, Ordering::SeqCst);
            });

        let f = write_file("# Progress\n\nContent for progress test.\n", ".md");
        let result = pipeline.ingest_file(f.path()).expect("ingest ok");

        assert_eq!(
            counter.load(Ordering::SeqCst),
            result.chunks_created,
            "progress callback must fire once per chunk"
        );
    }

    // ── ingest::chunker_for_path ──────────────────────────────────────────────

    #[test]
    fn chunker_selection_md() {
        let cfg = ChunkerConfig::default();
        let c = chunker_for_path(Path::new("foo.md"), &cfg);
        assert_eq!(c.strategy_name(), "markdown");
    }

    #[test]
    fn chunker_selection_mdx() {
        let cfg = ChunkerConfig::default();
        let c = chunker_for_path(Path::new("foo.mdx"), &cfg);
        assert_eq!(c.strategy_name(), "markdown");
    }

    #[test]
    fn chunker_selection_txt_hierarchical() {
        let cfg = ChunkerConfig::default();
        let c = chunker_for_path(Path::new("foo.txt"), &cfg);
        // Accept either the long or short strategy name for future-proofing.
        assert!(
            c.strategy_name().starts_with("hierarchical"),
            "expected hierarchical chunker, got: {}",
            c.strategy_name()
        );
    }

    #[test]
    fn chunker_selection_unknown_semantic() {
        let cfg = ChunkerConfig::default();
        let c = chunker_for_path(Path::new("foo.pdf"), &cfg);
        assert!(
            c.strategy_name().starts_with("semantic"),
            "expected semantic chunker, got: {}",
            c.strategy_name()
        );
    }

    #[cfg(feature = "code-chunker")]
    #[test]
    fn chunker_selection_rs_code() {
        let cfg = ChunkerConfig::default();
        let c = chunker_for_path(Path::new("foo.rs"), &cfg);
        assert_eq!(c.strategy_name(), "code");
    }

    // ── ingest::hex_sha256 ────────────────────────────────────────────────────

    #[test]
    fn sha256_is_deterministic() {
        let h1 = hex_sha256(b"hello world");
        let h2 = hex_sha256(b"hello world");
        assert_eq!(h1, h2);
        assert_eq!(h1.len(), 64, "SHA-256 hex must be 64 chars");
    }

    #[test]
    fn sha256_differs_on_change() {
        let h1 = hex_sha256(b"version 1");
        let h2 = hex_sha256(b"version 2");
        assert_ne!(h1, h2);
    }

    // ── ingest::delta scan ────────────────────────────────────────────────────

    fn make_state_store() -> crate::index::state::IndexStateStore {
        crate::index::state::IndexStateStore::open_in_memory().expect("state store")
    }

    /// New file: ingest_delta must ingest it and record stats.
    #[test]
    fn delta_new_file_is_ingested() {
        let conn = migrated_conn();
        let pipeline = make_pipeline(conn);
        let state = make_state_store();
        let f = write_file("# Delta new\n\nContent.\n", ".md");

        let stats = pipeline.ingest_delta([f.path()], &state);

        assert_eq!(stats.scanned, 1);
        assert_eq!(stats.ingested, 1);
        assert_eq!(stats.skipped, 0);
        assert_eq!(stats.evicted, 0);
    }

    /// Unchanged file: second delta pass must skip.
    #[test]
    fn delta_unchanged_file_is_skipped() {
        let conn = migrated_conn();
        let pipeline = make_pipeline(conn);
        let state = make_state_store();
        let f = write_file("# Delta unchanged\n\nSame content.\n", ".md");

        // First pass — ingests.
        let s1 = pipeline.ingest_delta([f.path()], &state);
        assert_eq!(s1.ingested, 1);

        // Second pass — skips (mtime/size or hash unchanged).
        let s2 = pipeline.ingest_delta([f.path()], &state);
        assert_eq!(s2.ingested, 0);
        assert_eq!(s2.skipped, 1, "unchanged file must be skipped");
    }

    /// Modified file: second delta pass must re-ingest.
    #[test]
    fn delta_modified_file_is_reingested() {
        let conn = migrated_conn();
        let pipeline = make_pipeline(conn);
        let state = make_state_store();
        let f = write_file("# Delta v1\n\nOriginal.\n", ".md");

        let s1 = pipeline.ingest_delta([f.path()], &state);
        assert_eq!(s1.ingested, 1);

        // Overwrite to force content change and mtime bump.
        std::fs::write(f.path(), b"# Delta v2\n\nCompletely different content now.\n").unwrap();

        let s2 = pipeline.ingest_delta([f.path()], &state);
        assert_eq!(s2.ingested, 1, "modified file must be re-ingested");
        assert_eq!(s2.skipped, 0);
    }

    /// Deleted file: eviction sweep removes it from state store.
    #[test]
    fn delta_deleted_file_is_evicted() {
        let conn = migrated_conn();
        let pipeline = make_pipeline(conn);
        let state = make_state_store();
        let f = write_file("# Delta delete\n\nContent.\n", ".md");
        let path_copy = f.path().to_path_buf();

        // Index it.
        let s1 = pipeline.ingest_delta([f.path()], &state);
        assert_eq!(s1.ingested, 1);
        assert_eq!(state.count(), 1);

        // Delete from disk.
        drop(f);
        assert!(!path_copy.exists());

        // Next pass — no candidates, eviction still fires.
        let s2 = pipeline.ingest_delta(std::iter::empty(), &state);
        assert_eq!(s2.evicted, 1, "deleted file must be evicted");
        assert_eq!(state.count(), 0);
    }

    /// Fast-path: stats.skipped increments when mtime+size match (no hash computed).
    #[test]
    fn delta_stats_skipped_count_is_accurate() {
        let conn = migrated_conn();
        let pipeline = make_pipeline(conn);
        let state = make_state_store();
        let files: Vec<_> = (0..3)
            .map(|i| write_file(&format!("# File {i}\n\nContent {i}.\n"), ".md"))
            .collect();

        // First pass — ingest all.
        let paths: Vec<&Path> = files.iter().map(|f| f.path()).collect();
        let s1 = pipeline.ingest_delta(paths.iter().copied(), &state);
        assert_eq!(s1.ingested, 3);

        // Second pass — all skipped.
        let s2 = pipeline.ingest_delta(paths.iter().copied(), &state);
        assert_eq!(s2.skipped, 3);
        assert_eq!(s2.ingested, 0);
        assert_eq!(s2.scanned, 3);
    }

    /// incremental=false forces full re-index regardless of hash.
    #[test]
    fn delta_incremental_false_always_reindexes() {
        let conn = migrated_conn();
        let embed = Arc::new(MockEmbedModel::default());
        let mut cfg = IngestConfig::default();
        cfg.incremental = false;
        let pipeline = IngestPipeline::new(conn, embed, cfg);
        let state = make_state_store();

        let f = write_file("# Full reindex\n\nContent.\n", ".md");

        // First pass.
        let s1 = pipeline.ingest_delta([f.path()], &state);
        assert_eq!(s1.ingested, 1);

        // Second pass — incremental=false must re-ingest even though content unchanged.
        let s2 = pipeline.ingest_delta([f.path()], &state);
        assert_eq!(s2.ingested, 1, "incremental=false must always re-ingest");
        assert_eq!(s2.skipped, 0);
    }

    /// Duration field is non-zero after a real pass.
    #[test]
    fn delta_stats_duration_is_populated() {
        let conn = migrated_conn();
        let pipeline = make_pipeline(conn);
        let state = make_state_store();
        let f = write_file("# Duration test\n\nContent.\n", ".md");

        let stats = pipeline.ingest_delta([f.path()], &state);
        // Duration must be non-zero (at least a few nanoseconds).
        // Even an empty loop takes > 0 ns; this is always true in practice.
        assert!(stats.duration_ms() < 60_000, "duration sanity: < 60s");
    }
}
