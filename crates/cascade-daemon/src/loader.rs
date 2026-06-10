//! Cascade instruction-file loader with chunk caching.
//!
//! Purpose: Read, parse, and chunk cascade instruction files
//!   (`.cascade/CASCADE.md`, `CLAUDE.md`, memory files, etc.) for RAG indexing.
//!   Every read goes through [`ChunkCache`] so unchanged files are not
//!   re-chunked on every query.
//!
//! Inputs:
//!   - `path: &Path` — absolute path to a cascade instruction file
//!   - `cache: &ChunkCache` — the daemon's shared in-memory chunk cache
//!
//! Outputs:
//!   - `Ok(Vec<Chunk>)` — parsed + chunked content (from cache or fresh read)
//!   - `Err(LoadError)` — I/O or chunking failure
//!
//! Constraints:
//!   - The cache Mutex is NOT held during file I/O. File reading and chunking
//!     happen outside the lock; the lock is acquired only for get/insert.
//!   - mtime is read from `fs::metadata` BEFORE reading the file content. If the
//!     file is modified between the metadata call and the content read, we insert
//!     with a stale mtime — the next read will produce a miss and re-chunk the
//!     file, which is correct.
//!   - Chunking is a simple newline-paragraph split for cascade instruction files
//!     (Markdown text). A full MIME-dispatched ChunkerConfig pipeline is wired
//!     in a later ticket; this loader provides the warm-up layer.
//!
//! SPORT: MASTER-COMPONENTS.md → cascade-daemon loader (T-P4-E04-10)

use std::path::Path;
use std::sync::Arc;
use std::time::SystemTime;

use cascade_types::chunker::{Chunk, ChunkMetadata};

use crate::chunk_cache::ChunkCache;

// ── Error ─────────────────────────────────────────────────────────────────────

/// Errors returned by [`load_cascade_file`].
#[derive(Debug, thiserror::Error)]
pub enum LoadError {
    /// Could not read the file or its metadata.
    #[error("I/O error reading {path}: {source}")]
    Io {
        path: String,
        #[source]
        source: std::io::Error,
    },
}

// ── Loader ────────────────────────────────────────────────────────────────────

/// Load a cascade instruction file, returning parsed chunks.
///
/// Algorithm:
///   1. Read `fs::metadata(path).modified()` for the current mtime.
///   2. Check the chunk cache — return the hit immediately.
///   3. On miss: read the file, chunk by paragraph, insert into the cache.
///
/// The Mutex inside `cache` is NOT held during step 3 (file I/O).
///
/// # Arguments
///
/// * `path` — absolute path to the file to load.
/// * `cache` — the shared [`ChunkCache`] instance.
///
/// # Errors
///
/// Returns [`LoadError::Io`] if the file cannot be read or its mtime cannot
/// be determined.
pub fn load_cascade_file(path: &Path, cache: &Arc<ChunkCache>) -> Result<Vec<Chunk>, LoadError> {
    // Step 1: read mtime — before locking the cache or reading the file.
    let mtime = file_mtime(path)?;

    // Step 2: cache lookup — lock released immediately after.
    if let Some(cached) = cache.get(path, mtime) {
        return Ok(cached);
    }

    // Step 3: cache miss — read and chunk outside the lock.
    let content = std::fs::read_to_string(path).map_err(|e| LoadError::Io {
        path: path.display().to_string(),
        source: e,
    })?;

    let chunks = chunk_text(&content, path, mtime);

    // Populate the cache (lock acquired + released inside insert).
    cache.insert(path, mtime, chunks.clone());

    Ok(chunks)
}

/// Read the last-modified time of `path`.
fn file_mtime(path: &Path) -> Result<SystemTime, LoadError> {
    std::fs::metadata(path)
        .and_then(|m| m.modified())
        .map_err(|e| LoadError::Io {
            path: path.display().to_string(),
            source: e,
        })
}

