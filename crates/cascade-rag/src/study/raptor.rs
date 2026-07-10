//! RAPTOR bottom-up summarisation tree — rag-14 implementation.
//!
//! # Purpose
//!
//! Builds a recursive summarisation tree from leaf symbol chunks:
//!
//! 1. **Cluster** chunks by directory locality (same directory = same cluster).
//! 2. **Summarise** each cluster:
//!    - With LLM: call [`HydeLlm::generate_hypothetical`] with a prompt that
//!      lists the symbols and their text.
//!    - Without LLM (template fallback): extract the first sentence of each
//!      chunk text + the symbol names, joined into a compact markdown block.
//! 3. **Root summary**: same two-path strategy applied to all cluster summaries.
//!
//! The resulting tree has depth ≤ 2 (leaves → clusters → root) and is bounded
//! to [`MAX_CLUSTERS`] nodes.  Cache is keyed by BLAKE3 of the full input.
//!
//! # Ticket
//!
//! rag-14
//!
//! SPORT: MASTER-CRATES.md → cascade-rag::study::raptor

use std::collections::HashMap;

use rusqlite::{params, Connection};

use cascade_db::content_hash_str;
use cascade_types::error::{CascadeError, Result};

use crate::search::HydeLlm;

// ── Constants ─────────────────────────────────────────────────────────────────

/// Maximum number of leaf clusters (caps tree width for large projects).
const MAX_CLUSTERS: usize = 32;
/// Maximum characters of chunk_text included per symbol in a cluster prompt/template.
const MAX_CHUNK_PREVIEW: usize = 200;

// ── Public types ──────────────────────────────────────────────────────────────

/// A node in the RAPTOR tree: a label (directory or "root") plus a summary.
#[derive(Debug, Clone)]
pub struct RaptorNode {
    /// Cluster label — usually a directory path or `"(root)"`.
    pub label: String,
    /// Markdown summary for this cluster.
    pub summary: String,
}

/// RAPTOR summarisation tree produced by [`build_raptor_tree`].
#[derive(Debug, Clone, Default)]
pub struct RaptorTree {
    /// Leaf-level cluster nodes (one per directory group).
    pub clusters: Vec<RaptorNode>,
    /// Root summary spanning all clusters (empty string iff `clusters` is empty).
    pub root_summary: String,
}

impl RaptorTree {
    /// Returns `true` when no clusters have been built (empty input or error).
    pub fn is_empty(&self) -> bool {
        self.clusters.is_empty()
    }
}

// ── Schema ────────────────────────────────────────────────────────────────────

