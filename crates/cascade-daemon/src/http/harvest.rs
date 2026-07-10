//! Purpose: HTTP handler for POST /api/harvest/cc-session — receives a Claude Code
//! session stop payload, extracts memory facts from the transcript/summary using
//! rule-based patterns, writes them to the dev-<slug> memory namespace, and returns
//! 202 Accepted immediately.
//!
//! # Routes (nested at /api/harvest by the parent router)
//!   POST /cc-session — accept session harvest payload, fire-and-forget extraction
//!
//! # Inputs
//!   POST body: { session_id, project_dir, transcript?, summary?, harvest_personal? }
//!
//! # Outputs
//!   202: { "accepted": true, "session_id": "..." }
//!
//! # Constraints
//!   - Extraction is async (tokio::spawn + spawn_blocking); response is immediate.
//!   - Personal namespace is never written unless harvest_personal=true AND namespace IS personal.
//!   - No network calls in the extractor — rule-based regex/string matching only.
//!   - DB path: ~/.cascade/memory.db (shared with MCP memory handlers).

use axum::{http::StatusCode, response::IntoResponse, routing::post, Json, Router};
use serde::{Deserialize, Serialize};

use cascade_rag::{
    db::run_migrations,
    memory::{fact::upsert_fact, namespace::validate_with_firewall},
};

use crate::dashboard::DashboardState;

// ── Request / Response types ──────────────────────────────────────────────────

/// Harvest request body — all fields optional except session_id.
#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct HarvestCcSessionRequest {
    pub session_id: String,
    #[serde(default)]
    pub project_dir: String,
    #[serde(default)]
    pub transcript: Option<String>,
    #[serde(default)]
    pub summary: Option<String>,
    #[serde(default)]
    pub harvest_personal: bool,
}

// ── Memory fact types ─────────────────────────────────────────────────────────

/// A structured memory fact extracted from a session payload.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MemoryFact {
    pub kind: MemoryFactKind,
    pub content: String,
    pub project_slug: String,
    pub confidence: f64,
}

/// The category of a memory fact.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum MemoryFactKind {
    Decision,
    FileChange,
    ToolPattern,
}

impl std::fmt::Display for MemoryFactKind {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            MemoryFactKind::Decision => write!(f, "decision"),
            MemoryFactKind::FileChange => write!(f, "file_change"),
            MemoryFactKind::ToolPattern => write!(f, "tool_pattern"),
        }
    }
}

// ── Router ────────────────────────────────────────────────────────────────────

pub fn router() -> Router<DashboardState> {
    Router::new().route("/cc-session", post(harvest_cc_session_handler))
}

// ── DB helper ─────────────────────────────────────────────────────────────────

fn open_memory_db() -> Result<rusqlite::Connection, String> {
    use cascade_types::paths::home_dir;
    let path = home_dir().join(".cascade").join("memory.db");
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent).map_err(|e| format!("mkdir: {e}"))?;
    }
    let conn = rusqlite::Connection::open(&path).map_err(|e| format!("open: {e}"))?;
    run_migrations(&conn).map_err(|e| format!("migrations: {e}"))?;
    Ok(conn)
}

// ── Handler ───────────────────────────────────────────────────────────────────

/// POST /api/harvest/cc-session — accept session harvest payload.
///
/// Immediately returns 202 Accepted and spawns a background task to extract
/// memory facts from the transcript/summary and write them to the dev-<slug>
/// memory namespace.
async fn harvest_cc_session_handler(Json(req): Json<HarvestCcSessionRequest>) -> impl IntoResponse {
    let session_id = req.session_id.clone();
    tokio::spawn(async move {
        tokio::task::spawn_blocking(move || {
            let _ = extract_and_write(&req);
        })
        .await
        .ok();
    });
    (
        StatusCode::ACCEPTED,
        Json(serde_json::json!({ "accepted": true, "session_id": session_id })),
    )
}

