//! Purpose: Personal-workspace API handlers — exposes Claude threads, ideas/inbox,
//!   CRD relay chains, scheduled tasks, fleet quota, and per-account usage ledger.
//!   All routes are read-only and served under `/api/personal`.
//! Inputs:  `$HOME` via `std::env::var_os("HOME")` (never `dirs::home_dir`).
//! Outputs: JSON 200 on success; 500 only on I/O errors (absent paths → empty array).
//! Constraints: Auth is enforced by the outer `build_router` middleware; these handlers
//!   themselves are unauthenticated at the Router level (same as `gci_read_router`).
//!   rusqlite is used for the account-ledger query only.
//! SPORT: MASTER-ENDPOINTS.md § /api/personal; ADR-P3-002

use std::path::{Path, PathBuf};

use axum::{http::StatusCode, response::IntoResponse, routing::get, Json, Router};
use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

use crate::dashboard::DashboardState;

// ── helpers ──────────────────────────────────────────────────────────────────

/// Resolve `$HOME` to a `PathBuf`. Returns `None` if the variable is unset.
fn home_dir() -> Option<PathBuf> {
    std::env::var_os("HOME").map(PathBuf::from)
}

/// Format a `std::time::SystemTime` as an ISO 8601 UTC string, or `"unknown"`.
fn fmt_modified(meta: &std::fs::Metadata) -> String {
    meta.modified()
        .ok()
        .map(|t| {
            let dt: DateTime<Utc> = t.into();
            dt.to_rfc3339()
        })
        .unwrap_or_else(|| "unknown".to_string())
}

// ── shared types ─────────────────────────────────────────────────────────────

/// A single markdown file discovered when scanning thread or memory dirs.
#[derive(Debug, Serialize, Deserialize, PartialEq)]
pub struct ThreadEntry {
    pub project: String,
    pub name: String,
    pub path: String,
    pub size_bytes: u64,
    pub modified_at: String,
}

/// A single idea or inbox item discovered across project dirs.
#[derive(Debug, Serialize, Deserialize, PartialEq)]
pub struct IdeaEntry {
    pub project: String,
    /// `"idea"` or `"inbox"`
    pub kind: String,
    pub name: String,
    pub path: String,
    pub modified_at: String,
}

/// A CRD relay chain summary.
#[derive(Debug, Serialize, Deserialize, PartialEq)]
pub struct CrdChain {
    pub id: String,
    pub status: String,
    pub message_count: u64,
    pub last_updated: String,
}

/// One entry from ~/.claude/temp/scheduled-tasks.json (passed through as-is).
pub type ScheduledTask = Value;

/// Per-account quota info from quota-state.json.
#[derive(Debug, Serialize, Deserialize, PartialEq)]
pub struct FleetAccount {
    pub id: String,
    pub cc_pct: Option<f64>,
    pub cc_resets_at: Option<String>,
    pub oc_pct: Option<f64>,
    pub models_available: Vec<String>,
}

/// One row from the claude-usage SQLite ledger.
#[derive(Debug, Serialize, Deserialize, PartialEq)]
pub struct LedgerEntry {
    pub week: String,
    pub model: String,
    pub input_tokens: i64,
    pub output_tokens: i64,
    pub cost_usd: f64,
}

// ── list helpers ─────────────────────────────────────────────────────────────

/// Collect `*.md` files from `dir` into `ThreadEntry` items tagged with `project`.
/// Returns `Ok(vec![])` if the directory does not exist.
fn collect_md_files(dir: &Path, project: &str) -> Result<Vec<ThreadEntry>, String> {
    if !dir.exists() {
        return Ok(Vec::new());
    }
    let rd = std::fs::read_dir(dir).map_err(|e| e.to_string())?;
    let mut out = Vec::new();
    for entry in rd.flatten() {
        let p = entry.path();
        if p.extension().and_then(|e| e.to_str()) != Some("md") {
            continue;
        }
        let meta = std::fs::metadata(&p).map_err(|e| e.to_string())?;
        out.push(ThreadEntry {
            project: project.to_string(),
            name: p.file_name().unwrap_or_default().to_string_lossy().to_string(),
            path: p.to_string_lossy().to_string(),
            size_bytes: meta.len(),
            modified_at: fmt_modified(&meta),
        });
    }
    Ok(out)
}

