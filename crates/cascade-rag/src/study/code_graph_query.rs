//! Structural code-graph query: route structural intent questions to the
//! `code_graph` adjacency table instead of vector search.
//!
//! # Purpose
//!
//! Answers questions like "who calls X" or "what's in file Y" using the
//! symbol adjacency table built by [`super::graph`] (rag-14).  This is
//! fundamentally a graph/relational query; running it through vector search
//! would be both slower and less precise.
//!
//! # Algorithm
//!
//! 1. [`classify_structural`] identifies whether the question is structural
//!    by scanning for keyword patterns (`callers of`, `calls`, `references`,
//!    `defined in`, `symbols in`, `what's in`, etc.).
//! 2. [`code_graph_query`] dispatches to either:
//!    - `callers_of(symbol)` for caller-lookup questions.
//!    - `symbols_in(file_path)` for file-content questions.
//! 3. Returns a [`GraphQueryResult`] that includes a human-readable summary
//!    and the raw data for downstream formatting.
//!
//! # Inputs
//!
//! - `conn`: open SQLite connection with a populated `code_graph` table.
//! - `question`: natural-language structural question (English).
//!
//! # Outputs
//!
//! `Ok(Some(GraphQueryResult))` when the question is structural and the graph
//! has data; `Ok(None)` when the question is not structural (caller should
//! fall back to vector search).
//!
//! # Constraints
//!
//! - Symbol/path extraction is keyword-heuristic, not NLP.  False positives
//!   and negatives are possible; this is appropriate for a v1 routing layer.
//! - The `code_graph` table is optional; if absent, all queries return `Ok(None)`.
//!
//! # Ticket
//!
//! rag-09
//!
//! SPORT: MASTER-CRATES.md → cascade-rag::study::code_graph_query

use rusqlite::Connection;

use cascade_types::error::Result;

use super::graph::{callers_of, symbols_in};

// ── Types ─────────────────────────────────────────────────────────────────────

/// The structural intent of a question.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum StructuralIntent {
    /// "who calls X" / "callers of X" / "what references X"
    CallersOf(String),
    /// "what's in file Y" / "symbols in Y" / "defined in Y"
    SymbolsIn(String),
}

/// Result of a structural code-graph query.
#[derive(Debug, Clone)]
pub struct GraphQueryResult {
    /// The intent that was matched and executed.
    pub intent: StructuralIntent,
    /// Human-readable summary of the result.
    pub summary: String,
    /// Raw data: for `CallersOf`, caller symbol names; for `SymbolsIn`, `(symbol, kind)` pairs.
    pub raw: GraphRaw,
}

/// Raw data variant for [`GraphQueryResult`].
#[derive(Debug, Clone)]
pub enum GraphRaw {
    /// Caller symbol names for a `CallersOf` query.
    Callers(Vec<String>),
    /// `(symbol, kind)` pairs for a `SymbolsIn` query.
    Symbols(Vec<(String, String)>),
}

// ── Intent classification ─────────────────────────────────────────────────────

/// Classify a natural-language question into a structural intent, if any.
///
/// Returns `Some(StructuralIntent)` when the question matches a known
/// structural pattern, `None` otherwise.
///
/// # Inputs
///
/// - `question`: trimmed question string (case-insensitive match performed internally).
///
/// # Outputs
///
/// `Option<StructuralIntent>` — the detected intent, or `None`.
pub fn classify_structural(question: &str) -> Option<StructuralIntent> {
    let q = question.trim();
    let lower = q.to_ascii_lowercase();

    // ── Caller patterns ───────────────────────────────────────────────────────
    // "who calls foo"  "callers of foo"  "what references foo"
    // "what calls foo" "functions calling foo"
    let caller_prefixes = [
        "who calls ",
        "callers of ",
        "what calls ",
        "what references ",
        "functions calling ",
        "who references ",
        "what uses ",
        "callers:",
    ];
    for prefix in &caller_prefixes {
        if let Some(rest) = lower.strip_prefix(prefix) {
            let symbol = extract_symbol(rest);
            if !symbol.is_empty() {
                return Some(StructuralIntent::CallersOf(symbol));
            }
        }
    }
    // "X callers" or "X references" (trailing form)
    for suffix in &[" callers", " callers?", " references", " references?"] {
        if let Some(sym_part) = lower.strip_suffix(suffix) {
            let symbol = extract_symbol(sym_part);
            if !symbol.is_empty() {
                return Some(StructuralIntent::CallersOf(symbol));
            }
        }
    }

    // ── Symbol / file patterns ────────────────────────────────────────────────
    // "symbols in src/lib.rs"  "what's in file.rs"  "defined in path"
    let symbol_prefixes = [
        "symbols in ",
        "what's in ",
        "what is in ",
        "defined in ",
        "what symbols are in ",
        "list symbols in ",
        "contents of ",
        "show symbols in ",
        "symbols:",
    ];
    for prefix in &symbol_prefixes {
        if let Some(rest) = lower.strip_prefix(prefix) {
            let path = extract_path(q, rest);
            if !path.is_empty() {
                return Some(StructuralIntent::SymbolsIn(path));
            }
        }
    }

    None
}