/// Split the content of a cascade instruction file into paragraph-level chunks.
///
/// WHY paragraph split: cascade files are Markdown; paragraphs (separated by
/// blank lines) are natural semantic units for retrieval. A full Markdown-aware
/// chunker is wired later via the RAG pipeline; this implementation is
/// sufficient for the cache warm-up layer.
///
/// Each chunk carries the source path and byte offsets in its metadata.
fn chunk_text(content: &str, path: &Path, mtime: SystemTime) -> Vec<Chunk> {
    let source_hash = {
        use std::collections::hash_map::DefaultHasher;
        use std::hash::{Hash, Hasher};
        let mut h = DefaultHasher::new();
        path.hash(&mut h);
        mtime
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs()
            .hash(&mut h);
        format!("{:016x}", h.finish())
    };

    let mut chunks = Vec::new();
    let mut byte_offset: usize = 0;

    for para in content.split("\n\n") {
        let trimmed = para.trim();
        if trimmed.is_empty() {
            byte_offset += para.len() + 2; // account for the "\n\n" separator
            continue;
        }
        let start = byte_offset;
        let end = start + para.len();
        let id = format!("{source_hash}-{start}");

        chunks.push(Chunk {
            id,
            text: trimmed.to_owned(),
            metadata: ChunkMetadata {
                source_path: Some(path.to_path_buf()),
                start_byte: start,
                end_byte: end,
                start_line: None,
                end_line: None,
                chunk_index: chunks.len(),
                total_chunks: 0, // back-filled after the loop
                extra: Default::default(),
            },
        });

        byte_offset = end + 2; // +2 for "\n\n"
    }

    // Back-fill total_chunks now that we know the final count.
    let total = chunks.len();
    for c in &mut chunks {
        c.metadata.total_chunks = total;
    }

    chunks
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Arc;
    use tempfile::NamedTempFile;
    use std::io::Write as IoWrite;

    fn make_cache() -> Arc<ChunkCache> {
        Arc::new(ChunkCache::new(256))
    }

    /// Reading the same file twice with the same mtime produces exactly one
    /// filesystem read (verified by checking the cache counters).
    #[test]
    fn loader_cache_hit_on_second_read() {
        let mut file = NamedTempFile::new().unwrap();
        writeln!(file, "# Cascade\n\nFirst paragraph.\n\nSecond paragraph.").unwrap();
        file.flush().unwrap();

        let path = file.path();
        let cache = make_cache();

        // First read — cold miss.
        let chunks1 = load_cascade_file(path, &cache).expect("first read failed");
        assert!(!chunks1.is_empty());

        // Second read — should hit the cache; counters confirm it.
        let chunks2 = load_cascade_file(path, &cache).expect("second read failed");
        assert_eq!(chunks1.len(), chunks2.len());

        let c = cache.counters();
        assert_eq!(c.misses, 1, "expected exactly 1 miss (the cold read)");
        assert_eq!(c.hits, 1, "expected exactly 1 hit (the warm read)");
    }

    /// Changing the file content (and hence the mtime) causes a cache miss.
    #[test]
    fn loader_cache_miss_on_file_change() {
        let mut file = NamedTempFile::new().unwrap();
        writeln!(file, "First version.").unwrap();
        file.flush().unwrap();

        let path = file.path();
        let cache = make_cache();

        let chunks1 = load_cascade_file(path, &cache).expect("first read failed");

        // Overwrite the file — the OS will update the mtime.
        // On macOS, mtime has 1-second granularity in some circumstances; force
        // a distinct mtime by truncating and rewriting with a small sleep.
        // WHY not sleep: avoid slowing tests. Instead we directly set the file
        // modification time using set_file_mtime via filetime.
        // For simplicity we use a loop to wait until mtime changes.
        let mtime_before = file_mtime(path).unwrap();
        // Write new content in a tight loop until mtime advances.
        loop {
            let mut f = std::fs::File::create(path).unwrap();
            writeln!(f, "Second version with more text.").unwrap();
            f.flush().unwrap();
            drop(f);
            let mtime_after = file_mtime(path).unwrap();
            if mtime_after != mtime_before {
                break;
            }
            // Tiny pause so we don't spin at 100% CPU.
            std::thread::sleep(std::time::Duration::from_millis(5));
        }

        let chunks2 = load_cascade_file(path, &cache).expect("second read failed");

        // The content changed, so at minimum the text differs.
        let joined1: String = chunks1.iter().map(|c| c.text.as_str()).collect();
        let joined2: String = chunks2.iter().map(|c| c.text.as_str()).collect();
        assert_ne!(joined1, joined2, "expected different content after file change");

        let c = cache.counters();
        // Two misses (mtime changed), zero hits.
        assert_eq!(c.misses, 2);
        assert_eq!(c.hits, 0);
    }
}