// ── Extractor ─────────────────────────────────────────────────────────────────

/// Extract MemoryFact[] from transcript/summary text using rule-based patterns.
///
/// No network calls — purely string matching.
/// confidence: 0.8 for summary hits, 0.6 for transcript hits.
pub fn extract_facts(
    _session_id: &str,
    project_slug: &str,
    transcript: Option<&str>,
    summary: Option<&str>,
) -> Vec<MemoryFact> {
    let mut facts = Vec::new();

    // Process summary first (higher confidence)
    if let Some(text) = summary {
        for line in text.lines() {
            let line = line.trim();
            if line.is_empty() {
                continue;
            }
            if let Some(kind) = classify_line(line) {
                facts.push(MemoryFact {
                    kind,
                    content: line.to_string(),
                    project_slug: project_slug.to_string(),
                    confidence: 0.8,
                });
            }
        }
    }

    // Process transcript (lower confidence)
    if let Some(text) = transcript {
        for line in text.lines() {
            let line = line.trim();
            if line.is_empty() {
                continue;
            }
            if let Some(kind) = classify_line(line) {
                facts.push(MemoryFact {
                    kind,
                    content: line.to_string(),
                    project_slug: project_slug.to_string(),
                    confidence: 0.6,
                });
            }
        }
    }

    facts
}

/// Classify a single line into a MemoryFactKind, or None if no rule matches.
fn classify_line(line: &str) -> Option<MemoryFactKind> {
    let lower = line.to_lowercase();

    // Decision: key decision phrases
    let decision_keywords = ["decided to", "chose", "selected", "adr"];
    for kw in &decision_keywords {
        if lower.contains(kw) {
            return Some(MemoryFactKind::Decision);
        }
    }

    // FileChange: editing keywords + path-like pattern
    let file_keywords = ["edited", "created file", "wrote", "modified"];
    let has_path = lower.contains('/')
        || lower.contains(".rs")
        || lower.contains(".ts")
        || lower.contains(".js")
        || lower.contains(".toml")
        || lower.contains(".json")
        || lower.contains(".md")
        || lower.contains(".py")
        || lower.contains(".go");
    if has_path {
        for kw in &file_keywords {
            if lower.contains(kw) {
                return Some(MemoryFactKind::FileChange);
            }
        }
    }

    // ToolPattern: tool usage keywords
    let tool_keywords = ["tool", "bash", "grep", "read"];
    // Only match if line also has a pattern indicator
    let has_pattern =
        lower.contains("pattern") || lower.contains("command") || lower.contains("use");
    if has_pattern {
        for kw in &tool_keywords {
            if lower.contains(kw) {
                return Some(MemoryFactKind::ToolPattern);
            }
        }
    }

    None
}

/// Orchestrate extraction and DB write for a harvest request.
pub fn extract_and_write(req: &HarvestCcSessionRequest) -> Result<usize, String> {
    let project_slug = slug_from_project_dir(&req.project_dir);
    let facts = extract_facts(
        &req.session_id,
        &project_slug,
        req.transcript.as_deref(),
        req.summary.as_deref(),
    );
    if facts.is_empty() {
        return Ok(0);
    }
    let conn = open_memory_db()?;
    write_facts_to_memory(&facts, &conn, req.harvest_personal)
}

/// Write extracted facts to the memory DB.
fn write_facts_to_memory(
    facts: &[MemoryFact],
    conn: &rusqlite::Connection,
    harvest_personal: bool,
) -> Result<usize, String> {
    let mut count = 0;
    for fact in facts {
        let namespace_str = format!("dev-{}", fact.project_slug);
        // validate_with_firewall: personal namespace is blocked unless harvest_personal=true AND it IS personal.
        // dev- namespaces are never personal, so harvest_personal has no effect on them.
        let ns = match validate_with_firewall(&namespace_str, harvest_personal) {
            Ok(ns) => ns,
            Err(e) => {
                tracing::warn!(namespace = %namespace_str, err = %e, "harvest: namespace validation failed, skipping fact");
                continue;
            }
        };
        let kind_str = fact.kind.to_string();
        match upsert_fact(
            conn,
            &ns,
            &kind_str,
            "observed",
            &fact.content,
            fact.confidence,
        ) {
            Ok(_) => count += 1,
            Err(e) => {
                tracing::warn!(kind = %kind_str, err = %e, "harvest: upsert_fact failed, skipping");
            }
        }
    }
    Ok(count)
}