const CREATE_RAPTOR_CACHE: &str = "
CREATE TABLE IF NOT EXISTS raptor_cache (
    content_hash TEXT PRIMARY KEY,
    tree_json    TEXT NOT NULL,
    created_at   INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
";

// ── Public API ────────────────────────────────────────────────────────────────

/// Build a RAPTOR summarisation tree from symbol chunks.
///
/// # Inputs
///
/// - `symbol_chunks`: slice of `(symbol_name, chunk_text)` pairs; an empty
///   slice produces an empty tree (no error).
/// - `llm`: optional LLM for richer summaries; absent → template fallback.
/// - `conn`: SQLite connection for the content-hash cache.
///
/// # Outputs
///
/// [`RaptorTree`] with cluster nodes and a root summary.
///
/// # Errors
///
/// SQLite errors are wrapped in [`CascadeError::Other`].
pub async fn build_raptor_tree(
    symbol_chunks: &[(&str, &str)], // (symbol_name, chunk_text)
    llm: Option<&dyn HydeLlm>,
    conn: &Connection,
) -> Result<RaptorTree> {
    if symbol_chunks.is_empty() {
        return Ok(RaptorTree::default());
    }

    // Ensure cache table.
    conn.execute_batch(CREATE_RAPTOR_CACHE)
        .map_err(|e| CascadeError::Other(format!("raptor cache schema: {e}")))?;

    // Cache key over the full input.
    let cache_key = build_cache_key(symbol_chunks);
    if let Some(json) = lookup_cache(conn, &cache_key)? {
        return deserialise_tree(&json);
    }

    // Cluster by inferred directory (prefix of symbol name before `::` or `/`).
    let clustered = cluster_by_dir(symbol_chunks);

    // Summarise each cluster.
    let mut nodes = Vec::with_capacity(clustered.len().min(MAX_CLUSTERS));
    for (label, members) in clustered.iter().take(MAX_CLUSTERS) {
        let summary = summarise_cluster(label, members, llm).await;
        nodes.push(RaptorNode {
            label: label.clone(),
            summary,
        });
    }

    // Root summary.
    let root_summary = if nodes.is_empty() {
        String::new()
    } else {
        let all_summaries: Vec<(&str, &str)> = nodes
            .iter()
            .map(|n| (n.label.as_str(), n.summary.as_str()))
            .collect();
        summarise_root(&all_summaries, llm).await
    };

    let tree = RaptorTree {
        clusters: nodes,
        root_summary,
    };

    // Persist to cache.
    let json = serialise_tree(&tree);
    conn.execute(
        "INSERT OR REPLACE INTO raptor_cache (content_hash, tree_json) VALUES (?1, ?2)",
        params![cache_key, json],
    )
    .map_err(|e| CascadeError::Other(format!("raptor cache insert: {e}")))?;

    Ok(tree)
}

// ── Clustering ────────────────────────────────────────────────────────────────

/// Group chunks by a directory-like prefix of the symbol name.
///
/// Strategy:
/// - If `symbol_name` contains `::` (Rust/Go path) → first segment.
/// - If it contains `/` (file path) → first directory component.
/// - Otherwise → `"(root)"`.
fn cluster_by_dir<'a>(chunks: &[(&'a str, &'a str)]) -> Vec<(String, Vec<(&'a str, &'a str)>)> {
    let mut map: HashMap<String, Vec<(&str, &str)>> = HashMap::new();
    let mut order: Vec<String> = Vec::new();

    for &(sym, text) in chunks {
        let label = infer_cluster_label(sym);
        let entry = map.entry(label.clone()).or_insert_with(|| {
            order.push(label.clone());
            Vec::new()
        });
        entry.push((sym, text));
    }

    order
        .into_iter()
        .map(|k| {
            let v = map.remove(&k).unwrap_or_default();
            (k, v)
        })
        .collect()
}

fn infer_cluster_label(sym: &str) -> String {
    if let Some(idx) = sym.find("::") {
        return sym[..idx].to_string();
    }
    if let Some(idx) = sym.find('/') {
        return sym[..idx].to_string();
    }
    "(root)".to_string()
}

// ── Summarisation ─────────────────────────────────────────────────────────────

async fn summarise_cluster(
    label: &str,
    members: &[(&str, &str)],
    llm: Option<&dyn HydeLlm>,
) -> String {
    if let Some(llm) = llm {
        let prompt = build_cluster_prompt(label, members);
        if let Ok(s) = llm.generate_hypothetical(&prompt).await {
            if !s.trim().is_empty() {
                return s;
            }
        }
    }
    // Template fallback.
    template_cluster_summary(label, members)
}

async fn summarise_root(cluster_summaries: &[(&str, &str)], llm: Option<&dyn HydeLlm>) -> String {
    if let Some(llm) = llm {
        let prompt = build_root_prompt(cluster_summaries);
        if let Ok(s) = llm.generate_hypothetical(&prompt).await {
            if !s.trim().is_empty() {
                return s;
            }
        }
    }
    template_root_summary(cluster_summaries)
}

// ── Template builders (no-LLM path) ──────────────────────────────────────────

fn template_cluster_summary(label: &str, members: &[(&str, &str)]) -> String {
    let sym_count = members.len();
    let preview: Vec<String> = members
        .iter()
        .take(5)
        .map(|(sym, text)| {
            let first = first_sentence(text);
            let preview_len = first.len().min(MAX_CHUNK_PREVIEW);
            format!("- `{}`: {}", sym, &first[..preview_len])
        })
        .collect();
    format!(
        "**{}** ({} symbols)\n\n{}\n",
        label,
        sym_count,
        preview.join("\n")
    )
}

fn template_root_summary(clusters: &[(&str, &str)]) -> String {
    let lines: Vec<String> = clusters
        .iter()
        .map(|(label, summary)| {
            let first = first_sentence(summary);
            let preview_len = first.len().min(150);
            format!("- **{}**: {}", label, &first[..preview_len])
        })
        .collect();
    format!("## Project Summary\n\n{}\n", lines.join("\n"))
}

