//! RAPTOR clustering — stub for rag-14.
//!
//! # TODO(rag-14)
//!
//! RAPTOR (Recursive Abstractive Processing Tree for Organized Retrieval)
//! requires:
//! 1. A cheap LLM (via [`HydeLlm`]) to summarise leaf-level symbol clusters.
//! 2. A clustering algorithm (e.g. k-means or UMAP + HDBSCAN) to group
//!    semantically related symbols before summarisation.
//! 3. Embedding vectors for each symbol chunk (currently computed only when
//!    the `vec` feature is compiled in).
//!
//! These dependencies are too heavy for the rag-14 core pass. This file is a
//! documented stub so the public API surface (`study_project`) does not need to
//! change when the full implementation lands.
//!
//! SPORT: MASTER-CRATES.md → cascade-rag::study::raptor

/// Placeholder for the RAPTOR cluster output.
///
/// Will hold a list of `(cluster_label, summary_md)` pairs once implemented.
#[derive(Debug, Clone, Default)]
pub struct RaptorTree {
    /// Placeholder — will hold cluster summaries.
    _clusters: Vec<(String, String)>,
}

impl RaptorTree {
    /// Returns `true` when no clusters have been built (stub or empty input).
    pub fn is_empty(&self) -> bool {
        self._clusters.is_empty()
    }
}

/// Build a RAPTOR summarisation tree from symbol chunks.
///
/// # TODO(rag-14)
///
/// Current implementation is a no-op stub. Returns an empty [`RaptorTree`].
/// Full implementation requires: cheap LLM injection + embedding vectors +
/// clustering library.
pub fn build_raptor_tree(
    _symbol_chunks: &[(&str, &str)], // (symbol_name, chunk_text)
    _llm: Option<&dyn crate::search::HydeLlm>,
) -> RaptorTree {
    // TODO(rag-14): implement recursive summarisation with LLM + clustering.
    RaptorTree::default()
}