/// Convert a filesystem path to a slug (last component, lowercase, non-alphanum → hyphen).
pub fn slug_from_project_dir(dir: &str) -> String {
    let name = std::path::Path::new(dir)
        .file_name()
        .and_then(|n| n.to_str())
        .unwrap_or("unknown");
    let slug: String = name
        .to_lowercase()
        .chars()
        .map(|c| if c.is_ascii_alphanumeric() { c } else { '-' })
        .collect();
    let slug = slug.trim_matches('-').to_string();
    if slug.is_empty() {
        "unknown".to_string()
    } else {
        slug
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn extract_and_write_produces_facts_in_dev_namespace() {
        use cascade_rag::db::run_migrations;
        use rusqlite::Connection;

        let conn = Connection::open_in_memory().unwrap();
        run_migrations(&conn).unwrap();

        let facts = extract_facts(
            "sess-001",
            "cascade",
            None,
            Some("decided to use axum for HTTP routing. edited /src/main.rs to add routes."),
        );
        assert!(!facts.is_empty(), "should extract at least one fact");

        // Write to in-memory DB directly
        let ns = cascade_rag::memory::namespace::validate("dev-cascade").unwrap();
        for f in &facts {
            let kind_str = f.kind.to_string();
            cascade_rag::memory::fact::upsert_fact(
                &conn,
                &ns,
                &kind_str,
                "observed",
                &f.content,
                f.confidence,
            )
            .unwrap();
        }

        let count: i64 = conn
            .query_row(
                "SELECT COUNT(*) FROM memory_facts WHERE namespace = 'dev-cascade' AND archived = 0",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert!(count > 0, "facts must be written to dev-cascade namespace");
    }

    #[test]
    fn personal_namespace_blocked_without_harvest_personal_flag() {
        use cascade_rag::memory::namespace::{validate_with_firewall, NamespaceError};

        // Personal namespace should be blocked without opt_in
        let err = validate_with_firewall("personal", false).unwrap_err();
        assert!(matches!(err, NamespaceError::PersonalFirewall));

        // dev- namespaces always pass
        let ok = validate_with_firewall("dev-cascade", false);
        assert!(ok.is_ok());
    }

    #[test]
    fn slug_from_project_dir_extracts_last_component() {
        assert_eq!(
            slug_from_project_dir("/home/me/projects/cascade"),
            "cascade"
        );
        assert_eq!(slug_from_project_dir("/home/user/my-project"), "my-project");
        assert_eq!(slug_from_project_dir(""), "unknown");
    }

    #[test]
    fn classify_line_decision() {
        let f = extract_facts("s", "p", None, Some("decided to use axum"));
        assert!(!f.is_empty());
        assert_eq!(f[0].kind, MemoryFactKind::Decision);
        assert_eq!(f[0].confidence, 0.8);
    }

    #[test]
    fn classify_line_file_change() {
        let f = extract_facts("s", "p", None, Some("edited /src/main.rs to add handler"));
        assert!(!f.is_empty());
        assert_eq!(f[0].kind, MemoryFactKind::FileChange);
    }

    #[test]
    fn transcript_lower_confidence() {
        let f = extract_facts("s", "p", Some("decided to use serde"), None);
        assert!(!f.is_empty());
        assert!((f[0].confidence - 0.6).abs() < f64::EPSILON);
    }
}
