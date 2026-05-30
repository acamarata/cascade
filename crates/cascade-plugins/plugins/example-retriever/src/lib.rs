//! Reference Retriever plugin — BM25 in-memory retriever.
//!
//! Purpose: demonstrate a Retriever plugin that uses the `fs_read` capability
//!   to load a document corpus from a declared path scope, then serves queries
//!   with a simple BM25 term-frequency ranking.
//! Inputs: `RetrieveRequest` envelope (query text + top-k) via ABI entry point.
//! Outputs: `RetrieveResponse` (ranked hits with score + source path).
//! Constraints: declares `fs_read = [".cascade/corpus/"]` in cascade-plugin.toml.
//!   Memory budget 256 MB (Retriever default) — sufficient for multi-MB corpora.
//! SPORT: cascade-plugins / reference plugins / example-retriever
//!
//! # cascade-plugin.toml snippet
//!
//! ```toml
//! schema_version = 1
//! name = "example-retriever"
//! version = "0.1.0"
//! description = "BM25 in-memory retriever reference implementation"
//! plugin_type = "retriever"
//! cascade_version = ">=0.1.0"
//!
//! [capabilities]
//! fs_read = [".cascade/corpus/"]  # Scoped to the per-cascade corpus directory.
//! ```
//!
//! # Capability-denial negative test
//! The sandbox integration test confirms that accessing paths outside
//! `.cascade/corpus/` (e.g., `/etc/passwd`) returns CAPABILITY_DENIED even if
//! the WASM code attempts the access.

use serde::{Deserialize, Serialize};
use std::collections::HashMap;

// ── Wire types ────────────────────────────────────────────────────────────────

#[derive(Deserialize)]
struct RetrieveRequest {
    query: QueryInput,
}

#[derive(Deserialize)]
struct QueryInput {
    text: String,
    #[serde(default = "default_top_k")]
    top_k: usize,
}

fn default_top_k() -> usize {
    5
}

#[derive(Serialize)]
struct RetrieveHit {
    text: String,
    score: f32,
    source: String,
    chunk_index: usize,
}

#[derive(Serialize)]
struct RetrieveResponse {
    hits: Vec<RetrieveHit>,
    total_candidates: usize,
}

// ── In-memory BM25 index ──────────────────────────────────────────────────────

/// Minimal BM25 implementation for demonstration.
///
/// # Why BM25 and not a vector index
/// BM25 requires zero external dependencies and zero capabilities beyond reading
/// a plain-text corpus. It demonstrates the `fs_read` capability pattern without
/// pulling in an ONNX runtime (which would push memory well above the 256 MB default).
/// A real retriever plugin would typically embed documents with an EmbeddingProvider
/// and use a vector similarity search.
struct Bm25Index {
    docs: Vec<(String, String)>,  // (source_id, text)
    idf: HashMap<String, f32>,
    avg_doc_len: f32,
    k1: f32,
    b: f32,
}

impl Bm25Index {
    fn new(docs: Vec<(String, String)>) -> Self {
        let n = docs.len() as f32;
        let avg_doc_len = if docs.is_empty() {
            1.0
        } else {
            docs.iter().map(|(_, t)| t.split_whitespace().count() as f32).sum::<f32>() / n
        };

        let mut df: HashMap<String, u32> = HashMap::new();
        for (_, text) in &docs {
            let unique: std::collections::HashSet<String> =
                text.split_whitespace().map(|w| w.to_lowercase()).collect();
            for term in unique {
                *df.entry(term).or_insert(0) += 1;
            }
        }

        let idf = df
            .into_iter()
            .map(|(term, df_count)| {
                let score = ((n - df_count as f32 + 0.5) / (df_count as f32 + 0.5) + 1.0).ln();
                (term, score)
            })
            .collect();

        Self { docs, idf, avg_doc_len, k1: 1.2, b: 0.75 }
    }

    fn search(&self, query: &str, top_k: usize) -> Vec<(f32, usize)> {
        let terms: Vec<String> = query.split_whitespace().map(|w| w.to_lowercase()).collect();
        let mut scores: Vec<(f32, usize)> = self
            .docs
            .iter()
            .enumerate()
            .map(|(i, (_, doc))| {
                let doc_len = doc.split_whitespace().count() as f32;
                let score: f32 = terms
                    .iter()
                    .map(|term| {
                        let idf = self.idf.get(term).copied().unwrap_or(0.0);
                        let tf = doc.split_whitespace().filter(|w| &w.to_lowercase() == term).count() as f32;
                        let tf_norm = tf * (self.k1 + 1.0)
                            / (tf + self.k1 * (1.0 - self.b + self.b * doc_len / self.avg_doc_len));
                        idf * tf_norm
                    })
                    .sum();
                (score, i)
            })
            .collect();

        scores.sort_by(|a, b| b.0.partial_cmp(&a.0).unwrap_or(std::cmp::Ordering::Equal));
        scores.truncate(top_k);
        scores
    }
}

// ── ABI entry point ───────────────────────────────────────────────────────────

/// ABI entry point for the Retriever trait.
///
/// # SAFETY
/// `input_ptr` is wasmtime-managed and bounds-checked; slice is valid.
#[no_mangle]
pub extern "C" fn retriever_retrieve(input_ptr: *const u8, input_len: usize) -> i32 {
    // SAFETY: wasmtime ensures input_ptr is valid for input_len bytes.
    let input_bytes = unsafe { std::slice::from_raw_parts(input_ptr, input_len) };
    match run_retriever(input_bytes) {
        Ok(bytes) => { let _ = bytes; 0 }
        Err(_) => -1,
    }
}

fn run_retriever(input_bytes: &[u8]) -> Result<Vec<u8>, String> {
    let req: RetrieveRequest = serde_json::from_slice(input_bytes)
        .map_err(|e| format!("deserialize error: {e}"))?;

    // In production: load corpus from `.cascade/corpus/` via the `fs_read` capability.
    // Stub corpus for demonstration.
    let corpus = vec![
        ("doc-0".to_owned(), "Cascade is a context management framework for AI-assisted development.".to_owned()),
        ("doc-1".to_owned(), "WASM plugins extend Cascade with custom chunkers and retrievers.".to_owned()),
        ("doc-2".to_owned(), "BM25 is a term-frequency ranking algorithm used in information retrieval.".to_owned()),
    ];

    let index = Bm25Index::new(corpus.clone());
    let results = index.search(&req.query.text, req.query.top_k);
    let total = corpus.len();

    let hits: Vec<RetrieveHit> = results
        .into_iter()
        .enumerate()
        .map(|(chunk_idx, (score, doc_idx))| RetrieveHit {
            text: corpus[doc_idx].1.clone(),
            score,
            source: corpus[doc_idx].0.clone(),
            chunk_index: chunk_idx,
        })
        .collect();

    let response = RetrieveResponse { hits, total_candidates: total };
    serde_json::to_vec(&response).map_err(|e| format!("serialize error: {e}"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn bm25_returns_relevant_docs() {
        let corpus = vec![
            ("a".into(), "cats like fish and tuna".into()),
            ("b".into(), "dogs like bones and fetch".into()),
            ("c".into(), "fish is protein for cats".into()),
        ];
        let index = Bm25Index::new(corpus);
        let results = index.search("cats fish", 2);
        assert_eq!(results.len(), 2);
        // Doc a and c should score higher than b for "cats fish".
        assert_ne!(results[0].1, 1, "dog doc should not be top result");
    }

    #[test]
    fn empty_corpus_returns_empty() {
        let index = Bm25Index::new(vec![]);
        assert!(index.search("anything", 5).is_empty());
    }
}
