//! Code-graph adjacency table: definition → references, file → symbols.
//!
//! # Purpose
//!
//! Builds a simple symbol adjacency table from [`Chunk`] metadata produced by the
//! tree-sitter [`CodeChunker`]. The graph is stored in a dedicated `code_graph`
//! SQLite table and supports two query patterns:
//! - `callers_of(symbol)` — returns all symbols whose chunk text references the
//!   given symbol name (naive string-scan; sufficient for overview generation).
//! - `symbols_in(file)` — returns all `(symbol, kind)` pairs extracted from a
//!   given file path.
//!
//! # Constraints
//!
//! - No tree-sitter dependency at study time — symbol info is read from the
//!   chunk `metadata` map (`symbol_name` + `kind` keys set by `ts_impl`).
//! - The `code_graph` table is created in-place in whatever connection is passed;
//!   callers may use the RAG index connection or an in-memory DB for tests.
//! - Cross-file reference detection is a string scan (`INSTR`), not a
//!   type-checker. False positives are possible in comments/strings.
//!
//! # Ticket
//!
//! rag-14
//!
//! SPORT: MASTER-CRATES.md → cascade-rag::study::graph

use rusqlite::{params, Connection};

use crate::chunk::Chunk;
use cascade_types::error::{CascadeError, Result};

// ── Schema ────────────────────────────────────────────────────────────────────