/// Collect `*.md` files from a single `dir`, tagging them with `project` and `kind`.
/// Returns `Ok(vec![])` if the directory does not exist.
fn collect_idea_files(dir: &Path, project: &str, kind: &str) -> Result<Vec<IdeaEntry>, String> {
    if !dir.exists() {
        return Ok(Vec::new());
    }
    let rd = std::fs::read_dir(dir).map_err(|e| e.to_string())?;
    let mut out = Vec::new();
    for entry in rd.flatten() {
        let p = entry.path();
        if p.extension().and_then(|e| e.to_str()) != Some("md") {
            continue;
        }
        let meta = std::fs::metadata(&p).map_err(|e| e.to_string())?;
        out.push(IdeaEntry {
            project: project.to_string(),
            kind: kind.to_string(),
            name: p.file_name().unwrap_or_default().to_string_lossy().to_string(),
            path: p.to_string_lossy().to_string(),
            modified_at: fmt_modified(&meta),
        });
    }
    Ok(out)
}

// ── T-P3-E02-10: handlers ────────────────────────────────────────────────────

/// `GET /api/personal/threads`
///
/// Scans `~/.claude/projects/**/MEMORY.md` and `**/memory/*.md` for thread entries.
/// Returns `[ThreadEntry]`.
pub async fn handler_threads() -> impl IntoResponse {
    let Some(home) = home_dir() else {
        return (StatusCode::OK, Json(json!([]))).into_response();
    };
    let projects_dir = home.join(".claude").join("projects");
    let mut entries: Vec<ThreadEntry> = Vec::new();

    // Walk ~/.claude/projects/ one level deep to find per-project dirs.
    if let Ok(rd) = std::fs::read_dir(&projects_dir) {
        for proj_entry in rd.flatten() {
            let proj_path = proj_entry.path();
            if !proj_path.is_dir() {
                continue;
            }
            let proj_name = proj_path
                .file_name()
                .unwrap_or_default()
                .to_string_lossy()
                .to_string();

            // MEMORY.md at project root
            let memory_md = proj_path.join("MEMORY.md");
            if memory_md.is_file() {
                if let Ok(meta) = std::fs::metadata(&memory_md) {
                    entries.push(ThreadEntry {
                        project: proj_name.clone(),
                        name: "MEMORY.md".to_string(),
                        path: memory_md.to_string_lossy().to_string(),
                        size_bytes: meta.len(),
                        modified_at: fmt_modified(&meta),
                    });
                }
            }

            // memory/*.md subdirectory
            let memory_dir = proj_path.join("memory");
            match collect_md_files(&memory_dir, &proj_name) {
                Ok(mut v) => entries.append(&mut v),
                Err(e) => {
                    tracing::warn!("threads: failed to read memory dir {:?}: {}", memory_dir, e);
                }
            }
        }
    }

    (StatusCode::OK, Json(serde_json::to_value(entries).unwrap_or(json!([]))))
        .into_response()
}

/// `GET /api/personal/ideas-inbox`
///
/// Walks `~/.claude/ideas/`, `~/Downloads/.claude/ideas/`, `~/Sites/*/.claude/ideas/`,
/// and `~/Sites/*/.claude/inbox/`. Returns `[IdeaEntry]`.
pub async fn handler_ideas_inbox() -> impl IntoResponse {
    let Some(home) = home_dir() else {
        return (StatusCode::OK, Json(json!([]))).into_response();
    };

    let mut entries: Vec<IdeaEntry> = Vec::new();

    // Global ~/.claude/ideas/
    let global_ideas = home.join(".claude").join("ideas");
    if let Ok(mut v) = collect_idea_files(&global_ideas, "global", "idea") {
        entries.append(&mut v);
    }

    // ~/Downloads/.claude/ideas/
    let downloads_ideas = home.join("Downloads").join(".claude").join("ideas");
    if let Ok(mut v) = collect_idea_files(&downloads_ideas, "downloads", "idea") {
        entries.append(&mut v);
    }

    // ~/Sites/*/.claude/ideas/ and ~/Sites/*/.claude/inbox/
    let sites_dir = home.join("Sites");
    if let Ok(rd) = std::fs::read_dir(&sites_dir) {
        for proj_entry in rd.flatten() {
            let proj_path = proj_entry.path();
            if !proj_path.is_dir() {
                continue;
            }
            let proj_name = proj_path
                .file_name()
                .unwrap_or_default()
                .to_string_lossy()
                .to_string();

            let ideas_dir = proj_path.join(".claude").join("ideas");
            if let Ok(mut v) = collect_idea_files(&ideas_dir, &proj_name, "idea") {
                entries.append(&mut v);
            }

            let inbox_dir = proj_path.join(".claude").join("inbox");
            if let Ok(mut v) = collect_idea_files(&inbox_dir, &proj_name, "inbox") {
                entries.append(&mut v);
            }
        }
    }

    (StatusCode::OK, Json(serde_json::to_value(entries).unwrap_or(json!([]))))
        .into_response()
}

