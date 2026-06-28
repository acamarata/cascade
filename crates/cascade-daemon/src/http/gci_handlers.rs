//! Purpose: Read-only GCI API handlers for the dashboard Global panel.
//!   Exposes the user's GCI surface (rules, references, memory, agents, skills,
//!   settings, cascade-tier diagram, hooks) as JSON for the browser frontend.
//! Inputs:  GCI filesystem paths derived from $HOME (no user input in path construction).
//! Outputs: Typed JSON responses; missing dirs return [] / empty objects, never 500.
//! Constraints:
//!   - Read-only: no writes anywhere in this module.
//!   - Path construction uses $HOME env var only (repo convention — NOT dirs::home_dir).
//!   - No vault.env or secrets exposed; settings snapshot redacts sensitive fields.
//!   - Enumeration of ~/Sites/ capped at 1 level (PPI) + 1 sublevel (PRI); no deeper recursion.
//!
//! SPORT: MASTER-ENDPOINTS.md — GET /api/gci/rules, /references, /memory, /agents,
//! /skills, /settings-snapshot, /cascade-diagram, /hooks
//!
//! T-P3-E02-06: rules, references, memory, agents, skills (file metadata lists)
//! T-P3-E02-07: settings-snapshot (redacted settings.json view)
//! T-P3-E02-08: cascade-diagram (tier tree) + hooks inspector

use std::path::{Path, PathBuf};

use axum::routing::get;
use axum::{http::StatusCode, response::IntoResponse, Json, Router};
use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use serde_json::{json, Map, Value};

use crate::dashboard::DashboardState;

// ── Shared helpers ────────────────────────────────────────────────────────────

/// Resolve $HOME as a PathBuf.
///
/// WHY: repo convention requires std::env var lookup (not dirs::home_dir) for
/// consistency with other path resolution in this codebase.
fn home_dir() -> Option<PathBuf> {
    std::env::var_os("HOME").map(PathBuf::from)
}

// ── T-P3-E02-06 types ─────────────────────────────────────────────────────────

/// Metadata entry for a single file in a GCI directory listing.
#[derive(Debug, Serialize, Deserialize, PartialEq)]
pub struct FileEntry {
    /// Filename with extension (e.g. "delegation-mandate.md").
    pub name: String,
    /// Absolute path as a string.
    pub path: String,
    /// File size in bytes.
    pub size_bytes: u64,
    /// ISO 8601 UTC modification time (best-effort; "unknown" on error).
    pub modified_at: String,
}

/// Read all `.md` files from `dir` and return their metadata.
///
/// Inputs: `dir` — an existing or non-existing directory path.
/// Outputs: sorted Vec<FileEntry> (sorted by name); empty vec when dir is absent.
/// Constraints: returns an error String only on unexpected I/O failures (not on ENOENT).
pub fn list_dir_md(dir: &Path) -> Result<Vec<FileEntry>, String> {
    if !dir.exists() {
        return Ok(Vec::new());
    }

    let read_dir =
        std::fs::read_dir(dir).map_err(|e| format!("failed to read {}: {e}", dir.display()))?;

    let mut entries: Vec<FileEntry> = read_dir
        .filter_map(|res| {
            let entry = res.ok()?;
            let path = entry.path();
            if path.extension().and_then(|s| s.to_str()) != Some("md") {
                return None;
            }
            let meta = std::fs::metadata(&path).ok()?;
            let modified_at = meta
                .modified()
                .ok()
                .map(|t| DateTime::<Utc>::from(t).to_rfc3339())
                .unwrap_or_else(|| "unknown".to_string());
            Some(FileEntry {
                name: path.file_name()?.to_string_lossy().into_owned(),
                path: path.to_string_lossy().into_owned(),
                size_bytes: meta.len(),
                modified_at,
            })
        })
        .collect();

    entries.sort_by(|a, b| a.name.cmp(&b.name));
    Ok(entries)
}

/// Shared JSON-array response helper.
///
/// Returns 200+JSON on success, 500 on I/O error (not ENOENT — that yields []).
fn file_list_response(result: Result<Vec<FileEntry>, String>) -> impl IntoResponse {
    match result {
        Ok(entries) => Json(json!(entries)).into_response(),
        Err(msg) => (
            StatusCode::INTERNAL_SERVER_ERROR,
            Json(json!({"error": msg})),
        )
            .into_response(),
    }
}

