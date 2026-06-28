//! Architecture diagram extraction — rag-14 implementation.
//!
//! # Purpose
//!
//! Produces a Mermaid `graph TD` diagram from the `code_graph` SQLite table
//! built by [`graph::build_graph`].  Two passes:
//!
//! 1. **Module detection**: group all symbols by their inferred module prefix
//!    (same logic as RAPTOR clustering — first `::` segment, or `(root)`).
//! 2. **Edge detection**: for each pair of distinct modules, check whether any
//!    symbol in module A is referenced in the chunk text of any symbol in module B
//!    (or vice-versa); add a directed edge A → B.
//!
//! The result is a non-empty Mermaid diagram as long as the graph table contains
//! at least one row.  When the graph is empty, a minimal sentinel diagram is
//! returned (single `(no symbols detected)` node).
//!
//! Optional LLM enrichment (module label prettification) is applied when an LLM
//! is injected; otherwise raw module names are used.
//!
//! # Constraints
//!
//! - Top-N modules only: capped at [`MAX_NODES`] to keep diagrams readable.
//! - Edge density cap: at most [`MAX_EDGES`] edges emitted.
//! - No tree-sitter at study time — all data comes from the SQLite `code_graph` table.
//!
//! # Ticket
//!
//! rag-14
//!
//! SPORT: MASTER-CRATES.md → cascade-rag::study::arch

use rusqlite::{params, Connection};

use cascade_types::error::{CascadeError, Result};

use crate::search::HydeLlm;

// ── Constants ─────────────────────────────────────────────────────────────────

/// Maximum number of module nodes emitted in the Mermaid diagram.
const MAX_NODES: usize = 24;
/// Maximum number of directed edges emitted.
const MAX_EDGES: usize = 48;

// ── Public types ──────────────────────────────────────────────────────────────

/// An extracted architecture diagram.
#[derive(Debug, Clone, Default)]
pub struct ArchDiagram {
    /// Mermaid diagram source, or empty when not yet generated.
    pub mermaid_src: String,
}

impl ArchDiagram {
    /// Returns `true` when no diagram has been generated.
    pub fn is_empty(&self) -> bool {
        self.mermaid_src.is_empty()
    }
}

// ── Public API ────────────────────────────────────────────────────────────────

/// Extract an architecture diagram from the `code_graph` table.
///
/// # Inputs
///
/// - `conn`: SQLite connection with a populated `code_graph` table (built by
///   [`graph::build_graph`]).  An empty or missing table produces a minimal
///   sentinel diagram.
/// - `llm`: optional LLM for label enrichment (currently reserved; ignored in
///   the template path so this function stays network-free in tests).
///
/// # Outputs
///
/// [`ArchDiagram`] whose `mermaid_src` is always non-empty for a real project.
///
/// # Errors
///
/// SQLite errors are wrapped in [`CascadeError::Other`].
pub fn extract_arch(
    conn: &Connection,
    _llm: Option<&dyn HydeLlm>,
) -> Result<ArchDiagram> {
    // Fetch all (symbol, file_path, chunk_text) rows.
    let rows = fetch_graph_rows(conn)?;

    if rows.is_empty() {
        return Ok(ArchDiagram {
            mermaid_src: "graph TD\n    A[(no symbols detected)]\n".to_string(),
        });
    }

    // Group symbols by module prefix.
    let modules = group_by_module(&rows);

    // Take top-N modules by symbol count (most populated first).
    let mut sorted_modules: Vec<(&str, Vec<usize>)> = modules
        .iter()
        .map(|(label, indices)| (label.as_str(), indices.clone()))
        .collect();
    sorted_modules.sort_by(|a, b| b.1.len().cmp(&a.1.len()));
    sorted_modules.truncate(MAX_NODES);

    // Build a stable node list.
    let node_labels: Vec<&str> = sorted_modules.iter().map(|(l, _)| *l).collect();

    // Detect edges: module A → module B if any symbol in A's chunk_text mentions
    // any symbol from module B.
    let edges = detect_edges(&rows, &sorted_modules, &node_labels);

    // Render Mermaid.
    let mermaid = render_mermaid(&node_labels, &edges);

    Ok(ArchDiagram {
        mermaid_src: mermaid,
    })
}