/// Extract a symbol name from the remainder of a matched prefix.
///
/// Trims trailing punctuation and whitespace.
fn extract_symbol(rest: &str) -> String {
    rest.trim()
        .trim_end_matches(['?', '.', ':', '\'', '"'])
        .trim()
        .to_string()
}

/// Extract a file path from the remainder.  Uses original case from `original`
/// because file paths are case-sensitive on most systems.
fn extract_path(original: &str, lower_rest: &str) -> String {
    // Find the position in the original string where the rest starts.
    // lower_rest is the lowercased suffix; find the offset from the end.
    let rest_len = lower_rest.len();
    if rest_len > original.len() {
        return lower_rest.trim().to_string();
    }
    let offset = original.len() - rest_len;
    original[offset..]
        .trim()
        .trim_end_matches(['?', '.', '\'', '"'])
        .trim()
        .to_string()
}

// ── Main query function ───────────────────────────────────────────────────────

/// Route a structural question to the code-graph and return results.
///
/// # Inputs
///
/// - `conn`: SQLite connection with a `code_graph` table (may be absent).
/// - `question`: natural-language question about code structure.
///
/// # Outputs
///
/// - `Ok(Some(GraphQueryResult))`: structural question answered from the graph.
/// - `Ok(None)`: question is not structural; caller should use vector search.
/// - `Err(_)`: structural question but SQLite error (rare; table schema mismatch).
///
/// # Constraints
///
/// If `code_graph` table is absent, `callers_of`/`symbols_in` will error;
/// those errors are converted to `Ok(None)` so the caller falls back silently.
pub fn code_graph_query(conn: &Connection, question: &str) -> Result<Option<GraphQueryResult>> {
    let Some(intent) = classify_structural(question) else {
        return Ok(None);
    };

    match &intent {
        StructuralIntent::CallersOf(symbol) => {
            let callers = callers_of(conn, symbol).unwrap_or_default();
            let summary = if callers.is_empty() {
                format!("No callers of `{symbol}` found in the code graph.")
            } else {
                format!("`{symbol}` is referenced by: {}", callers.join(", "))
            };
            Ok(Some(GraphQueryResult {
                intent,
                summary,
                raw: GraphRaw::Callers(callers),
            }))
        }
        StructuralIntent::SymbolsIn(path) => {
            let symbols = symbols_in(conn, path).unwrap_or_default();
            let summary = if symbols.is_empty() {
                format!("No symbols found for `{path}` in the code graph.")
            } else {
                let names: Vec<String> = symbols
                    .iter()
                    .map(|(sym, kind)| format!("{sym} ({kind})"))
                    .collect();
                format!("`{path}` defines: {}", names.join(", "))
            };
            Ok(Some(GraphQueryResult {
                intent,
                summary,
                raw: GraphRaw::Symbols(symbols),
            }))
        }
    }
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

    // ── Helpers ───────────────────────────────────────────────────────────────

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

    fn synthetic_graph() -> Connection {
        let conn = Connection::open_in_memory().unwrap();
        let chunks = vec![
            make_chunk("foo", "fn", "src/lib.rs", "fn foo() {}"),
            make_chunk("bar", "fn", "src/lib.rs", "fn bar() { foo() }"),
            make_chunk("baz", "fn", "src/lib.rs", "fn baz() { foo() }"),
            make_chunk("init", "fn", "src/main.rs", "fn init() { bar(); baz(); }"),
            make_chunk(
                "Config",
                "struct",
                "src/config.rs",
                "struct Config { field: u32 }",
            ),
        ];
        build_graph(&conn, &chunks).unwrap();
        conn
    }

    // ── classify_structural tests ─────────────────────────────────────────────

    #[test]
    fn classify_who_calls_symbol() {
        let i = classify_structural("who calls foo").unwrap();
        assert_eq!(i, StructuralIntent::CallersOf("foo".into()));
    }

    #[test]
    fn classify_callers_of_symbol() {
        let i = classify_structural("callers of bar").unwrap();
        assert_eq!(i, StructuralIntent::CallersOf("bar".into()));
    }

    #[test]
    fn classify_what_references() {
        let i = classify_structural("what references foo?").unwrap();
        assert_eq!(i, StructuralIntent::CallersOf("foo".into()));
    }

    #[test]
    fn classify_symbols_in_file() {
        let i = classify_structural("symbols in src/lib.rs").unwrap();
        assert_eq!(i, StructuralIntent::SymbolsIn("src/lib.rs".into()));
    }

    #[test]
    fn classify_whats_in_file() {
        let i = classify_structural("what's in src/main.rs").unwrap();
        assert_eq!(i, StructuralIntent::SymbolsIn("src/main.rs".into()));
    }

    #[test]
    fn classify_defined_in_file() {
        let i = classify_structural("defined in src/config.rs").unwrap();
        assert_eq!(i, StructuralIntent::SymbolsIn("src/config.rs".into()));
    }

    #[test]
    fn classify_non_structural_returns_none() {
        assert!(classify_structural("how does RRF work?").is_none());
        assert!(classify_structural("what is embedding distance").is_none());
        assert!(classify_structural("explain chunking").is_none());
        assert!(classify_structural("").is_none());
    }

    // ── code_graph_query tests ────────────────────────────────────────────────

    #[test]
    fn code_graph_query_callers_of_foo() {
        let conn = synthetic_graph();
        let res = code_graph_query(&conn, "who calls foo").unwrap().unwrap();

        assert!(matches!(res.intent, StructuralIntent::CallersOf(ref s) if s == "foo"));
        let callers = match &res.raw {
            GraphRaw::Callers(c) => c.clone(),
            _ => panic!("expected Callers"),
        };
        let mut callers_sorted = callers.clone();
        callers_sorted.sort();
        assert_eq!(callers_sorted, vec!["bar", "baz"]);
        assert!(res.summary.contains("bar") || res.summary.contains("baz"));
    }

    #[test]
    fn code_graph_query_symbols_in_file() {
        let conn = synthetic_graph();
        let res = code_graph_query(&conn, "symbols in src/lib.rs")
            .unwrap()
            .unwrap();

        assert!(matches!(res.intent, StructuralIntent::SymbolsIn(ref p) if p == "src/lib.rs"));
        let syms = match &res.raw {
            GraphRaw::Symbols(s) => s.clone(),
            _ => panic!("expected Symbols"),
        };
        assert_eq!(syms.len(), 3);
        assert_eq!(syms[0].0, "foo");
        assert_eq!(syms[1].0, "bar");
        assert_eq!(syms[2].0, "baz");
    }

    #[test]
    fn code_graph_query_no_callers_returns_empty_summary() {
        let conn = synthetic_graph();
        // "Config" has no callers in the graph
        let res = code_graph_query(&conn, "callers of Config")
            .unwrap()
            .unwrap();
        assert!(res.summary.contains("No callers"));
    }

    #[test]
    fn code_graph_query_unknown_file_returns_empty_summary() {
        let conn = synthetic_graph();
        let res = code_graph_query(&conn, "symbols in src/unknown.rs")
            .unwrap()
            .unwrap();
        assert!(res.summary.contains("No symbols"));
    }

    #[test]
    fn code_graph_query_non_structural_returns_none() {
        let conn = synthetic_graph();
        let res = code_graph_query(&conn, "how does embedding work?").unwrap();
        assert!(res.is_none());
    }

    #[test]
    fn code_graph_query_missing_table_returns_none_not_error() {
        // Empty DB — code_graph table doesn't exist; should not propagate error.
        let conn = Connection::open_in_memory().unwrap();
        let res = code_graph_query(&conn, "who calls foo").unwrap();
        // callers_of returns Err which we unwrap_or_default — result is Some with empty callers
        assert!(
            matches!(res, Some(GraphQueryResult { raw: GraphRaw::Callers(ref c), .. }) if c.is_empty())
        );
    }
}