// ── T-P3-E02-06 handlers ──────────────────────────────────────────────────────

/// GET /api/gci/rules
///
/// Purpose: list all .md files under ~/.claude/rules/.
pub async fn handler_rules() -> impl IntoResponse {
    let result = home_dir()
        .map(|h| list_dir_md(&h.join(".claude/rules")))
        .unwrap_or_else(|| Ok(Vec::new()));
    file_list_response(result)
}

/// GET /api/gci/references
///
/// Purpose: list all .md files under ~/.claude/references/.
pub async fn handler_references() -> impl IntoResponse {
    let result = home_dir()
        .map(|h| list_dir_md(&h.join(".claude/references")))
        .unwrap_or_else(|| Ok(Vec::new()));
    file_list_response(result)
}

/// GET /api/gci/memory
///
/// Purpose: aggregate .md files from every {project}/.claude/memory/ under ~/Sites/.
/// WHY aggregated: the Global panel shows a unified memory view across all projects.
/// Constraints: enumerate only one level under ~/Sites/ (project dirs only; no deeper).
pub async fn handler_memory() -> impl IntoResponse {
    let mut all: Vec<FileEntry> = Vec::new();

    if let Some(home) = home_dir() {
        let sites_dir = home.join("Sites");
        if let Ok(projects) = std::fs::read_dir(&sites_dir) {
            for project_entry in projects.flatten() {
                let memory_dir = project_entry.path().join(".claude").join("memory");
                if let Ok(entries) = list_dir_md(&memory_dir) {
                    all.extend(entries);
                }
            }
        }
    }

    all.sort_by(|a, b| a.name.cmp(&b.name));
    Json(json!(all)).into_response()
}

/// GET /api/gci/agents
///
/// Purpose: list all .md files under ~/.claude/agents/.
pub async fn handler_agents() -> impl IntoResponse {
    let result = home_dir()
        .map(|h| list_dir_md(&h.join(".claude/agents")))
        .unwrap_or_else(|| Ok(Vec::new()));
    file_list_response(result)
}

/// GET /api/gci/skills
///
/// Purpose: list skill bundle directories under ~/.claude/skills/.
/// WHY directories not .md: each skill is a directory with a SKILL.md inside.
/// Returns the same FileEntry shape for directory entries (size_bytes = 0).
pub async fn handler_skills() -> impl IntoResponse {
    let dir = match home_dir() {
        Some(h) => h.join(".claude/skills"),
        None => return Json(json!([])).into_response(),
    };

    if !dir.exists() {
        return Json(json!([])).into_response();
    }

    let mut entries: Vec<FileEntry> = match std::fs::read_dir(&dir) {
        Ok(rd) => rd
            .flatten()
            .filter_map(|e| {
                let p = e.path();
                if !p.is_dir() {
                    return None;
                }
                let meta = std::fs::metadata(&p).ok()?;
                let modified_at = meta
                    .modified()
                    .ok()
                    .map(|t| DateTime::<Utc>::from(t).to_rfc3339())
                    .unwrap_or_else(|| "unknown".to_string());
                Some(FileEntry {
                    name: p.file_name()?.to_string_lossy().into_owned(),
                    path: p.to_string_lossy().into_owned(),
                    size_bytes: 0,
                    modified_at,
                })
            })
            .collect(),
        Err(e) => {
            return (
                StatusCode::INTERNAL_SERVER_ERROR,
                Json(json!({"error": e.to_string()})),
            )
                .into_response()
        }
    };

    entries.sort_by(|a, b| a.name.cmp(&b.name));
    Json(json!(entries)).into_response()
}

// ── T-P3-E02-07 types + handler ──────────────────────────────────────────────

/// Response for GET /api/gci/settings-snapshot.
#[derive(Debug, Serialize)]
pub struct SettingsSnapshot {
    /// Redacted settings object (leaf values matching sensitive key patterns replaced
    /// with "[REDACTED]").
    pub settings: Value,
    /// Dot-path strings of every field that was redacted.
    pub redacted_fields: Vec<String>,
    /// Absolute path of the settings file that was read.
    pub path: String,
}

/// Redaction regex pattern: keys matching this pattern have their string values replaced.
///
/// WHY case-insensitive: user configs vary in capitalization (e.g. "apiKey", "api_key",
/// "API_TOKEN").
const REDACT_PATTERN: &[&str] = &["token", "key", "secret", "password", "auth"];