// ── Data fetching ─────────────────────────────────────────────────────────────

struct GraphRow {
    symbol: String,
    chunk_text: String,
}

fn fetch_graph_rows(conn: &Connection) -> Result<Vec<GraphRow>> {
    // Return empty vec gracefully if the table doesn't exist yet.
    let table_exists: bool = conn
        .query_row(
            "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='code_graph'",
            [],
            |row| row.get::<_, i64>(0),
        )
        .unwrap_or(0)
        > 0;

    if !table_exists {
        return Ok(Vec::new());
    }

    let mut stmt = conn
        .prepare("SELECT symbol, chunk_text FROM code_graph ORDER BY id")
        .map_err(|e| CascadeError::Other(format!("arch fetch: {e}")))?;

    let rows = stmt
        .query_map(params![], |row| {
            Ok(GraphRow {
                symbol: row.get::<_, String>(0)?,
                chunk_text: row.get::<_, String>(1)?,
            })
        })
        .map_err(|e| CascadeError::Other(format!("arch query: {e}")))?;

    let mut result = Vec::new();
    for r in rows {
        result.push(r.map_err(|e| CascadeError::Other(e.to_string()))?);
    }
    Ok(result)
}

// ── Module grouping ───────────────────────────────────────────────────────────

/// Returns an ordered map of module_label → Vec<row_index>.
fn group_by_module(rows: &[GraphRow]) -> Vec<(String, Vec<usize>)> {
    let mut map: std::collections::HashMap<String, Vec<usize>> =
        std::collections::HashMap::new();
    let mut order: Vec<String> = Vec::new();

    for (i, row) in rows.iter().enumerate() {
        let label = module_label(&row.symbol);
        let entry = map.entry(label.clone()).or_insert_with(|| {
            order.push(label.clone());
            Vec::new()
        });
        entry.push(i);
    }

    order.into_iter().map(|k| {
        let v = map.remove(&k).unwrap_or_default();
        (k, v)
    }).collect()
}

fn module_label(sym: &str) -> String {
    if let Some(idx) = sym.find("::") {
        return sym[..idx].to_string();
    }
    if let Some(idx) = sym.find('/') {
        return sym[..idx].to_string();
    }
    "(root)".to_string()
}

// ── Edge detection ────────────────────────────────────────────────────────────

/// Returns `(from_idx, to_idx)` pairs in `node_labels`.
fn detect_edges(
    rows: &[GraphRow],
    modules: &[(&str, Vec<usize>)],
    node_labels: &[&str],
) -> Vec<(usize, usize)> {
    let mut edges: Vec<(usize, usize)> = Vec::new();
    let n = node_labels.len();

    // For each module A, collect the symbols in A, then scan module B's chunk
    // texts to see if they mention any symbol from A.
    for a_idx in 0..n {
        let a_syms: Vec<&str> = modules[a_idx]
            .1
            .iter()
            .map(|&ri| rows[ri].symbol.as_str())
            .collect();

        for b_idx in 0..n {
            if a_idx == b_idx {
                continue;
            }
            // Check if any chunk in B mentions any symbol from A.
            let referenced = modules[b_idx].1.iter().any(|&ri| {
                let text = &rows[ri].chunk_text;
                a_syms.iter().any(|sym| text.contains(sym))
            });
            if referenced {
                let edge = (a_idx, b_idx);
                if !edges.contains(&edge) {
                    edges.push(edge);
                    if edges.len() >= MAX_EDGES {
                        return edges;
                    }
                }
            }
        }
    }

    edges
}

