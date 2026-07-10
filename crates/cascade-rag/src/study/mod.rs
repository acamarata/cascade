//! Codebase study pipeline — entry point for project-level understanding.
//!
//! # Purpose
//!
//! [`study_project`] is the single public entry point. It:
//! 1. Detects the tech stack from marker files (synchronous, pure).
//! 2. Builds a code-graph adjacency table from a set of [`Chunk`]s.
//! 3. Collects all symbols in the project.
//! 4. Produces a ≤2 KB markdown overview (template-based, cached by content hash).
//! 5. Builds a RAPTOR summarisation tree (directory-clustered, LLM-optional).
//! 6. Extracts a Mermaid architecture diagram from the code graph.
//!
//! # Inputs
//!
//! - `dir`: project root directory path.
//! - `chunks`: symbol chunks extracted by [`CodeChunker`] (may be empty).
//! - `conn`: SQLite connection used for the graph table + overview cache.
//! - `project_name`: human-readable name used as the markdown heading.
//! - `llm`: optional [`HydeLlm`] for future enrichment (currently ignored).
//!
//! # Outputs
//!
//! [`ProjectStudy`] with `overview_md`, `graph` connection (the caller's `conn`),
//! and `stack`.
//!
//! # Ticket
//!
//! rag-14
//!
//! SPORT: MASTER-CRATES.md → cascade-rag::study

pub mod arch;
pub mod code_graph_query;
pub mod graph;
pub mod overview;
pub mod raptor;
pub mod stack;

use std::path::Path;
use std::sync::Arc;

use rusqlite::Connection;

use crate::chunk::Chunk;
use crate::search::HydeLlm;
use cascade_types::error::Result;

pub use arch::{extract_arch, ArchDiagram};
pub use code_graph_query::{
    classify_structural, code_graph_query, GraphQueryResult, GraphRaw, StructuralIntent,
};
pub use graph::{build_graph, callers_of, symbols_in};
pub use overview::{generate as generate_overview, OverviewResult};
pub use raptor::{build_raptor_tree, RaptorTree};
pub use stack::{detect as detect_stack, TechStack};

// ── Public types ──────────────────────────────────────────────────────────────

/// The result of studying a project directory.
///
/// Returned by [`study_project`].
#[derive(Debug)]
pub struct ProjectStudy {
    /// ≤2 KB markdown overview of the project.
    pub overview_md: String,
    /// Whether the overview was served from cache.
    pub overview_from_cache: bool,
    /// All `(symbol, kind)` pairs found across all chunks.
    pub symbols: Vec<(String, String)>,
    /// Detected tech stack.
    pub stack: TechStack,
    /// RAPTOR summarisation tree — clustered by directory, template or LLM summaries.
    pub raptor: RaptorTree,
    /// Mermaid architecture diagram extracted from the code graph.
    pub arch: ArchDiagram,
}

// ── Entry point ───────────────────────────────────────────────────────────────