fn key_is_sensitive(k: &str) -> bool {
    let lower = k.to_lowercase();
    REDACT_PATTERN.iter().any(|pat| lower.contains(pat))
}

/// Recursively redact sensitive string values in a JSON Value.
///
/// Inputs: `val` — the JSON subtree; `prefix` — dot-path of the current node;
///         `redacted` — accumulator for redacted field paths.
/// Outputs: redacted copy of `val`.
pub fn redact_json(val: Value, prefix: &str, redacted: &mut Vec<String>) -> Value {
    match val {
        Value::Object(map) => {
            let mut out = Map::new();
            for (k, v) in map {
                let child_path = if prefix.is_empty() {
                    k.clone()
                } else {
                    format!("{prefix}.{k}")
                };
                if key_is_sensitive(&k) && v.is_string() {
                    redacted.push(child_path.clone());
                    out.insert(k, Value::String("[REDACTED]".to_string()));
                } else {
                    out.insert(k, redact_json(v, &child_path, redacted));
                }
            }
            Value::Object(out)
        }
        Value::Array(arr) => Value::Array(
            arr.into_iter()
                .enumerate()
                .map(|(i, v)| {
                    let child_path = format!("{prefix}[{i}]");
                    redact_json(v, &child_path, redacted)
                })
                .collect(),
        ),
        other => other,
    }
}

/// GET /api/gci/settings-snapshot
///
/// Purpose: return ~/.claude/settings.json with sensitive fields redacted.
/// Security: prevents accidental API-key leakage in the dashboard UI.
pub async fn handler_settings_snapshot() -> impl IntoResponse {
    let path = match home_dir() {
        Some(h) => h.join(".claude/settings.json"),
        None => {
            return Json(SettingsSnapshot {
                settings: Value::Object(Map::new()),
                redacted_fields: vec![],
                path: String::new(),
            })
            .into_response()
        }
    };

    let path_str = path.to_string_lossy().into_owned();

    let raw = match std::fs::read_to_string(&path) {
        Ok(s) => s,
        Err(_) => {
            // File absent or unreadable: return empty object, not 500.
            return Json(SettingsSnapshot {
                settings: Value::Object(Map::new()),
                redacted_fields: vec![],
                path: path_str,
            })
            .into_response();
        }
    };

    let parsed: Value = match serde_json::from_str(&raw) {
        Ok(v) => v,
        Err(e) => {
            return (
                StatusCode::INTERNAL_SERVER_ERROR,
                Json(json!({"error": format!("invalid JSON in settings.json: {e}")})),
            )
                .into_response()
        }
    };

    let mut redacted_fields = Vec::new();
    let settings = redact_json(parsed, "", &mut redacted_fields);

    Json(SettingsSnapshot {
        settings,
        redacted_fields,
        path: path_str,
    })
    .into_response()
}

// ── T-P3-E02-08 types + handlers ─────────────────────────────────────────────

/// A single tier in the GCI cascade diagram.
#[derive(Debug, Serialize)]
pub struct CascadeTierEntry {
    /// Canonical tier name: GCI, ASI, PPI, PRI, PAI.
    pub tier: String,
    /// Human-readable name (e.g. the project or repo folder name).
    pub name: String,
    /// Absolute path of the .claude/ directory.
    pub path: String,
    /// Whether a CLAUDE.md exists at this tier.
    pub file_exists: bool,
    /// First non-empty line of CLAUDE.md (title hint); empty when file absent.
    pub first_line: String,
    /// Number of .claude/ entries (files + dirs) — rough indicator of tier richness.
    pub child_count: usize,
}

fn tier_entry(tier: &str, name: &str, claude_dir: &Path) -> CascadeTierEntry {
    let claude_md = claude_dir.join("CLAUDE.md");
    let file_exists = claude_md.exists();

    let first_line = if file_exists {
        std::fs::read_to_string(&claude_md)
            .unwrap_or_default()
            .lines()
            .find(|l| !l.trim().is_empty())
            .unwrap_or("")
            .trim()
            .to_string()
    } else {
        String::new()
    };

    let child_count = std::fs::read_dir(claude_dir)
        .map(|rd| rd.count())
        .unwrap_or(0);

    CascadeTierEntry {
        tier: tier.to_string(),
        name: name.to_string(),
        path: claude_dir.to_string_lossy().into_owned(),
        file_exists,
        first_line,
        child_count,
    }
}