fn build_cluster_prompt(label: &str, members: &[(&str, &str)]) -> String {
    let items: Vec<String> = members
        .iter()
        .take(10)
        .map(|(sym, text)| {
            let preview_len = text.len().min(MAX_CHUNK_PREVIEW);
            format!("- `{}`: {}", sym, &text[..preview_len])
        })
        .collect();
    format!(
        "Summarise this code cluster named '{}' in 1-2 sentences:\n\n{}",
        label,
        items.join("\n")
    )
}

fn build_root_prompt(clusters: &[(&str, &str)]) -> String {
    let items: Vec<String> = clusters
        .iter()
        .map(|(label, summary)| {
            let preview_len = summary.len().min(200);
            format!("- {}: {}", label, &summary[..preview_len])
        })
        .collect();
    format!(
        "Provide a 2-3 sentence high-level summary of this codebase based on its modules:\n\n{}",
        items.join("\n")
    )
}

fn first_sentence(text: &str) -> &str {
    let trimmed = text.trim();
    for delim in [". ", ".\n", "! ", "? "] {
        if let Some(idx) = trimmed.find(delim) {
            return &trimmed[..idx + 1];
        }
    }
    trimmed
}

// ── Cache helpers ─────────────────────────────────────────────────────────────

fn build_cache_key(chunks: &[(&str, &str)]) -> String {
    let mut s = String::new();
    for (sym, text) in chunks {
        s.push_str(sym);
        s.push('|');
        s.push_str(text);
        s.push('\n');
    }
    content_hash_str(&s)
}

fn lookup_cache(conn: &Connection, hash: &str) -> Result<Option<String>> {
    match conn.query_row(
        "SELECT tree_json FROM raptor_cache WHERE content_hash = ?1",
        params![hash],
        |row| row.get::<_, String>(0),
    ) {
        Ok(v) => Ok(Some(v)),
        Err(rusqlite::Error::QueryReturnedNoRows) => Ok(None),
        Err(e) => Err(CascadeError::Other(format!("raptor cache lookup: {e}"))),
    }
}

/// Minimal JSON-like serialisation (no serde dep — uses our own controlled format).
fn serialise_tree(tree: &RaptorTree) -> String {
    let clusters: Vec<String> = tree
        .clusters
        .iter()
        .map(|n| {
            format!(
                "{{\"label\":{},\"summary\":{}}}",
                json_str(&n.label),
                json_str(&n.summary)
            )
        })
        .collect();
    format!(
        "{{\"clusters\":[{}],\"root\":{}}}",
        clusters.join(","),
        json_str(&tree.root_summary)
    )
}

fn deserialise_tree(json: &str) -> Result<RaptorTree> {
    // Lightweight parse of our own serialised format only.
    let root_summary = extract_json_str(json, "\"root\":")
        .map(unescape_json_str)
        .unwrap_or_default();

    let mut clusters = Vec::new();
    let mut rest = json;
    while let Some(label_start) = rest.find("\"label\":") {
        rest = &rest[label_start + 8..];
        let label = extract_leading_json_str(rest)
            .map(unescape_json_str)
            .unwrap_or_else(|| "?".to_string());
        if let Some(sum_start) = rest.find("\"summary\":") {
            rest = &rest[sum_start + 10..];
            let summary = extract_leading_json_str(rest)
                .map(unescape_json_str)
                .unwrap_or_default();
            clusters.push(RaptorNode { label, summary });
        } else {
            break;
        }
    }
    Ok(RaptorTree {
        clusters,
        root_summary,
    })
}

fn json_str(s: &str) -> String {
    let escaped = s
        .replace('\\', "\\\\")
        .replace('"', "\\\"")
        .replace('\n', "\\n")
        .replace('\r', "\\r");
    format!("\"{}\"", escaped)
}

/// Extract value after `key` from a JSON-like string (handles `"..."` values only).
fn extract_json_str<'a>(json: &'a str, key: &str) -> Option<&'a str> {
    let pos = json.find(key)?;
    let after = &json[pos + key.len()..];
    extract_leading_json_str(after)
}

fn extract_leading_json_str(s: &str) -> Option<&str> {
    let s = s.trim_start();
    if !s.starts_with('"') {
        return None;
    }
    let inner = &s[1..];
    let mut chars = inner.char_indices().peekable();
    while let Some((i, c)) = chars.next() {
        if c == '\\' {
            chars.next(); // skip escaped char
        } else if c == '"' {
            return Some(&inner[..i]);
        }
    }
    None
}