/// `GET /api/personal/crd-chains`
///
/// Lists `~/.claude-relay/chains/` — reads each JSON file and extracts chain metadata.
/// Returns `[CrdChain]`.
pub async fn handler_crd_chains() -> impl IntoResponse {
    let Some(home) = home_dir() else {
        return (StatusCode::OK, Json(json!([]))).into_response();
    };

    let chains_dir = home.join(".claude-relay").join("chains");
    if !chains_dir.exists() {
        return (StatusCode::OK, Json(json!([]))).into_response();
    }

    let mut chains: Vec<CrdChain> = Vec::new();

    if let Ok(rd) = std::fs::read_dir(&chains_dir) {
        for entry in rd.flatten() {
            let p = entry.path();
            if p.extension().and_then(|e| e.to_str()) != Some("json") {
                continue;
            }
            let Ok(content) = std::fs::read_to_string(&p) else {
                continue;
            };
            let Ok(val): Result<Value, _> = serde_json::from_str(&content) else {
                continue;
            };

            let id = p
                .file_stem()
                .unwrap_or_default()
                .to_string_lossy()
                .to_string();
            let status = val
                .get("status")
                .and_then(|v| v.as_str())
                .unwrap_or("unknown")
                .to_string();
            let message_count = val
                .get("messages")
                .and_then(|v| v.as_array())
                .map(|a| a.len() as u64)
                .unwrap_or(0);
            let last_updated = val
                .get("updated_at")
                .and_then(|v| v.as_str())
                .unwrap_or("unknown")
                .to_string();

            chains.push(CrdChain {
                id,
                status,
                message_count,
                last_updated,
            });
        }
    }

    (StatusCode::OK, Json(serde_json::to_value(chains).unwrap_or(json!([]))))
        .into_response()
}

/// `GET /api/personal/scheduled-tasks`
///
/// Reads `~/.claude/temp/scheduled-tasks.json`. Returns the array, or `[]` if absent.
pub async fn handler_scheduled_tasks() -> impl IntoResponse {
    let Some(home) = home_dir() else {
        return (StatusCode::OK, Json(json!([]))).into_response();
    };

    let tasks_file = home.join(".claude").join("temp").join("scheduled-tasks.json");
    if !tasks_file.exists() {
        return (StatusCode::OK, Json(json!([]))).into_response();
    }

    match std::fs::read_to_string(&tasks_file) {
        Ok(content) => {
            let val: Value = serde_json::from_str(&content).unwrap_or(json!([]));
            let arr = if val.is_array() { val } else { json!([val]) };
            (StatusCode::OK, Json(arr)).into_response()
        }
        Err(e) => (
            StatusCode::INTERNAL_SERVER_ERROR,
            Json(json!({"error": e.to_string()})),
        )
            .into_response(),
    }
}

// ── T-P3-E02-11: handlers ────────────────────────────────────────────────────