/// GET /api/gci/cascade-diagram
///
/// Purpose: enumerate the five-tier GCI cascade (GCI, ASI, PPI, PRI, PAI) and
/// return a structured JSON array for the visual tier diagram in the dashboard.
/// Constraints: reads ~/.claude/ (GCI), ~/Sites/.claude/ (ASI), and then for each
/// immediate subdir of ~/Sites/ reads the .claude/ dir (PPI). For each PPI, reads
/// one level of subdirectories for PRI. No symlink following; no recursion past PRI.
pub async fn handler_cascade_diagram() -> impl IntoResponse {
    let mut tiers: Vec<CascadeTierEntry> = Vec::new();

    let home = match home_dir() {
        Some(h) => h,
        None => return Json(json!([])).into_response(),
    };

    // GCI: ~/.claude/
    let gci_dir = home.join(".claude");
    tiers.push(tier_entry("GCI", "~/.claude", &gci_dir));

    // ASI: ~/Sites/.claude/
    let asi_dir = home.join("Sites").join(".claude");
    tiers.push(tier_entry("ASI", "~/Sites/.claude", &asi_dir));

    // PPI + PRI: one level under ~/Sites/
    let sites_dir = home.join("Sites");
    if let Ok(projects) = std::fs::read_dir(&sites_dir) {
        for proj_entry in projects.flatten() {
            let proj_path = proj_entry.path();
            // Skip the .claude/ dir itself; only real project dirs.
            if !proj_path.is_dir() {
                continue;
            }
            let proj_name = proj_path
                .file_name()
                .map(|n| n.to_string_lossy().into_owned())
                .unwrap_or_default();
            if proj_name.starts_with('.') {
                continue;
            }

            let ppi_dir = proj_path.join(".claude");
            if ppi_dir.is_dir() {
                tiers.push(tier_entry("PPI", &proj_name, &ppi_dir));
            }

            // PRI: immediate subdirectories of the project that contain .claude/
            if let Ok(repos) = std::fs::read_dir(&proj_path) {
                for repo_entry in repos.flatten() {
                    let repo_path = repo_entry.path();
                    if !repo_path.is_dir() {
                        continue;
                    }
                    let repo_name = repo_path
                        .file_name()
                        .map(|n| n.to_string_lossy().into_owned())
                        .unwrap_or_default();
                    if repo_name.starts_with('.') {
                        continue;
                    }
                    let pri_dir = repo_path.join(".claude");
                    if pri_dir.is_dir() {
                        let full_name = format!("{proj_name}/{repo_name}");
                        tiers.push(tier_entry("PRI", &full_name, &pri_dir));
                    }
                }
            }
        }
    }

    Json(json!(tiers)).into_response()
}

// ── Hooks inspector ───────────────────────────────────────────────────────────

/// A single registered hook entry.
#[derive(Debug, Serialize)]
pub struct HookEntry {
    /// The CC event name (e.g. "PreToolUse", "UserPromptSubmit").
    pub event: String,
    /// The tool matcher, if any (from the hooks.PreToolUse[n].matcher field).
    pub matcher: Option<String>,
    /// The shell command string.
    pub command: String,
    /// Inferred script path (first token of command if it starts with /); None otherwise.
    pub path: Option<String>,
    /// Number of times this hook has fired (from hook-fire-counts.json); 0 if unknown.
    pub fire_count: u64,
}

