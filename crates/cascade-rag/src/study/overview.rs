//! Overview synthesizer — produces a ≤2 KB markdown project overview.
//!
//! # Purpose
//!
//! Combines a [`TechStack`] and the top-level symbols from the code graph into
//! a short markdown summary. Output is purely template-based; if a [`HydeLlm`]
//! is injected, the template is optionally enriched (currently a no-op stub —
//! see TODO comment). Re-running on unchanged input returns a cache hit via a
//! BLAKE3 content-hash stored in SQLite.
//!
//! # Inputs
//!
//! - `symbols`: list of `(symbol, kind)` pairs (typically the top-N from the
//!   graph, one line each in the output).
//! - `stack`: [`TechStack`] from the stack detector.
//! - `llm`: optional `Arc<dyn HydeLlm>` — when provided and the feature is not
//!   stubbed, future work can enrich the template. Ignored in the current pass.
//! - `conn`: live SQLite connection used for the content-hash cache table.
//!
//! # Outputs
//!
//! `Ok(OverviewResult)` containing the markdown string and whether the result
//! was a cache hit.
//!
//! # Constraints
//!
//! - Output must be ≤ 2048 bytes; content is truncated at that boundary.
//! - Cache key = BLAKE3 hash of `(stack serialisation + symbol list)`.
//! - Cache table `study_overview_cache` is created on first call (idempotent).
//!
//! # Ticket
//!
//! rag-14
//!
//! SPORT: MASTER-CRATES.md → cascade-rag::study::overview

use std::sync::Arc;

use rusqlite::{params, Connection};

use cascade_db::content_hash_str;
use cascade_types::error::{CascadeError, Result};

use crate::search::HydeLlm;
use crate::study::stack::TechStack;

// ── Types ─────────────────────────────────────────────────────────────────────

/// Result of [`generate`].
#[derive(Debug, Clone)]
pub struct OverviewResult {
    /// Markdown overview string (≤ 2048 bytes).
    pub markdown: String,
    /// `true` when the result was served from cache.
    pub from_cache: bool,
}

// ── Schema ────────────────────────────────────────────────────────────────────

const MAX_OVERVIEW_BYTES: usize = 2048;