// ── Mermaid rendering ─────────────────────────────────────────────────────────

fn render_mermaid(node_labels: &[&str], edges: &[(usize, usize)]) -> String {
    let mut out = String::from("graph TD\n");

    // Emit node declarations (sanitise labels for Mermaid IDs).
    for (i, label) in node_labels.iter().enumerate() {
        let id = mermaid_id(i);
        let display = mermaid_escape(label);
        out.push_str(&format!("    {}[\"{}\"]\n", id, display));
    }

    // Emit edges.
    for &(from, to) in edges {
        out.push_str(&format!(
            "    {} --> {}\n",
            mermaid_id(from),
            mermaid_id(to)
        ));
    }

    out
}

/// Stable alphanumeric ID for a node index (M0, M1, ...).
fn mermaid_id(i: usize) -> String {
    format!("M{}", i)
}

/// Escape a module label for a Mermaid quoted string.
fn mermaid_escape(s: &str) -> String {
    s.replace('"', "'")
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::chunk::Chunk;
    use crate::study::graph::build_graph;
    use rusqlite::Connection;
    use std::collections::HashMap;
    use std::path::PathBuf;

    fn mem_conn() -> Connection {
        Connection::open_in_memory().unwrap()
    }

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

    #[test]
    fn empty_graph_produces_sentinel_diagram() {
        let conn = mem_conn();
        // Don't call build_graph — table absent.
        let diag = extract_arch(&conn, None).unwrap();
        assert!(!diag.is_empty(), "sentinel diagram must be non-empty");
        assert!(
            diag.mermaid_src.contains("graph TD"),
            "must start with graph TD"
        );
        assert!(
            diag.mermaid_src.contains("no symbols detected"),
            "sentinel text expected"
        );
    }

    #[test]
    fn graph_with_modules_produces_module_nodes() {
        let conn = mem_conn();
        let chunks = vec![
            make_chunk("router::get", "fn", "src/router.rs", "fn get() {}"),
            make_chunk("router::post", "fn", "src/router.rs", "fn post() {}"),
            make_chunk("db::connect", "fn", "src/db.rs", "fn connect() {}"),
            make_chunk("db::query", "fn", "src/db.rs", "fn query() {}"),
        ];
        build_graph(&conn, &chunks).unwrap();

        let diag = extract_arch(&conn, None).unwrap();
        assert!(!diag.is_empty());
        assert!(
            diag.mermaid_src.contains("graph TD"),
            "must be mermaid graph TD"
        );
        assert!(diag.mermaid_src.contains("router"), "must contain 'router' module");
        assert!(diag.mermaid_src.contains("db"), "must contain 'db' module");
    }

    #[test]
    fn cross_module_references_produce_edges() {
        let conn = mem_conn();
        // handler references db::connect in its chunk text
        let chunks = vec![
            make_chunk("db::connect", "fn", "src/db.rs", "fn connect() -> Pool {}"),
            make_chunk(
                "handler::run",
                "fn",
                "src/handler.rs",
                "fn run() { db::connect(); }",
            ),
        ];
        build_graph(&conn, &chunks).unwrap();

        let diag = extract_arch(&conn, None).unwrap();
        assert!(!diag.is_empty());
        // At least one edge arrow should appear.
        assert!(
            diag.mermaid_src.contains("-->"),
            "expected at least one directed edge\n{}",
            diag.mermaid_src
        );
    }

    #[test]
    fn single_symbol_produces_non_empty_diagram() {
        let conn = mem_conn();
        let chunks = vec![make_chunk("main", "fn", "src/main.rs", "fn main() {}")];
        build_graph(&conn, &chunks).unwrap();

        let diag = extract_arch(&conn, None).unwrap();
        assert!(!diag.is_empty());
        assert!(diag.mermaid_src.contains("graph TD"));
        assert!(diag.mermaid_src.contains("root") || diag.mermaid_src.contains("M0"));
    }
}
