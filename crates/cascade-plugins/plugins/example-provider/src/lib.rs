//! Reference EmbeddingProvider plugin — mock returning deterministic test output.
//!
//! Purpose: demonstrate an EmbeddingProvider plugin. Returns stable, deterministic
//!   embeddings (hash-derived float vectors) so integration tests produce
//!   reproducible results without a real embedding model or network access.
//! Inputs: `EmbedRequest` envelope (text strings + options) via ABI entry point.
//! Outputs: `EmbedResponse` (one float vector per input string).
//! Constraints: zero capabilities declared — no filesystem or network needed for
//!   the deterministic hash-based implementation. A real embedding provider would
//!   declare `net_outbound` (API call) or `fs_read` (local ONNX model).
//! SPORT: cascade-plugins / reference plugins / example-provider
//!
//! # cascade-plugin.toml snippet
//!
//! ```toml
//! schema_version = 1
//! name = "example-provider"
//! version = "0.1.0"
//! description = "Deterministic test EmbeddingProvider — stable hashed embeddings"
//! plugin_type = "embedding_provider"
//! cascade_version = ">=0.1.0"
//! # No [capabilities] — hash-based, no I/O.
//! ```

use serde::{Deserialize, Serialize};

// ── Wire types ────────────────────────────────────────────────────────────────

#[derive(Deserialize)]
struct EmbedRequest {
    texts: Vec<String>,
    opts: EmbedOpts,
}

#[derive(Deserialize)]
struct EmbedOpts {
    #[serde(default = "default_dim")]
    dim: usize,
}

fn default_dim() -> usize {
    384
}

#[derive(Serialize)]
struct EmbedResponse {
    embeddings: Vec<Vec<f32>>,
    dim: usize,
}

// ── ABI entry point ───────────────────────────────────────────────────────────

/// ABI entry point for EmbeddingProvider trait.
///
/// # SAFETY
/// `input_ptr` is wasmtime-managed and bounds-checked.
#[no_mangle]
pub extern "C" fn provider_embed(input_ptr: *const u8, input_len: usize) -> i32 {
    // SAFETY: wasmtime ensures this is valid for input_len bytes.
    let input_bytes = unsafe { std::slice::from_raw_parts(input_ptr, input_len) };
    match run_provider(input_bytes) {
        Ok(bytes) => { let _ = bytes; 0 }
        Err(_) => -1,
    }
}

fn run_provider(input_bytes: &[u8]) -> Result<Vec<u8>, String> {
    let req: EmbedRequest = serde_json::from_slice(input_bytes)
        .map_err(|e| format!("deserialize error: {e}"))?;

    let dim = req.opts.dim;
    let embeddings: Vec<Vec<f32>> = req.texts
        .iter()
        .map(|text| hash_embed(text, dim))
        .collect();

    let response = EmbedResponse { embeddings, dim };
    serde_json::to_vec(&response).map_err(|e| format!("serialize error: {e}"))
}

/// Generate a deterministic unit-length embedding from a string via FNV-inspired hashing.
///
/// # Why hash-based embeddings for tests
/// Real embedding models require either a network call or a local model file.
/// Both require capabilities and introduce flakiness (network latency, model file
/// presence). Hash-based embeddings are deterministic, zero-dependency, and
/// fast enough for all integration test scenarios that only need stable vectors.
///
/// For semantic similarity tests, use a fixture corpus where the ground-truth
/// rankings are known independent of the embedding method.
fn hash_embed(text: &str, dim: usize) -> Vec<f32> {
    let mut vec: Vec<f32> = Vec::with_capacity(dim);
    let bytes = text.as_bytes();
    let seed: u64 = 0xcbf2_9ce4_8422_2325; // FNV offset basis

    for i in 0..dim {
        let mut h = seed;
        for (j, &byte) in bytes.iter().enumerate() {
            // Mix position, dimension index, and byte value for spread.
            h ^= (byte as u64).wrapping_mul(j as u64 + 1);
            h = h.wrapping_mul(0x0000_0100_000001B3); // FNV prime
            h ^= i as u64;
        }
        // Map to [-1, 1] range.
        vec.push((h as f32 / u64::MAX as f32) * 2.0 - 1.0);
    }

    // Normalize to unit length for cosine similarity.
    let norm: f32 = vec.iter().map(|x| x * x).sum::<f32>().sqrt().max(1e-9);
    vec.iter_mut().for_each(|x| *x /= norm);
    vec
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn deterministic_same_input() {
        let v1 = hash_embed("hello world", 16);
        let v2 = hash_embed("hello world", 16);
        assert_eq!(v1, v2);
    }

    #[test]
    fn different_texts_differ() {
        let v1 = hash_embed("cat", 16);
        let v2 = hash_embed("dog", 16);
        assert_ne!(v1, v2);
    }

    #[test]
    fn unit_length() {
        let v = hash_embed("test", 32);
        let norm: f32 = v.iter().map(|x| x * x).sum::<f32>().sqrt();
        assert!((norm - 1.0).abs() < 1e-5, "norm should be ~1.0, got {norm}");
    }

    #[test]
    fn respects_dim() {
        let v = hash_embed("abc", 64);
        assert_eq!(v.len(), 64);
    }
}
