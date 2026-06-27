//! Chunker selection and file utility helpers for the ingest pipeline.
//!
//! # Contents
//! - [`chunker_for_path`] — maps file extension to the right [`Chunker`] impl.
//! - [`hex_blake3`] — BLAKE3 content hash (canonical; see `cascade_db::content_hash`).
//! - [`file_mtime`] — extract mtime from a path.
//!
//! SPORT: MASTER-CRATES.md → cascade-rag::ingest::chunker

use std::path::Path;
use std::time::UNIX_EPOCH;

use crate::chunk::hierarchical::HierarchicalChunker;
use crate::chunk::markdown::MarkdownChunker;
use crate::chunk::semantic::SemanticChunker;
use crate::chunk::{Chunker, ChunkerConfig};

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

/// Compute a hex-encoded BLAKE3 digest of `bytes`.
///
/// Delegates to `cascade_db::content_hash` — the canonical hash function for
/// all dedup, delta-detection, and cache keys in Cascade (locked decision #2).
pub(super) fn hex_blake3(bytes: &[u8]) -> String {
    cascade_db::content_hash(bytes)
}

/// Extract the file's mtime as Unix seconds, or `None` if unavailable.
pub(super) fn file_mtime(path: &Path) -> Option<i64> {
    std::fs::metadata(path)
        .ok()
        .and_then(|m| m.modified().ok())
        .and_then(|t| t.duration_since(UNIX_EPOCH).ok())
        .map(|d| d.as_secs() as i64)
}
