//! Shared data types for the ingest pipeline.
//!
//! # Contents
//! - [`IngestStats`] — aggregate metrics from one delta scan + ingest pass.
//! - [`IngestConfig`] — tuning parameters for the pipeline.
//! - [`IngestResult`] — outcome of a single file ingest.
//!
//! SPORT: MASTER-CRATES.md → cascade-rag::ingest::types

use std::time::Duration;

use crate::chunk::ChunkerConfig;

/// Batch size for embedding calls (default).
pub(super) const EMBED_BATCH_SIZE: usize = 32;

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