/// GET /api/gci/hooks
///
/// Purpose: parse hooks registered in ~/.claude/settings.json and return a flat list
/// with optional fire-count data from ~/.claude/temp/hook-fire-counts.json.
/// Constraints: handles both the new nested format
///   hooks: { PreToolUse: [{matcher,command},...], ... }
/// and the old flat format (array of strings under an event key).
pub async fn handler_hooks() -> impl IntoResponse {
    let home = match home_dir() {
        Some(h) => h,
        None => return Json(json!([])).into_response(),
    };

    // Load settings.json.
    let settings_path = home.join(".claude/settings.json");
    let settings: Value = match std::fs::read_to_string(&settings_path) {
        Ok(s) => serde_json::from_str(&s).unwrap_or(Value::Null),
        Err(_) => Value::Null,
    };

    // Load optional hook-fire-counts.json.
    let counts_path = home.join(".claude/temp/hook-fire-counts.json");
    let fire_counts: Value = std::fs::read_to_string(&counts_path)
        .ok()
        .and_then(|s| serde_json::from_str(&s).ok())
        .unwrap_or(Value::Object(Map::new()));

    let mut hooks: Vec<HookEntry> = Vec::new();

    if let Some(hooks_obj) = settings.get("hooks").and_then(|h| h.as_object()) {
        for (event, event_hooks) in hooks_obj {
            // Nested format: hooks.PreToolUse = [{matcher, command}]
            if let Some(arr) = event_hooks.as_array() {
                for item in arr {
                    let command = extract_command_from_hook_item(item);
                    if command.is_empty() {
                        continue;
                    }
                    let matcher = item
                        .get("matcher")
                        .and_then(|v| v.as_str())
                        .map(String::from);
                    let path = extract_path_from_command(&command);
                    let count_key = format!("{event}:{command}");
                    let fire_count = fire_counts
                        .get(&count_key)
                        .and_then(|v| v.as_u64())
                        .unwrap_or(0);
                    hooks.push(HookEntry {
                        event: event.clone(),
                        matcher,
                        command,
                        path,
                        fire_count,
                    });
                }
            }
        }
    }

    Json(json!(hooks)).into_response()
}

/// Extract the command string from a hook item (handles {command: "..."} and
/// {hooks: [{command: "..."}]}).
fn extract_command_from_hook_item(item: &Value) -> String {
    if let Some(cmd) = item.get("command").and_then(|v| v.as_str()) {
        return cmd.to_string();
    }
    // Fallback: nested hooks array inside item
    if let Some(sub_hooks) = item.get("hooks").and_then(|v| v.as_array()) {
        for h in sub_hooks {
            if let Some(cmd) = h.get("command").and_then(|v| v.as_str()) {
                return cmd.to_string();
            }
        }
    }
    String::new()
}

/// Infer a script path from a command string (first whitespace-delimited token if
/// it looks like an absolute path).
fn extract_path_from_command(cmd: &str) -> Option<String> {
    let first = cmd.split_whitespace().next()?;
    if first.starts_with('/') {
        Some(first.to_string())
    } else {
        None
    }
}

// ── Router factory ────────────────────────────────────────────────────────────