const CREATE_CODE_GRAPH: &str = "
CREATE TABLE IF NOT EXISTS code_graph (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    symbol      TEXT NOT NULL,
    kind        TEXT NOT NULL,
    file_path   TEXT NOT NULL,
    chunk_text  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_code_graph_symbol ON code_graph(symbol);
CREATE INDEX IF NOT EXISTS idx_code_graph_file   ON code_graph(file_path);
";

// ── Public API ────────────────────────────────────────────────────────────────

/// Build the `code_graph` table from a slice of [`Chunk`]s.
///
/// # Inputs
///
/// - `conn`: open SQLite connection; schema is created if absent (idempotent).
/// - `chunks`: output of [`CodeChunker::chunk`]; only chunks that have a
///   `symbol_name` in their metadata map are indexed.
///
/// # Outputs
///
/// `Ok(())` when the table is populated.
///
/// # Errors
///
/// SQLite errors are wrapped in [`CascadeError::Other`].
pub fn build_graph(conn: &Connection, chunks: &[Chunk]) -> Result<()> {
    conn.execute_batch(CREATE_CODE_GRAPH)
        .map_err(|e| CascadeError::Other(format!("code_graph schema: {e}")))?;

    for chunk in chunks {
        let symbol = match chunk.metadata.get("symbol_name") {
            Some(s) if !s.is_empty() => s.clone(),
            _ => continue,
        };
        let kind = chunk
            .metadata
            .get("kind")
            .cloned()
            .unwrap_or_else(|| "item".into());
        let file = chunk.source_path.to_string_lossy().to_string();

        conn.execute(
            "INSERT INTO code_graph (symbol, kind, file_path, chunk_text) \
             VALUES (?1, ?2, ?3, ?4)",
            params![symbol, kind, file, chunk.text],
        )
        .map_err(|e| CascadeError::Other(format!("code_graph insert: {e}")))?;
    }

    Ok(())
}

/// Return all symbols whose chunk text contains a reference to `symbol`.
///
/// Uses `INSTR` for a naive substring scan. Excludes the symbol itself.
///
/// # Inputs
///
/// - `conn`: connection with a populated `code_graph` table.
/// - `symbol`: symbol name to look up callers for.
///
/// # Outputs
///
/// `Ok(Vec<String>)` — deduplicated list of referencing symbol names.
pub fn callers_of(conn: &Connection, symbol: &str) -> Result<Vec<String>> {
    let mut stmt = conn
        .prepare(
            "SELECT DISTINCT symbol FROM code_graph \
             WHERE INSTR(chunk_text, ?1) > 0 AND symbol != ?1",
        )
        .map_err(|e| CascadeError::Other(format!("callers_of prepare: {e}")))?;

    let rows = stmt
        .query_map(params![symbol], |row| row.get::<_, String>(0))
        .map_err(|e| CascadeError::Other(format!("callers_of query: {e}")))?;

    let mut result = Vec::new();
    for r in rows {
        result.push(r.map_err(|e| CascadeError::Other(e.to_string()))?);
    }
    Ok(result)
}

/// Return all `(symbol, kind)` pairs extracted from `file_path`, in insertion order.
///
/// # Inputs
///
/// - `conn`: connection with a populated `code_graph` table.
/// - `file_path`: path string (exact match against the `file_path` column).
///
/// # Outputs
///
/// `Ok(Vec<(symbol, kind)>)`.
pub fn symbols_in(conn: &Connection, file_path: &str) -> Result<Vec<(String, String)>> {
    let mut stmt = conn
        .prepare("SELECT symbol, kind FROM code_graph WHERE file_path = ?1 ORDER BY id")
        .map_err(|e| CascadeError::Other(format!("symbols_in prepare: {e}")))?;

    let rows = stmt
        .query_map(params![file_path], |row| {
            Ok((row.get::<_, String>(0)?, row.get::<_, String>(1)?))
        })
        .map_err(|e| CascadeError::Other(format!("symbols_in query: {e}")))?;

    let mut result = Vec::new();
    for r in rows {
        result.push(r.map_err(|e| CascadeError::Other(e.to_string()))?);
    }
    Ok(result)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;
    use std::path::PathBuf;

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
    fn build_and_query_symbols_in_file() {
        let conn = Connection::open_in_memory().unwrap();
        let chunks = vec![
            make_chunk("foo", "fn", "src/lib.rs", "fn foo() {}"),
            make_chunk("bar", "fn", "src/lib.rs", "fn bar() { foo() }"),
            make_chunk("baz", "fn", "src/other.rs", "fn baz() {}"),
        ];
        build_graph(&conn, &chunks).unwrap();

        let syms = symbols_in(&conn, "src/lib.rs").unwrap();
        assert_eq!(syms.len(), 2);
        assert_eq!(syms[0], ("foo".into(), "fn".into()));
        assert_eq!(syms[1], ("bar".into(), "fn".into()));

        let other = symbols_in(&conn, "src/other.rs").unwrap();
        assert_eq!(other.len(), 1);
        assert_eq!(other[0].0, "baz");
    }

    #[test]
    fn callers_of_finds_references() {
        let conn = Connection::open_in_memory().unwrap();
        let chunks = vec![
            make_chunk("foo", "fn", "src/lib.rs", "fn foo() {}"),
            make_chunk("bar", "fn", "src/lib.rs", "fn bar() { foo() }"),
            make_chunk("baz", "fn", "src/lib.rs", "fn baz() { foo() }"),
            make_chunk("qux", "fn", "src/lib.rs", "fn qux() {}"),
        ];
        build_graph(&conn, &chunks).unwrap();

        let mut callers = callers_of(&conn, "foo").unwrap();
        callers.sort();
        assert_eq!(callers, vec!["bar", "baz"]);

        assert!(!callers.contains(&"qux".to_string()));
        assert!(!callers.contains(&"foo".to_string()));
    }

    #[test]
    fn unnamed_chunks_are_skipped() {
        let conn = Connection::open_in_memory().unwrap();
        let chunk = Chunk {
            chunk_id: None,
            source_path: PathBuf::from("src/lib.rs"),
            chunk_index: 0,
            text: "use std::collections::HashMap;".into(),
            char_start: 0,
            char_end: 30,
            line_start: 1,
            line_end: 1,
            parent_chunk_id: None,
            heading_path: None,
            metadata: HashMap::new(),
        };
        build_graph(&conn, &[chunk]).unwrap();
        let syms = symbols_in(&conn, "src/lib.rs").unwrap();
        assert!(syms.is_empty());
    }
}