/// `GET /api/personal/fleet-quota`
///
/// Parses `~/.claude/temp/quota-state.json` (written by the Claw Fleet Widget).
/// Returns `{accounts: [FleetAccount], source: "file"|"file_missing"}`.
pub async fn handler_fleet_quota() -> impl IntoResponse {
    let Some(home) = home_dir() else {
        return (
            StatusCode::OK,
            Json(json!({"accounts": [], "source": "file_missing"})),
        )
            .into_response();
    };

    let quota_file = home.join(".claude").join("temp").join("quota-state.json");
    if !quota_file.exists() {
        return (
            StatusCode::OK,
            Json(json!({"accounts": [], "source": "file_missing"})),
        )
            .into_response();
    }

    match std::fs::read_to_string(&quota_file) {
        Err(e) => (
            StatusCode::INTERNAL_SERVER_ERROR,
            Json(json!({"error": e.to_string()})),
        )
            .into_response(),
        Ok(content) => {
            let val: Value = serde_json::from_str(&content).unwrap_or(json!({}));

            // quota-state.json shape (Claw Fleet Widget):
            // { "accounts": { "acct1": { "cc_pct": 12.0, "cc_resets_at": "...", ... }, ... } }
            // OR a flat {"cc": {...}, "oc": {...}} shape — handle both gracefully.
            let accounts_val = val.get("accounts");
            let mut accounts: Vec<FleetAccount> = Vec::new();

            if let Some(accts) = accounts_val.and_then(|v| v.as_object()) {
                for (id, info) in accts {
                    accounts.push(FleetAccount {
                        id: id.clone(),
                        cc_pct: info.get("cc_pct").and_then(|v| v.as_f64()),
                        cc_resets_at: info
                            .get("cc_resets_at")
                            .and_then(|v| v.as_str())
                            .map(str::to_string),
                        oc_pct: info.get("oc_pct").and_then(|v| v.as_f64()),
                        models_available: info
                            .get("models_available")
                            .and_then(|v| v.as_array())
                            .map(|arr| {
                                arr.iter()
                                    .filter_map(|m| m.as_str().map(str::to_string))
                                    .collect()
                            })
                            .unwrap_or_default(),
                    });
                }
            } else if let Some(accts) = accounts_val.and_then(|v| v.as_array()) {
                // Array-of-objects shape
                for info in accts {
                    let id = info
                        .get("id")
                        .and_then(|v| v.as_str())
                        .unwrap_or("unknown")
                        .to_string();
                    accounts.push(FleetAccount {
                        id,
                        cc_pct: info.get("cc_pct").and_then(|v| v.as_f64()),
                        cc_resets_at: info
                            .get("cc_resets_at")
                            .and_then(|v| v.as_str())
                            .map(str::to_string),
                        oc_pct: info.get("oc_pct").and_then(|v| v.as_f64()),
                        models_available: info
                            .get("models_available")
                            .and_then(|v| v.as_array())
                            .map(|arr| {
                                arr.iter()
                                    .filter_map(|m| m.as_str().map(str::to_string))
                                    .collect()
                            })
                            .unwrap_or_default(),
                    });
                }
            }

            (
                StatusCode::OK,
                Json(json!({"accounts": accounts, "source": "file"})),
            )
                .into_response()
        }
    }
}

/// `GET /api/personal/account-ledger`
///
/// Reads `~/.config/claude-usage/usage.db` (written by the `claude-usage` tool).
/// If absent, returns `{data: [], note: "claude-usage not installed"}`.
/// If present, runs a weekly aggregation query and returns `{data: [LedgerEntry]}`.
pub async fn handler_account_ledger() -> impl IntoResponse {
    let Some(home) = home_dir() else {
        return (
            StatusCode::OK,
            Json(json!({"data": [], "note": "HOME unset"})),
        )
            .into_response();
    };

    let db_path = home
        .join(".config")
        .join("claude-usage")
        .join("usage.db");

    if !db_path.exists() {
        return (
            StatusCode::OK,
            Json(json!({"data": [], "note": "claude-usage not installed"})),
        )
            .into_response();
    }

    // Open the DB in read-only mode via URI flags.
    let db_path_str = db_path.to_string_lossy().to_string();
    let result = tokio::task::spawn_blocking(move || -> Result<Vec<LedgerEntry>, String> {
        let conn = rusqlite::Connection::open_with_flags(
            &db_path_str,
            rusqlite::OpenFlags::SQLITE_OPEN_READ_ONLY,
        )
        .map_err(|e| e.to_string())?;

        // Try the most common schema first; fall back gracefully if columns differ.
        let query = "SELECT \
            strftime('%Y-W%W', created_at) AS week, \
            model, \
            SUM(input_tokens) AS input_tokens, \
            SUM(output_tokens) AS output_tokens, \
            SUM(COALESCE(cost_usd, 0.0)) AS cost_usd \
          FROM usage \
          GROUP BY week, model \
          ORDER BY week DESC, model \
          LIMIT 200";

        let mut stmt = conn.prepare(query).map_err(|e| e.to_string())?;
        let rows = stmt
            .query_map([], |row| {
                Ok(LedgerEntry {
                    week: row.get::<_, String>(0)?,
                    model: row.get::<_, String>(1)?,
                    input_tokens: row.get::<_, i64>(2)?,
                    output_tokens: row.get::<_, i64>(3)?,
                    cost_usd: row.get::<_, f64>(4)?,
                })
            })
            .map_err(|e| e.to_string())?;

        let mut entries = Vec::new();
        for row in rows {
            entries.push(row.map_err(|e| e.to_string())?);
        }
        Ok(entries)
    })
    .await;

    match result {
        Ok(Ok(data)) => {
            (StatusCode::OK, Json(json!({"data": data}))).into_response()
        }
        Ok(Err(e)) => {
            // Schema may differ — return informative error, not 500.
            (
                StatusCode::OK,
                Json(json!({"data": [], "note": format!("query error: {}", e)})),
            )
                .into_response()
        }
        Err(e) => (
            StatusCode::INTERNAL_SERVER_ERROR,
            Json(json!({"error": e.to_string()})),
        )
            .into_response(),
    }
}