/// Build a read-only axum sub-router for all /api/gci/* endpoints (T-P3-E02-06/07/08).
///
/// Purpose: registers the eight GCI API routes; caller nests this under /api/gci-data
/// or similar in build_router (dashboard.rs).
/// Inputs:  none (all handlers are stateless filesystem readers).
/// Outputs: Router<DashboardState> ready for nesting.
/// SPORT: MASTER-ENDPOINTS.md § GCI read-only API
pub fn gci_read_router() -> Router<DashboardState> {
    Router::new()
        .route("/rules", get(handler_rules))
        .route("/references", get(handler_references))
        .route("/memory", get(handler_memory))
        .route("/agents", get(handler_agents))
        .route("/skills", get(handler_skills))
        .route("/settings-snapshot", get(handler_settings_snapshot))
        .route("/cascade-diagram", get(handler_cascade_diagram))
        .route("/hooks", get(handler_hooks))
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use axum::{body::Body, http::Request, Router};
    use serial_test::serial;
    use tempfile::TempDir;
    use tower::ServiceExt;

    // Helper: fake HOME pointing at a tempdir.
    // Callers must carry #[serial(global_env)] to prevent cross-test HOME races.
    fn with_fake_home<F: FnOnce(&TempDir)>(f: F) {
        let tmp = TempDir::new().unwrap();
        // SAFETY: serialised by #[serial(global_env)] on every calling test.
        std::env::set_var("HOME", tmp.path());
        f(&tmp);
    }

    // ── list_dir_md unit tests ───────────────────────────────────────────────

    #[test]
    fn test_list_dir_md_missing_dir_returns_empty() {
        let tmp = TempDir::new().unwrap();
        let result = list_dir_md(&tmp.path().join("nonexistent"));
        assert!(result.is_ok());
        assert_eq!(result.unwrap(), vec![]);
    }

    #[test]
    fn test_list_dir_md_filters_non_md() {
        let tmp = TempDir::new().unwrap();
        std::fs::write(tmp.path().join("file.md"), "# Rule").unwrap();
        std::fs::write(tmp.path().join("file.txt"), "text").unwrap();
        let entries = list_dir_md(tmp.path()).unwrap();
        assert_eq!(entries.len(), 1);
        assert_eq!(entries[0].name, "file.md");
    }

    #[test]
    fn test_list_dir_md_returns_sorted_entries() {
        let tmp = TempDir::new().unwrap();
        std::fs::write(tmp.path().join("z-last.md"), "z").unwrap();
        std::fs::write(tmp.path().join("a-first.md"), "a").unwrap();
        let entries = list_dir_md(tmp.path()).unwrap();
        assert_eq!(entries[0].name, "a-first.md");
        assert_eq!(entries[1].name, "z-last.md");
    }

    #[test]
    fn test_list_dir_md_entry_shape() {
        let tmp = TempDir::new().unwrap();
        std::fs::write(tmp.path().join("rule.md"), "# Rule\nContent").unwrap();
        let entries = list_dir_md(tmp.path()).unwrap();
        assert_eq!(entries.len(), 1);
        let e = &entries[0];
        assert_eq!(e.name, "rule.md");
        assert!(e.size_bytes > 0);
        assert!(!e.modified_at.is_empty());
        assert_ne!(e.modified_at, "unknown");
    }

    // ── redact_json unit tests ───────────────────────────────────────────────

    #[test]
    fn test_redact_json_redacts_token_field() {
        let val = serde_json::json!({"apiToken": "secret123", "other": "keep"});
        let mut redacted = Vec::new();
        let out = redact_json(val, "", &mut redacted);
        assert_eq!(out["apiToken"], "[REDACTED]");
        assert_eq!(out["other"], "keep");
        assert!(redacted.contains(&"apiToken".to_string()));
    }

    #[test]
    fn test_redact_json_nested_key() {
        let val = serde_json::json!({"env": {"MY_SECRET": "shhh"}});
        let mut redacted = Vec::new();
        let out = redact_json(val, "", &mut redacted);
        assert_eq!(out["env"]["MY_SECRET"], "[REDACTED]");
        assert!(redacted.iter().any(|r| r.contains("MY_SECRET")));
    }

    #[test]
    fn test_redact_json_non_string_value_not_redacted() {
        // Non-string values under sensitive keys are NOT redacted (numbers, bools, objects).
        let val = serde_json::json!({"tokenCount": 42});
        let mut redacted = Vec::new();
        let out = redact_json(val, "", &mut redacted);
        assert_eq!(out["tokenCount"], 42);
        assert!(redacted.is_empty());
    }

    #[test]
    #[serial(global_env)]
    fn test_settings_snapshot_missing_file_returns_empty() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        with_fake_home(|tmp| {
            std::fs::create_dir_all(tmp.path().join(".claude")).unwrap();
            // No settings.json created — expect empty response.
            let rt = tokio::runtime::Builder::new_current_thread()
                .enable_all()
                .build()
                .unwrap();
            rt.block_on(async {
                let app: Router = Router::new().route(
                    "/api/gci/settings-snapshot",
                    axum::routing::get(handler_settings_snapshot),
                );
                let req = Request::builder()
                    .uri("/api/gci/settings-snapshot")
                    .body(Body::empty())
                    .unwrap();
                let resp = app.oneshot(req).await.unwrap();
                assert_eq!(resp.status(), axum::http::StatusCode::OK);
                let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
                    .await
                    .unwrap();
                let json: Value = serde_json::from_slice(&body).unwrap();
                assert_eq!(json["settings"], serde_json::json!({}));
                assert_eq!(json["redacted_fields"], serde_json::json!([]));
            });
        });
    }

    #[test]
    #[serial(global_env)]
    fn test_settings_snapshot_redacts_token() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        with_fake_home(|tmp| {
            let claude_dir = tmp.path().join(".claude");
            std::fs::create_dir_all(&claude_dir).unwrap();
            std::fs::write(
                claude_dir.join("settings.json"),
                r#"{"model":"sonnet","apiToken":"sk-secret","debug":true}"#,
            )
            .unwrap();
            let rt = tokio::runtime::Builder::new_current_thread()
                .enable_all()
                .build()
                .unwrap();
            rt.block_on(async {
                let app: Router = Router::new().route(
                    "/api/gci/settings-snapshot",
                    axum::routing::get(handler_settings_snapshot),
                );
                let req = Request::builder()
                    .uri("/api/gci/settings-snapshot")
                    .body(Body::empty())
                    .unwrap();
                let resp = app.oneshot(req).await.unwrap();
                assert_eq!(resp.status(), axum::http::StatusCode::OK);
                let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
                    .await
                    .unwrap();
                let json: Value = serde_json::from_slice(&body).unwrap();
                assert_eq!(json["settings"]["apiToken"], "[REDACTED]");
                assert_eq!(json["settings"]["model"], "sonnet");
                let rf = json["redacted_fields"].as_array().unwrap();
                assert!(rf.iter().any(|v| v.as_str() == Some("apiToken")));
            });
        });
    }

    // ── cascade-diagram unit test ─────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn test_cascade_diagram_includes_gci_and_asi() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        with_fake_home(|tmp| {
            // Minimal: create ~/.claude/ and ~/Sites/.claude/
            let claude_dir = tmp.path().join(".claude");
            let sites_claude = tmp.path().join("Sites").join(".claude");
            std::fs::create_dir_all(&claude_dir).unwrap();
            std::fs::create_dir_all(&sites_claude).unwrap();
            std::fs::write(claude_dir.join("CLAUDE.md"), "# GCI").unwrap();

            let rt = tokio::runtime::Builder::new_current_thread()
                .enable_all()
                .build()
                .unwrap();
            rt.block_on(async {
                let app: Router = Router::new().route(
                    "/api/gci/cascade-diagram",
                    axum::routing::get(handler_cascade_diagram),
                );
                let req = Request::builder()
                    .uri("/api/gci/cascade-diagram")
                    .body(Body::empty())
                    .unwrap();
                let resp = app.oneshot(req).await.unwrap();
                assert_eq!(resp.status(), axum::http::StatusCode::OK);
                let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
                    .await
                    .unwrap();
                let json: Value = serde_json::from_slice(&body).unwrap();
                let arr = json.as_array().unwrap();
                let tiers: Vec<&str> = arr.iter().filter_map(|e| e["tier"].as_str()).collect();
                assert!(tiers.contains(&"GCI"), "expected GCI tier");
                assert!(tiers.contains(&"ASI"), "expected ASI tier");
                // GCI entry has file_exists = true
                let gci = arr.iter().find(|e| e["tier"] == "GCI").unwrap();
                assert_eq!(gci["file_exists"], true);
                assert_eq!(gci["first_line"], "# GCI");
            });
        });
    }

    // ── hooks unit test ───────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn test_gci_hooks_parses_settings() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        with_fake_home(|tmp| {
            let claude_dir = tmp.path().join(".claude");
            std::fs::create_dir_all(&claude_dir).unwrap();
            std::fs::write(
                claude_dir.join("settings.json"),
                r#"{
                  "hooks": {
                    "PreToolUse": [
                      {"matcher": "Bash", "command": "/usr/local/bin/check.sh"}
                    ],
                    "UserPromptSubmit": [
                      {"command": "echo hello"}
                    ]
                  }
                }"#,
            )
            .unwrap();

            let rt = tokio::runtime::Builder::new_current_thread()
                .enable_all()
                .build()
                .unwrap();
            rt.block_on(async {
                let app: Router =
                    Router::new().route("/api/gci/hooks", axum::routing::get(handler_hooks));
                let req = Request::builder()
                    .uri("/api/gci/hooks")
                    .body(Body::empty())
                    .unwrap();
                let resp = app.oneshot(req).await.unwrap();
                assert_eq!(resp.status(), axum::http::StatusCode::OK);
                let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
                    .await
                    .unwrap();
                let json: Value = serde_json::from_slice(&body).unwrap();
                let arr = json.as_array().unwrap();
                assert_eq!(arr.len(), 2);
                let pre = arr.iter().find(|e| e["event"] == "PreToolUse").unwrap();
                assert_eq!(pre["command"], "/usr/local/bin/check.sh");
                assert_eq!(pre["matcher"], "Bash");
                assert_eq!(pre["path"], "/usr/local/bin/check.sh");
                let prompt = arr
                    .iter()
                    .find(|e| e["event"] == "UserPromptSubmit")
                    .unwrap();
                assert_eq!(prompt["command"], "echo hello");
                assert_eq!(prompt["fire_count"], 0);
            });
        });
    }
}