/// Unescape a JSON string value (handles `\\n`, `\\r`, `\\"`, `\\\\`).
fn unescape_json_str(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    let mut chars = s.chars().peekable();
    while let Some(c) = chars.next() {
        if c == '\\' {
            match chars.next() {
                Some('n') => out.push('\n'),
                Some('r') => out.push('\r'),
                Some('"') => out.push('"'),
                Some('\\') => out.push('\\'),
                Some(other) => {
                    out.push('\\');
                    out.push(other);
                }
                None => {}
            }
        } else {
            out.push(c);
        }
    }
    out
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use rusqlite::Connection;

    fn mem_conn() -> Connection {
        Connection::open_in_memory().unwrap()
    }

    #[tokio::test]
    async fn empty_chunks_returns_empty_tree() {
        let conn = mem_conn();
        let tree = build_raptor_tree(&[], None, &conn).await.unwrap();
        assert!(tree.is_empty());
        assert!(tree.root_summary.is_empty());
    }

    #[tokio::test]
    async fn template_path_produces_non_empty_tree() {
        let conn = mem_conn();
        let chunks = vec![
            ("router::get", "fn get() -> Response { ... }"),
            ("router::post", "fn post(body: Json) -> Result<()> { ... }"),
            ("db::connect", "fn connect(url: &str) -> Pool { ... }"),
            (
                "db::query",
                "fn query(pool: &Pool, sql: &str) -> Rows { ... }",
            ),
        ];
        let tree = build_raptor_tree(&chunks, None, &conn).await.unwrap();

        assert!(!tree.is_empty(), "expected non-empty tree");
        assert_eq!(tree.clusters.len(), 2, "expected router + db clusters");
        let labels: Vec<&str> = tree.clusters.iter().map(|n| n.label.as_str()).collect();
        assert!(labels.contains(&"router"), "expected 'router' cluster");
        assert!(labels.contains(&"db"), "expected 'db' cluster");
        for node in &tree.clusters {
            assert!(
                !node.summary.is_empty(),
                "cluster '{}' has empty summary",
                node.label
            );
        }
        assert!(
            !tree.root_summary.is_empty(),
            "root summary should be non-empty"
        );
    }

    #[tokio::test]
    async fn single_module_chunks_produce_root_cluster() {
        let conn = mem_conn();
        let chunks = vec![
            ("foo", "fn foo() { println!(\"hello\"); }"),
            ("bar", "fn bar() -> i32 { 42 }"),
        ];
        let tree = build_raptor_tree(&chunks, None, &conn).await.unwrap();
        assert!(!tree.is_empty());
        assert_eq!(tree.clusters[0].label, "(root)");
        assert!(!tree.root_summary.is_empty());
    }

    #[tokio::test]
    async fn second_call_is_cache_hit() {
        let conn = mem_conn();
        let chunks = vec![
            ("mod_a::func", "fn func() {}"),
            ("mod_b::init", "fn init() {}"),
        ];
        let tree1 = build_raptor_tree(&chunks, None, &conn).await.unwrap();
        let tree2 = build_raptor_tree(&chunks, None, &conn).await.unwrap();
        assert_eq!(tree1.clusters.len(), tree2.clusters.len());
        assert_eq!(tree1.root_summary, tree2.root_summary);
    }

    #[tokio::test]
    async fn llm_path_used_when_provided() {
        use async_trait::async_trait;
        struct MockLlm;
        #[async_trait]
        impl HydeLlm for MockLlm {
            async fn generate_hypothetical(
                &self,
                _q: &str,
            ) -> cascade_types::error::Result<String> {
                Ok("Mock LLM summary.".to_string())
            }
        }
        let conn = mem_conn();
        let chunks = vec![
            ("svc::start", "fn start() {}"),
            ("svc::stop", "fn stop() {}"),
        ];
        let llm = MockLlm;
        let tree = build_raptor_tree(&chunks, Some(&llm), &conn).await.unwrap();
        assert!(!tree.is_empty());
        assert!(
            tree.clusters[0].summary.contains("Mock LLM"),
            "expected LLM summary in cluster"
        );
        assert!(
            tree.root_summary.contains("Mock LLM"),
            "expected LLM summary at root"
        );
    }
}