// ── router ───────────────────────────────────────────────────────────────────

/// Build the `/api/personal` sub-router (no auth — protected by outer middleware).
pub fn router() -> Router<DashboardState> {
    Router::new()
        .route("/threads", get(handler_threads))
        .route("/ideas-inbox", get(handler_ideas_inbox))
        .route("/crd-chains", get(handler_crd_chains))
        .route("/scheduled-tasks", get(handler_scheduled_tasks))
        .route("/fleet-quota", get(handler_fleet_quota))
        .route("/account-ledger", get(handler_account_ledger))
}

// ── tests ────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use axum::{body::Body, http::Request};
    use tower::ServiceExt;
    use tempfile::TempDir;

    /// Build a minimal test router (no DashboardState needed — we wrap with Router::new()).
    fn test_router() -> axum::Router {
        // Re-build without state type parameter for unit tests.
        axum::Router::new()
            .route("/threads", get(handler_threads))
            .route("/ideas-inbox", get(handler_ideas_inbox))
            .route("/crd-chains", get(handler_crd_chains))
            .route("/scheduled-tasks", get(handler_scheduled_tasks))
            .route("/fleet-quota", get(handler_fleet_quota))
            .route("/account-ledger", get(handler_account_ledger))
    }

    /// Helper: set HOME to a temp dir, call f, restore HOME.
    fn with_fake_home<F: FnOnce(&TempDir)>(f: F) {
        let tmp = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp.path());
        f(&tmp);
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn test_personal_threads_empty() {
        with_fake_home(|_tmp| {
            // No projects dir → should return 200 []
        });
        with_fake_home(|_tmp| {
            let app = test_router();
            tokio::task::block_in_place(|| {
                tokio::runtime::Handle::current().block_on(async {
                    let req = Request::builder()
                        .uri("/threads")
                        .body(Body::empty())
                        .unwrap();
                    let resp = app.oneshot(req).await.unwrap();
                    assert_eq!(resp.status(), StatusCode::OK);
                    let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
                        .await
                        .unwrap();
                    let val: Value = serde_json::from_slice(&body).unwrap();
                    assert!(val.is_array());
                })
            });
        });
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn test_personal_threads_with_memory() {
        with_fake_home(|tmp| {
            // Create ~/.claude/projects/my-project/MEMORY.md
            let proj_dir = tmp.path().join(".claude").join("projects").join("my-project");
            std::fs::create_dir_all(&proj_dir).unwrap();
            std::fs::write(proj_dir.join("MEMORY.md"), "# Memory").unwrap();

            let app = test_router();
            tokio::task::block_in_place(|| {
                tokio::runtime::Handle::current().block_on(async {
                    let req = Request::builder()
                        .uri("/threads")
                        .body(Body::empty())
                        .unwrap();
                    let resp = app.oneshot(req).await.unwrap();
                    assert_eq!(resp.status(), StatusCode::OK);
                    let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
                        .await
                        .unwrap();
                    let val: Value = serde_json::from_slice(&body).unwrap();
                    assert!(val.is_array());
                    assert_eq!(val.as_array().unwrap().len(), 1);
                    assert_eq!(val[0]["name"], "MEMORY.md");
                    assert_eq!(val[0]["project"], "my-project");
                })
            });
        });
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn test_personal_ideas_inbox_empty() {
        with_fake_home(|_tmp| {
            let app = test_router();
            tokio::task::block_in_place(|| {
                tokio::runtime::Handle::current().block_on(async {
                    let req = Request::builder()
                        .uri("/ideas-inbox")
                        .body(Body::empty())
                        .unwrap();
                    let resp = app.oneshot(req).await.unwrap();
                    assert_eq!(resp.status(), StatusCode::OK);
                    let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
                        .await
                        .unwrap();
                    let val: Value = serde_json::from_slice(&body).unwrap();
                    assert!(val.is_array());
                })
            });
        });
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn test_personal_ideas_inbox_with_idea() {
        with_fake_home(|tmp| {
            let ideas_dir = tmp.path().join(".claude").join("ideas");
            std::fs::create_dir_all(&ideas_dir).unwrap();
            std::fs::write(ideas_dir.join("cool-idea.md"), "# An idea").unwrap();

            let app = test_router();
            tokio::task::block_in_place(|| {
                tokio::runtime::Handle::current().block_on(async {
                    let req = Request::builder()
                        .uri("/ideas-inbox")
                        .body(Body::empty())
                        .unwrap();
                    let resp = app.oneshot(req).await.unwrap();
                    assert_eq!(resp.status(), StatusCode::OK);
                    let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
                        .await
                        .unwrap();
                    let val: Value = serde_json::from_slice(&body).unwrap();
                    let arr = val.as_array().unwrap();
                    assert_eq!(arr.len(), 1);
                    assert_eq!(arr[0]["kind"], "idea");
                    assert_eq!(arr[0]["project"], "global");
                })
            });
        });
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn test_personal_crd_chains_no_dir() {
        with_fake_home(|_tmp| {
            let app = test_router();
            tokio::task::block_in_place(|| {
                tokio::runtime::Handle::current().block_on(async {
                    let req = Request::builder()
                        .uri("/crd-chains")
                        .body(Body::empty())
                        .unwrap();
                    let resp = app.oneshot(req).await.unwrap();
                    assert_eq!(resp.status(), StatusCode::OK);
                    let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
                        .await
                        .unwrap();
                    let val: Value = serde_json::from_slice(&body).unwrap();
                    assert!(val.is_array());
                    assert!(val.as_array().unwrap().is_empty());
                })
            });
        });
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn test_personal_crd_chains_with_chain() {
        with_fake_home(|tmp| {
            let chains_dir = tmp.path().join(".claude-relay").join("chains");
            std::fs::create_dir_all(&chains_dir).unwrap();
            let chain_json = serde_json::json!({
                "status": "active",
                "messages": [{"id": 1}, {"id": 2}],
                "updated_at": "2026-06-01T00:00:00Z"
            });
            std::fs::write(
                chains_dir.join("chain-abc123.json"),
                serde_json::to_string(&chain_json).unwrap(),
            )
            .unwrap();

            let app = test_router();
            tokio::task::block_in_place(|| {
                tokio::runtime::Handle::current().block_on(async {
                    let req = Request::builder()
                        .uri("/crd-chains")
                        .body(Body::empty())
                        .unwrap();
                    let resp = app.oneshot(req).await.unwrap();
                    assert_eq!(resp.status(), StatusCode::OK);
                    let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
                        .await
                        .unwrap();
                    let val: Value = serde_json::from_slice(&body).unwrap();
                    let arr = val.as_array().unwrap();
                    assert_eq!(arr.len(), 1);
                    assert_eq!(arr[0]["id"], "chain-abc123");
                    assert_eq!(arr[0]["status"], "active");
                    assert_eq!(arr[0]["message_count"], 2);
                })
            });
        });
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn test_personal_scheduled_tasks_absent() {
        with_fake_home(|_tmp| {
            let app = test_router();
            tokio::task::block_in_place(|| {
                tokio::runtime::Handle::current().block_on(async {
                    let req = Request::builder()
                        .uri("/scheduled-tasks")
                        .body(Body::empty())
                        .unwrap();
                    let resp = app.oneshot(req).await.unwrap();
                    assert_eq!(resp.status(), StatusCode::OK);
                    let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
                        .await
                        .unwrap();
                    let val: Value = serde_json::from_slice(&body).unwrap();
                    assert!(val.is_array());
                })
            });
        });
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn test_personal_scheduled_tasks_present() {
        with_fake_home(|tmp| {
            let temp_dir = tmp.path().join(".claude").join("temp");
            std::fs::create_dir_all(&temp_dir).unwrap();
            let tasks_json = json!([{"id": "task-1", "cron": "0 5 * * *"}]);
            std::fs::write(
                temp_dir.join("scheduled-tasks.json"),
                serde_json::to_string(&tasks_json).unwrap(),
            )
            .unwrap();

            let app = test_router();
            tokio::task::block_in_place(|| {
                tokio::runtime::Handle::current().block_on(async {
                    let req = Request::builder()
                        .uri("/scheduled-tasks")
                        .body(Body::empty())
                        .unwrap();
                    let resp = app.oneshot(req).await.unwrap();
                    assert_eq!(resp.status(), StatusCode::OK);
                    let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
                        .await
                        .unwrap();
                    let val: Value = serde_json::from_slice(&body).unwrap();
                    assert!(val.is_array());
                    assert_eq!(val.as_array().unwrap().len(), 1);
                    assert_eq!(val[0]["id"], "task-1");
                })
            });
        });
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn test_personal_fleet_quota_missing() {
        with_fake_home(|_tmp| {
            let app = test_router();
            tokio::task::block_in_place(|| {
                tokio::runtime::Handle::current().block_on(async {
                    let req = Request::builder()
                        .uri("/fleet-quota")
                        .body(Body::empty())
                        .unwrap();
                    let resp = app.oneshot(req).await.unwrap();
                    assert_eq!(resp.status(), StatusCode::OK);
                    let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
                        .await
                        .unwrap();
                    let val: Value = serde_json::from_slice(&body).unwrap();
                    assert_eq!(val["source"], "file_missing");
                    assert!(val["accounts"].is_array());
                })
            });
        });
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn test_personal_fleet_quota_present() {
        with_fake_home(|tmp| {
            let temp_dir = tmp.path().join(".claude").join("temp");
            std::fs::create_dir_all(&temp_dir).unwrap();
            let quota_json = json!({
                "accounts": {
                    "acct1": {
                        "cc_pct": 15.0,
                        "cc_resets_at": "2026-06-07T00:00:00Z",
                        "oc_pct": 5.0,
                        "models_available": ["sonnet", "haiku"]
                    }
                }
            });
            std::fs::write(
                temp_dir.join("quota-state.json"),
                serde_json::to_string(&quota_json).unwrap(),
            )
            .unwrap();

            let app = test_router();
            tokio::task::block_in_place(|| {
                tokio::runtime::Handle::current().block_on(async {
                    let req = Request::builder()
                        .uri("/fleet-quota")
                        .body(Body::empty())
                        .unwrap();
                    let resp = app.oneshot(req).await.unwrap();
                    assert_eq!(resp.status(), StatusCode::OK);
                    let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
                        .await
                        .unwrap();
                    let val: Value = serde_json::from_slice(&body).unwrap();
                    assert_eq!(val["source"], "file");
                    let accts = val["accounts"].as_array().unwrap();
                    assert_eq!(accts.len(), 1);
                    assert_eq!(accts[0]["id"], "acct1");
                    assert_eq!(accts[0]["cc_pct"], 15.0);
                })
            });
        });
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn test_personal_account_ledger_missing() {
        with_fake_home(|_tmp| {
            let app = test_router();
            tokio::task::block_in_place(|| {
                tokio::runtime::Handle::current().block_on(async {
                    let req = Request::builder()
                        .uri("/account-ledger")
                        .body(Body::empty())
                        .unwrap();
                    let resp = app.oneshot(req).await.unwrap();
                    assert_eq!(resp.status(), StatusCode::OK);
                    let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
                        .await
                        .unwrap();
                    let val: Value = serde_json::from_slice(&body).unwrap();
                    assert_eq!(val["data"].as_array().unwrap().len(), 0);
                    assert!(val["note"].as_str().unwrap().contains("not installed"));
                })
            });
        });
    }
}