const CREATE_CACHE_TABLE: &str = "
CREATE TABLE IF NOT EXISTS study_overview_cache (
    content_hash TEXT PRIMARY KEY,
    overview_md  TEXT NOT NULL,
    created_at   INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
";

// ── Public API ────────────────────────────────────────────────────────────────

/// Generate (or retrieve from cache) a markdown overview for a project.
///
/// # Inputs
///
/// - `conn`: SQLite connection for the cache table.
/// - `stack`: detected technology stack.
/// - `symbols`: list of `(symbol_name, kind)` pairs to include in the overview.
/// - `project_name`: human-readable project name (used as the heading).
/// - `llm`: optional [`HydeLlm`] summarizer; when `Some`, enriches the overview
///   with a prose summary.  Falls back to the template on `None` or LLM error.
///
/// # Outputs
///
/// [`OverviewResult`] with the markdown text and cache-hit flag.
pub async fn generate(
    conn: &Connection,
    stack: &TechStack,
    symbols: &[(String, String)],
    project_name: &str,
    llm: Option<Arc<dyn HydeLlm>>,
) -> Result<OverviewResult> {
    // Ensure cache table exists.
    conn.execute_batch(CREATE_CACHE_TABLE)
        .map_err(|e| CascadeError::Other(format!("overview cache schema: {e}")))?;

    // Compute cache key from inputs.
    let cache_input = build_cache_key_input(stack, symbols);
    let hash = content_hash_str(&cache_input);

    // Check cache.
    if let Some(cached) = lookup_cache(conn, &hash)? {
        return Ok(OverviewResult {
            markdown: cached,
            from_cache: true,
        });
    }

    // Build the template-based overview; use it as both the prompt for the LLM
    // and the fallback when no LLM is present or the LLM call fails.
    let template = build_template(project_name, stack, symbols);

    // Optionally enrich with an injected summarizer LLM.
    // `generate_hypothetical` is repurposed here as a general prose generator:
    // we pass the template as the prompt so the LLM returns a richer version.
    // On any error the template is used unchanged.
    let markdown = enrich_with_llm(template, llm.as_deref()).await;

    // Store in cache.
    conn.execute(
        "INSERT OR REPLACE INTO study_overview_cache (content_hash, overview_md) \
         VALUES (?1, ?2)",
        params![hash, markdown],
    )
    .map_err(|e| CascadeError::Other(format!("overview cache insert: {e}")))?;

    Ok(OverviewResult {
        markdown,
        from_cache: false,
    })
}

/// Call the LLM to enrich the template prose; fall back to the template on
/// `None` or any error.
///
/// # Inputs
///
/// `template`: the template-built overview string (used as both the LLM prompt
/// and the fallback).
/// `llm`: optional reference to a [`HydeLlm`] summarizer.
///
/// # Outputs
///
/// Either the LLM-generated enriched overview (truncated to
/// [`MAX_OVERVIEW_BYTES`]) or the original `template`.
async fn enrich_with_llm(template: String, llm: Option<&dyn HydeLlm>) -> String {
    if let Some(llm) = llm {
        if let Ok(enriched) = llm.generate_hypothetical(&template).await {
            if !enriched.trim().is_empty() {
                // Truncate at MAX_OVERVIEW_BYTES on a UTF-8 boundary.
                if enriched.len() <= MAX_OVERVIEW_BYTES {
                    return enriched;
                }
                let mut end = MAX_OVERVIEW_BYTES;
                while !enriched.is_char_boundary(end) {
                    end -= 1;
                }
                return enriched[..end].to_string();
            }
        }
    }
    template
}

// ── Template builder ──────────────────────────────────────────────────────────

fn build_template(project_name: &str, stack: &TechStack, symbols: &[(String, String)]) -> String {
    let mut out = String::with_capacity(MAX_OVERVIEW_BYTES);

    // Heading
    out.push_str(&format!("# {project_name}\n\n"));

    // Stack section
    out.push_str("## Tech Stack\n\n");
    if let Some(lang) = &stack.language {
        out.push_str(&format!("- **Language**: {lang}\n"));
    }
    if let Some(fw) = &stack.framework {
        out.push_str(&format!("- **Framework**: {fw}\n"));
    }
    if let Some(bt) = &stack.build_tool {
        out.push_str(&format!("- **Build tool**: {bt}\n"));
    }
    if stack.is_unknown() {
        out.push_str("- *Stack unknown*\n");
    }
    out.push('\n');

    // Symbols section
    if !symbols.is_empty() {
        out.push_str("## Top-Level Symbols\n\n");
        // Limit to first 30 symbols to keep output under 2 KB.
        for (sym, kind) in symbols.iter().take(30) {
            let line = format!("- `{sym}` ({kind})\n");
            if out.len() + line.len() > MAX_OVERVIEW_BYTES.saturating_sub(64) {
                out.push_str("- *(truncated)*\n");
                break;
            }
            out.push_str(&line);
        }
        out.push('\n');
    }

    // Truncate hard at MAX_OVERVIEW_BYTES.
    if out.len() > MAX_OVERVIEW_BYTES {
        // Truncate at a UTF-8 boundary.
        let mut end = MAX_OVERVIEW_BYTES;
        while !out.is_char_boundary(end) {
            end -= 1;
        }
        out.truncate(end);
    }

    out
}

fn build_cache_key_input(stack: &TechStack, symbols: &[(String, String)]) -> String {
    let mut s = String::new();
    if let Some(l) = &stack.language {
        s.push_str(l);
    }
    s.push('|');
    if let Some(f) = &stack.framework {
        s.push_str(f);
    }
    s.push('|');
    if let Some(b) = &stack.build_tool {
        s.push_str(b);
    }
    s.push('|');
    for (sym, kind) in symbols {
        s.push_str(sym);
        s.push(':');
        s.push_str(kind);
        s.push(',');
    }
    s
}

fn lookup_cache(conn: &Connection, hash: &str) -> Result<Option<String>> {
    let result = conn.query_row(
        "SELECT overview_md FROM study_overview_cache WHERE content_hash = ?1",
        params![hash],
        |row| row.get::<_, String>(0),
    );
    match result {
        Ok(v) => Ok(Some(v)),
        Err(rusqlite::Error::QueryReturnedNoRows) => Ok(None),
        Err(e) => Err(CascadeError::Other(format!("overview cache lookup: {e}"))),
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use rusqlite::Connection;

    fn test_conn() -> Connection {
        Connection::open_in_memory().unwrap()
    }

    fn rust_stack() -> TechStack {
        TechStack {
            language: Some("rust".into()),
            framework: None,
            build_tool: Some("cargo".into()),
        }
    }

    fn sample_symbols() -> Vec<(String, String)> {
        vec![
            ("foo".into(), "fn".into()),
            ("bar".into(), "fn".into()),
            ("MyStruct".into(), "impl".into()),
        ]
    }

    #[tokio::test]
    async fn generates_overview_with_expected_sections() {
        let conn = test_conn();
        let result = generate(&conn, &rust_stack(), &sample_symbols(), "myproject", None)
            .await
            .unwrap();

        assert!(!result.from_cache);
        assert!(result.markdown.contains("# myproject"));
        assert!(result.markdown.contains("## Tech Stack"));
        assert!(result.markdown.contains("rust"));
        assert!(result.markdown.contains("cargo"));
        assert!(result.markdown.contains("## Top-Level Symbols"));
        assert!(result.markdown.contains("`foo`"));
        assert!(result.markdown.len() <= MAX_OVERVIEW_BYTES);
    }

    #[tokio::test]
    async fn second_call_is_cache_hit() {
        let conn = test_conn();
        let syms = sample_symbols();
        let stack = rust_stack();

        let first = generate(&conn, &stack, &syms, "myproject", None)
            .await
            .unwrap();
        assert!(!first.from_cache);

        let second = generate(&conn, &stack, &syms, "myproject", None)
            .await
            .unwrap();
        assert!(second.from_cache);
        assert_eq!(first.markdown, second.markdown);
    }

    #[tokio::test]
    async fn different_inputs_are_different_cache_entries() {
        let conn = test_conn();
        let stack = rust_stack();

        let r1 = generate(&conn, &stack, &[("alpha".into(), "fn".into())], "p1", None)
            .await
            .unwrap();
        let r2 = generate(&conn, &stack, &[("beta".into(), "fn".into())], "p2", None)
            .await
            .unwrap();

        assert!(!r1.from_cache);
        assert!(!r2.from_cache);
        assert_ne!(r1.markdown, r2.markdown);
    }

    #[tokio::test]
    async fn output_within_size_limit() {
        let conn = test_conn();
        // 50 symbols — enough to potentially exceed 2 KB
        let syms: Vec<(String, String)> = (0..50)
            .map(|i| (format!("symbol_{i}"), "fn".into()))
            .collect();
        let result = generate(&conn, &rust_stack(), &syms, "bigproject", None)
            .await
            .unwrap();
        assert!(
            result.markdown.len() <= MAX_OVERVIEW_BYTES,
            "overview exceeded 2 KB: {} bytes",
            result.markdown.len()
        );
    }

    #[tokio::test]
    async fn unknown_stack_produces_valid_output() {
        let conn = test_conn();
        let stack = TechStack::unknown();
        let result = generate(&conn, &stack, &[], "unknownproj", None)
            .await
            .unwrap();
        assert!(result.markdown.contains("# unknownproj"));
        assert!(result.markdown.contains("Stack unknown"));
    }

    // ── rag-14: LLM enrichment ───────────────────────────────────────────────

    /// Minimal mock LLM that returns a fixed prose string.
    struct MockOverviewLlm {
        response: String,
    }

    #[async_trait::async_trait]
    impl HydeLlm for MockOverviewLlm {
        async fn generate_hypothetical(
            &self,
            _query: &str,
        ) -> cascade_types::error::Result<String> {
            Ok(self.response.clone())
        }
    }

    /// When an LLM is injected, generate must return the LLM's prose, not the
    /// bare template.
    #[tokio::test]
    async fn uses_injected_summarizer_when_present() {
        let conn = test_conn();
        let llm: Arc<dyn HydeLlm> = Arc::new(MockOverviewLlm {
            response: "LLM-enriched prose for the project.".to_string(),
        });
        let result = generate(
            &conn,
            &rust_stack(),
            &sample_symbols(),
            "enriched_proj",
            Some(llm),
        )
        .await
        .unwrap();

        assert!(
            result.markdown.contains("LLM-enriched prose"),
            "expected LLM output in markdown, got: {}",
            result.markdown
        );
        assert!(!result.from_cache);
    }

    /// When no LLM is injected (`None`), generate must return the template-based
    /// overview (regression guard for the fallback path).
    #[tokio::test]
    async fn falls_back_to_template_when_no_llm() {
        let conn = test_conn();
        let result = generate(&conn, &rust_stack(), &sample_symbols(), "fallback_proj", None)
            .await
            .unwrap();

        assert!(
            result.markdown.contains("# fallback_proj"),
            "template heading must be present"
        );
        assert!(
            result.markdown.contains("## Tech Stack"),
            "template section must be present"
        );
        assert!(!result.from_cache);
    }
}
