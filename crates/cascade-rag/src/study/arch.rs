//! Architecture extraction — stub for rag-14.
//!
//! # TODO(rag-14)
//!
//! Architecture extraction infers module / layer boundaries from the code graph
//! and produces a structured diagram (mermaid or ASCII). Requires:
//! 1. Cross-file call-graph analysis (callers_of across all files).
//! 2. Cluster assignment (which symbols form a module boundary).
//! 3. Optional LLM enrichment for layer labels.
//!
//! The rag-14 core pass includes only the symbol adjacency table (graph.rs)
//! which is sufficient prerequisite data. This stub reserves the public surface.
//!
//! SPORT: MASTER-CRATES.md → cascade-rag::study::arch

/// Placeholder for an extracted architecture diagram.
#[derive(Debug, Clone, Default)]
pub struct ArchDiagram {
    /// Mermaid diagram source, or empty when not yet generated.
    pub mermaid_src: String,
}

impl ArchDiagram {
    /// Returns `true` when no diagram has been generated (stub).
    pub fn is_empty(&self) -> bool {
        self.mermaid_src.is_empty()
    }
}

/// Extract an architecture diagram from the code graph.
///
/// # TODO(rag-14)
///
/// Current implementation is a no-op stub. Returns an empty [`ArchDiagram`].
/// Full implementation requires cross-file call-graph traversal and LLM
/// label generation.
pub fn extract_arch(
    _conn: &rusqlite::Connection,
    _llm: Option<&dyn crate::search::HydeLlm>,
) -> ArchDiagram {
    // TODO(rag-14): traverse code_graph, group by module, emit mermaid.
    ArchDiagram::default()
}
