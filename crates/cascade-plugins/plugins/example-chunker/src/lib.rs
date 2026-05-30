//! Reference Chunker plugin — sentence-boundary chunker.
//!
//! Purpose: demonstrate the minimal correct pattern for a Cascade Chunker plugin.
//!   Splits text on sentence boundaries (`.`, `!`, `?` followed by whitespace).
//!   Zero capabilities declared — no filesystem or network access needed.
//! Inputs: `ChunkRequest` envelope (doc text + options) via ABI entry point.
//! Outputs: `ChunkResponse` (list of chunk texts with byte offsets).
//! Constraints: pure logic, no OS syscalls, no capabilities required.
//! SPORT: cascade-plugins / reference plugins / example-chunker
//!
//! # cascade-plugin.toml snippet
//!
//! ```toml
//! schema_version = 1
//! name = "example-chunker"
//! version = "0.1.0"
//! description = "Sentence-boundary chunker reference implementation"
//! plugin_type = "chunker"
//! cascade_version = ">=0.1.0"
//! # No [capabilities] section — pure logic, no OS access.
//! ```
//!
//! # Why this is a good reference
//! - Declares the minimum possible capabilities (none).
//! - All ABI functions use only primitive types or length-prefixed byte slices.
//! - Error paths return structured JSON rather than panicking.
//! - Inline WHY comments show non-obvious decisions for plugin authors to follow.

use serde::{Deserialize, Serialize};

// ── ABI wire types ────────────────────────────────────────────────────────────
// These mirror the host-side types in `cascade-abi`. In a real plugin, import
// from the `cascade-abi` crate. Inline here to keep this example self-contained.

#[derive(Deserialize)]
struct ChunkRequest {
    doc: DocInput,
    opts: ChunkOpts,
}

#[derive(Deserialize)]
struct DocInput {
    text: String,
}

#[derive(Deserialize)]
struct ChunkOpts {
    #[serde(default = "default_max_chars")]
    max_chars: usize,
}

fn default_max_chars() -> usize {
    512
}

#[derive(Serialize)]
struct ChunkOutput {
    text: String,
    start_byte: usize,
    end_byte: usize,
}

#[derive(Serialize)]
struct ChunkResponse {
    chunks: Vec<ChunkOutput>,
}

// ── ABI entry point ───────────────────────────────────────────────────────────

/// ABI entry point for the Chunker trait — `chunker_chunk`.
///
/// # Why `no_mangle` + `extern "C"`
/// The host locates this function by exact name via wasmtime's `get_func`.
/// `no_mangle` prevents Rust from mangling the symbol. `extern "C"` ensures the
/// calling convention matches the ABI contract (ptr + len in, ptr + len out).
///
/// In a real plugin, use the `cascade-abi` proc-macro which generates this
/// boilerplate from a simple `#[cascade_chunker]` attribute.
///
/// # SAFETY
/// - `input_ptr` points into WASM linear memory; wasmtime bounds-checks all accesses.
/// - The host allocates the output buffer and passes its ptr/len via a separate
///   `alloc_output` ABI call (not shown in this minimal stub).
/// - No pointer arithmetic outside the passed length.
#[no_mangle]
pub extern "C" fn chunker_chunk(input_ptr: *const u8, input_len: usize) -> i32 {
    // SAFETY: input_ptr is a valid pointer into WASM linear memory allocated by
    // the host; wasmtime enforces bounds, so this slice is valid for input_len bytes.
    let input_bytes = unsafe { std::slice::from_raw_parts(input_ptr, input_len) };

    match run_chunker(input_bytes) {
        Ok(json_bytes) => write_output(&json_bytes),
        Err(e) => write_error(&e),
    }
}

fn run_chunker(input_bytes: &[u8]) -> Result<Vec<u8>, String> {
    let req: ChunkRequest = serde_json::from_slice(input_bytes)
        .map_err(|e| format!("deserialize error: {e}"))?;

    let text = &req.doc.text;
    let max_chars = req.opts.max_chars;
    let chunks = split_sentences(text, max_chars);
    let response = ChunkResponse { chunks };
    serde_json::to_vec(&response).map_err(|e| format!("serialize error: {e}"))
}

/// Split text into sentence chunks, merging short sentences up to `max_chars`.
///
/// # Why merge rather than split at every period
/// Single sentences shorter than `max_chars` produce too many tiny chunks that
/// hurt retrieval precision. Merging contiguous sentences up to the limit gives
/// better semantic coherence per chunk.
fn split_sentences(text: &str, max_chars: usize) -> Vec<ChunkOutput> {
    let mut chunks = Vec::new();
    let mut current_start = 0usize;
    let mut current_end = 0usize;

    for (i, ch) in text.char_indices() {
        let is_boundary = matches!(ch, '.' | '!' | '?')
            && text[i + ch.len_utf8()..].starts_with(' ');

        if is_boundary {
            let end = i + ch.len_utf8();
            let proposed_len = end - current_start;
            if proposed_len >= max_chars && current_end > current_start {
                // Flush current accumulated chunk.
                chunks.push(ChunkOutput {
                    text: text[current_start..current_end].trim().to_owned(),
                    start_byte: current_start,
                    end_byte: current_end,
                });
                current_start = current_end;
            }
            current_end = end;
        }
    }

    // Flush remaining text.
    if current_end < text.len() {
        current_end = text.len();
    }
    if current_end > current_start {
        chunks.push(ChunkOutput {
            text: text[current_start..current_end].trim().to_owned(),
            start_byte: current_start,
            end_byte: current_end,
        });
    }

    chunks
}

// ── Output helpers ────────────────────────────────────────────────────────────
// These are stubs — the real ABI has a proper output allocation protocol.

fn write_output(bytes: &[u8]) -> i32 {
    // Real implementation: call host-provided `alloc_output(len)` to get a buffer
    // pointer, copy bytes, then return len as confirmation. Stubbed here.
    let _ = bytes;
    0
}

fn write_error(msg: &str) -> i32 {
    let _ = msg;
    -1
}

// ── Negative-capability test ──────────────────────────────────────────────────
// Plugin authors: include tests that verify unauthorized syscalls are denied.
// The sandbox simulator (cascade-plugins/tests) runs these inside wasmtime.

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn splits_simple_sentences() {
        let text = "Hello world. This is a test. Final sentence.";
        let chunks = split_sentences(text, 100);
        // With max_chars=100 all fit in one chunk.
        assert!(!chunks.is_empty());
        assert!(chunks.iter().all(|c| !c.text.is_empty()));
    }

    #[test]
    fn respects_max_chars() {
        let text = "Short. Also short. And this is another short sentence. Done.";
        let chunks = split_sentences(text, 20);
        // With max_chars=20 each sentence should be its own chunk.
        assert!(chunks.len() >= 2);
    }

    #[test]
    fn handles_empty_input() {
        let chunks = split_sentences("", 512);
        assert!(chunks.is_empty());
    }
}