/// Run the full codebase study pipeline for `dir`.
///
/// # Steps
///
/// 1. Detect tech stack from `dir` marker files.
/// 2. Build the `code_graph` table from `chunks` into `conn`.
/// 3. Collect all symbols from `conn`.
/// 4. Generate (or retrieve) a markdown overview.
/// 5. Build RAPTOR tree (directory clusters + summaries).
/// 6. Extract Mermaid architecture diagram from the code graph.
/// 7. Return [`ProjectStudy`].
///
/// # Inputs
///
/// - `dir`: project root (used for stack detection).
/// - `chunks`: code chunks with `symbol_name` metadata (from [`CodeChunker`]).
/// - `conn`: live SQLite connection for graph + cache tables.
/// - `project_name`: heading used in the markdown overview.
/// - `llm`: optional LLM (currently ignored; hook for future enrichment).
///
/// # Errors
///
/// Any SQLite or I/O error is returned as [`cascade_types::error::CascadeError`].
pub async fn study_project(
    dir: &Path,
    chunks: &[Chunk],
    conn: &Connection,
    project_name: &str,
    llm: Option<Arc<dyn HydeLlm>>,
) -> Result<ProjectStudy> {
    // 1. Stack detection (pure, sync).
    let stack = detect_stack(dir);

    // 2. Build code-graph adjacency table.
    build_graph(conn, chunks)?;

    // 3. Collect all symbols (all files).
    let symbols = collect_all_symbols(conn)?;

    // 4. Overview (async, cached).
    let overview_result =
        generate_overview(conn, &stack, &symbols, project_name, llm.clone()).await?;

    // 5. RAPTOR tree — directory-clustered, template or LLM summaries, cached.
    let symbol_chunks: Vec<(&str, &str)> = symbols
        .iter()
        .map(|(sym, _kind)| (sym.as_str(), ""))
        .collect();
    let llm_ref: Option<&dyn HydeLlm> = llm.as_ref().map(|a| a.as_ref());
    let raptor = build_raptor_tree(&symbol_chunks, llm_ref, conn).await?;

    // 6. Architecture diagram from code graph.
    let arch = extract_arch(conn, llm_ref)?;

    Ok(ProjectStudy {
        overview_md: overview_result.markdown,
        overview_from_cache: overview_result.from_cache,
        symbols,
        stack,
        raptor,
        arch,
    })
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Collect all `(symbol, kind)` pairs from `code_graph`, deduplicated,
/// ordered by insertion order.
fn collect_all_symbols(conn: &Connection) -> Result<Vec<(String, String)>> {
    use cascade_types::error::CascadeError;

    // Ensure table exists before querying (build_graph creates it, but if
    // chunks is empty, build_graph still creates the schema).
    let mut stmt = conn
        .prepare("SELECT symbol, kind FROM code_graph ORDER BY id")
        .map_err(|e| CascadeError::Other(format!("collect_all_symbols: {e}")))?;

    let rows = stmt
        .query_map([], |row| {
            Ok((row.get::<_, String>(0)?, row.get::<_, String>(1)?))
        })
        .map_err(|e| CascadeError::Other(format!("collect_all_symbols query: {e}")))?;

    let mut seen = std::collections::HashSet::new();
    let mut result = Vec::new();
    for r in rows {
        let (sym, kind) = r.map_err(|e| CascadeError::Other(e.to_string()))?;
        if seen.insert(sym.clone()) {
            result.push((sym, kind));
        }
    }
    Ok(result)
}

// ── Integration tests ─────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;
    use std::path::PathBuf;
    use tempfile::TempDir;

    fn make_chunk(symbol: &str, kind: &str, file: &str, text: &str) -> Chunk {
        let mut meta = HashMap::new();
        meta.insert("symbol_name".into(), symbol.into());
        meta.insert("kind".into(), kind.into());
        Chunk {
            chunk_id: None,
            source_path: PathBuf::from(file),
            chunk_index: 0,
            text: text.into(),
            char_start: 0,
            char_end: text.len(),
            line_start: 1,
            line_end: 1,
            parent_chunk_id: None,
            heading_path: None,
            metadata: meta,
        }
    }

    fn rust_dir() -> TempDir {
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join("Cargo.toml"), "[package]").unwrap();
        dir
    }

    #[tokio::test]
    async fn study_project_returns_expected_fields() {
        let dir = rust_dir();
        let conn = Connection::open_in_memory().unwrap();
        let chunks = vec![
            make_chunk("foo", "fn", "src/lib.rs", "fn foo() {}"),
            make_chunk("bar", "fn", "src/lib.rs", "fn bar() { foo() }"),
        ];

        let study = study_project(dir.path(), &chunks, &conn, "myproject", None)
            .await
            .unwrap();

        assert!(study.overview_md.contains("# myproject"));
        assert!(study.overview_md.contains("rust"));
        assert_eq!(study.stack.language.as_deref(), Some("rust"));
        assert!(study.symbols.iter().any(|(s, _)| s == "foo"));
        assert!(study.symbols.iter().any(|(s, _)| s == "bar"));
        assert!(!study.overview_from_cache);
        // RAPTOR and arch are now real implementations — both non-empty for non-empty chunks.
        assert!(!study.raptor.is_empty(), "raptor tree must be populated");
        assert!(!study.arch.is_empty(), "arch diagram must be populated");
    }

    #[tokio::test]
    async fn second_study_call_is_cache_hit() {
        let dir = rust_dir();
        let conn = Connection::open_in_memory().unwrap();
        let chunks = vec![make_chunk("foo", "fn", "src/lib.rs", "fn foo() {}")];

        let first = study_project(dir.path(), &chunks, &conn, "proj", None)
            .await
            .unwrap();
        // Rebuild with same conn + same chunks (graph rows duplicated, but
        // symbols are deduplicated and cache key is the same).
        let conn2 = Connection::open_in_memory().unwrap();
        // Use a fresh conn for a clean graph, same symbols.
        let second = study_project(dir.path(), &chunks, &conn2, "proj", None)
            .await
            .unwrap();

        assert!(!first.overview_from_cache);
        // second has its own fresh in-memory conn — will also be a miss (different conn).
        // To test true cache hit, call study_project again on the SAME conn.
        let conn3 = conn; // moved conn (first call already used it)
        let third = study_project(dir.path(), &chunks, &conn3, "proj", None)
            .await
            .unwrap();
        assert!(third.overview_from_cache);
        assert_eq!(first.overview_md, third.overview_md);

        let _ = second; // silence unused warning
    }

    #[tokio::test]
    async fn empty_chunks_still_produces_overview() {
        let dir = rust_dir();
        let conn = Connection::open_in_memory().unwrap();

        let study = study_project(dir.path(), &[], &conn, "emptyproj", None)
            .await
            .unwrap();

        assert!(study.overview_md.contains("# emptyproj"));
        assert!(study.symbols.is_empty());
    }
}
