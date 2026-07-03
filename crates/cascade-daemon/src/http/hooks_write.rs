//! Purpose: Write-side API handlers for the GCI hooks editor.
//!   POST /api/gci/hooks-write   — add a hook entry to ~/.claude/settings.json.
//!   DELETE /api/gci/hooks-write/:hook_id — remove a hook entry by event+index.
//!
//! Inputs:  JSON request bodies (HookWriteRequest / event+index path param).
//! Outputs: Updated hook list as JSON; 400 on validation failure; 404 on missing hook.
//! Constraints:
//!   - Path is always hardcoded to ~/.claude/settings.json (no path injection).
//!   - Event names validated against an allow-list of known Claude Code hook events.
//!   - Command is stored verbatim; never shell-executed in this handler.
//!   - Atomic write: write to .tmp, then std::fs::rename (POSIX atomic, same filesystem).
//!   - Localhost-only binding enforced by the daemon's listener (127.0.0.1:9761).
//!     SPORT: T-P3-E02-26; MASTER-ROUTES.md § /api/gci/hooks-write

use std::io;
use std::path::{Path, PathBuf};

use axum::{
    extract::Path as AxumPath,
    http::StatusCode,
    response::IntoResponse,
    routing::{delete, post},
    Json, Router,
};
use serde::{Deserialize, Serialize};
use serde_json::{json, Map, Value};

use crate::dashboard::DashboardState;

// ── Allow-list ────────────────────────────────────────────────────────────────

/// Known Claude Code hook event names. Requests using any other name return 400.
///
/// This list mirrors the Claude Code hook event surface as of 2026-06.
const ALLOWED_EVENTS: &[&str] = &[
    "PreToolUse",
    "PostToolUse",
    "UserPromptSubmit",
    "Stop",
    "SubagentStop",
    "Notification",
];

fn is_allowed_event(event: &str) -> bool {
    ALLOWED_EVENTS.contains(&event)
}

// ── Settings path ─────────────────────────────────────────────────────────────

/// Resolve the GCI settings.json path from $HOME.
///
/// Returns None if HOME is not set (should never happen in production, treated as
/// an internal error by callers).
fn settings_path() -> Option<PathBuf> {
    std::env::var_os("HOME").map(|h| PathBuf::from(h).join(".claude/settings.json"))
}

// ── Atomic read-modify-write helpers ─────────────────────────────────────────

/// A flattened hook entry for API responses.
///
/// Mirrors the shape read back by GET /api/gci/hooks so the UI can reconcile
/// without a separate fetch.
#[derive(Debug, Serialize, Deserialize, PartialEq)]
pub struct HookEntry {
    /// Claude Code event name (e.g. "PreToolUse").
    pub event: String,
    /// Optional matcher (PreToolUse / PostToolUse tool-filter).
    pub matcher: Option<String>,
    /// The shell command registered.
    pub command: String,
    /// Optional human label stored in the entry.
    pub label: Option<String>,
}

/// Read settings.json from `path`.  Missing file returns an empty JSON object.
fn read_settings(path: &Path) -> io::Result<Value> {
    match std::fs::read_to_string(path) {
        Ok(s) => {
            serde_json::from_str(&s).map_err(|e| io::Error::new(io::ErrorKind::InvalidData, e))
        }
        Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(Value::Object(Map::new())),
        Err(e) => Err(e),
    }
}

/// Write `value` to `path` atomically: serialize → write to `{path}.tmp` → rename.
fn write_settings_atomic(path: &Path, value: &Value) -> io::Result<()> {
    let tmp_path = path.with_extension("tmp");
    let content = serde_json::to_string_pretty(value)
        .map_err(|e| io::Error::new(io::ErrorKind::InvalidData, e))?;
    // Ensure parent directory exists.
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent)?;
    }
    std::fs::write(&tmp_path, content)?;
    std::fs::rename(&tmp_path, path)?;
    Ok(())
}

/// Extract all hook entries from `settings` into a flat Vec.
fn collect_hooks(settings: &Value) -> Vec<HookEntry> {
    let mut entries = Vec::new();
    if let Some(hooks_obj) = settings.get("hooks").and_then(|h| h.as_object()) {
        for (event, event_val) in hooks_obj {
            if let Some(arr) = event_val.as_array() {
                for item in arr {
                    let command = item
                        .get("command")
                        .and_then(|v| v.as_str())
                        .unwrap_or("")
                        .to_string();
                    if command.is_empty() {
                        continue;
                    }
                    let matcher = item
                        .get("matcher")
                        .and_then(|v| v.as_str())
                        .map(String::from);
                    let label = item.get("label").and_then(|v| v.as_str()).map(String::from);
                    entries.push(HookEntry {
                        event: event.clone(),
                        matcher,
                        command,
                        label,
                    });
                }
            }
        }
    }
    entries
}

/// Add (or append) a hook entry for `event` in `settings`.
///
/// Builds a JSON object `{command, label?, matcher?}` and pushes it onto
/// `settings.hooks[event]`, creating the array if absent.
fn add_hook_to_settings(
    settings: &mut Value,
    event: &str,
    command: &str,
    label: &str,
    matcher: Option<&str>,
) {
    // Ensure hooks object exists.
    if settings.get("hooks").is_none() {
        settings["hooks"] = Value::Object(Map::new());
    }
    let hooks_obj = settings["hooks"]
        .as_object_mut()
        .expect("hooks is an object — just created above if missing");

    // Ensure the event array exists.
    if !hooks_obj.contains_key(event) {
        hooks_obj.insert(event.to_string(), Value::Array(Vec::new()));
    }
    let arr = hooks_obj
        .get_mut(event)
        .and_then(|v| v.as_array_mut())
        .expect("event value is an array — just created above if missing");

    // Build the hook item.
    let mut item = Map::new();
    item.insert("command".to_string(), json!(command));
    if !label.is_empty() {
        item.insert("label".to_string(), json!(label));
    }
    if let Some(m) = matcher {
        item.insert("matcher".to_string(), json!(m));
    }
    arr.push(Value::Object(item));
}

/// Remove the hook at `index` within `settings.hooks[event]`.
///
/// Returns `true` if removed; `false` if the event or index was not found.
fn remove_hook_from_settings(settings: &mut Value, event: &str, index: usize) -> bool {
    let arr = match settings
        .get_mut("hooks")
        .and_then(|h| h.get_mut(event))
        .and_then(|v| v.as_array_mut())
    {
        Some(a) => a,
        None => return false,
    };
    if index >= arr.len() {
        return false;
    }
    arr.remove(index);
    true
}

// ── Request body types ────────────────────────────────────────────────────────

/// POST /api/gci/hooks-write request body.
#[derive(Debug, Deserialize)]
pub struct HookWriteBody {
    /// Claude Code hook event (must be in ALLOWED_EVENTS).
    pub event: String,
    /// Shell command string. Must not be empty.
    pub command: String,
    /// Human-readable label (optional).
    #[serde(default)]
    pub label: String,
    /// Tool matcher for PreToolUse/PostToolUse (optional).
    #[serde(default)]
    pub matcher: Option<String>,
}

// ── Handlers ──────────────────────────────────────────────────────────────────

/// POST /api/gci/hooks-write
///
/// Appends a new hook entry to ~/.claude/settings.json and returns the updated
/// full hook list. Returns 400 if `event` is unknown or `command` is empty.
pub async fn post_hook_handler(Json(body): Json<HookWriteBody>) -> impl IntoResponse {
    // Validate event.
    if !is_allowed_event(&body.event) {
        return (
            StatusCode::BAD_REQUEST,
            Json(json!({
                "error": format!(
                    "unknown event '{}'; allowed: {}",
                    body.event,
                    ALLOWED_EVENTS.join(", ")
                )
            })),
        )
            .into_response();
    }
    // Validate command.
    if body.command.trim().is_empty() {
        return (
            StatusCode::BAD_REQUEST,
            Json(json!({ "error": "command must not be empty" })),
        )
            .into_response();
    }

    let path = match settings_path() {
        Some(p) => p,
        None => {
            return (
                StatusCode::INTERNAL_SERVER_ERROR,
                Json(json!({ "error": "HOME not set" })),
            )
                .into_response();
        }
    };

    let mut settings = match read_settings(&path) {
        Ok(v) => v,
        Err(e) => {
            return (
                StatusCode::INTERNAL_SERVER_ERROR,
                Json(json!({ "error": format!("read settings.json: {e}") })),
            )
                .into_response();
        }
    };

    add_hook_to_settings(
        &mut settings,
        &body.event,
        &body.command,
        &body.label,
        body.matcher.as_deref(),
    );

    if let Err(e) = write_settings_atomic(&path, &settings) {
        return (
            StatusCode::INTERNAL_SERVER_ERROR,
            Json(json!({ "error": format!("write settings.json: {e}") })),
        )
            .into_response();
    }

    let hooks = collect_hooks(&settings);
    Json(json!({ "hooks": hooks })).into_response()
}

/// DELETE /api/gci/hooks-write/:hook_id
///
/// `hook_id` is `{event}:{index}`, e.g. `PreToolUse:0`.
/// Removes the identified entry from ~/.claude/settings.json.
/// Returns 404 if the event or index does not exist.
pub async fn delete_hook_handler(AxumPath(hook_id): AxumPath<String>) -> impl IntoResponse {
    // Parse hook_id as "{event}:{index}".
    let (event, index) = match parse_hook_id(&hook_id) {
        Some(v) => v,
        None => {
            return (
                StatusCode::BAD_REQUEST,
                Json(json!({
                    "error": "hook_id must be '{event}:{index}', e.g. PreToolUse:0"
                })),
            )
                .into_response();
        }
    };

    // Validate event name.
    if !is_allowed_event(&event) {
        return (
            StatusCode::BAD_REQUEST,
            Json(json!({
                "error": format!(
                    "unknown event '{}'; allowed: {}",
                    event,
                    ALLOWED_EVENTS.join(", ")
                )
            })),
        )
            .into_response();
    }

    let path = match settings_path() {
        Some(p) => p,
        None => {
            return (
                StatusCode::INTERNAL_SERVER_ERROR,
                Json(json!({ "error": "HOME not set" })),
            )
                .into_response();
        }
    };

    let mut settings = match read_settings(&path) {
        Ok(v) => v,
        Err(e) => {
            return (
                StatusCode::INTERNAL_SERVER_ERROR,
                Json(json!({ "error": format!("read settings.json: {e}") })),
            )
                .into_response();
        }
    };

    if !remove_hook_from_settings(&mut settings, &event, index) {
        return (
            StatusCode::NOT_FOUND,
            Json(json!({
                "error": format!("hook {event}:{index} not found")
            })),
        )
            .into_response();
    }

    if let Err(e) = write_settings_atomic(&path, &settings) {
        return (
            StatusCode::INTERNAL_SERVER_ERROR,
            Json(json!({ "error": format!("write settings.json: {e}") })),
        )
            .into_response();
    }

    let hooks = collect_hooks(&settings);
    Json(json!({ "hooks": hooks })).into_response()
}

/// Parse `"{event}:{index}"` into `(event, index)`.  Returns None on malformed input.
fn parse_hook_id(hook_id: &str) -> Option<(String, usize)> {
    let colon = hook_id.rfind(':')?;
    let event = hook_id[..colon].to_string();
    let index: usize = hook_id[colon + 1..].parse().ok()?;
    if event.is_empty() {
        return None;
    }
    Some((event, index))
}

// ── Router factory ────────────────────────────────────────────────────────────

/// Build the axum sub-router for write-side hook routes.
///
/// Routes (nested under /api/gci by dashboard.rs):
///   POST   /hooks-write          → post_hook_handler
///   DELETE /hooks-write/:hook_id → delete_hook_handler
pub fn router() -> Router<DashboardState> {
    Router::new()
        .route("/hooks-write", post(post_hook_handler))
        .route("/hooks-write/:hook_id", delete(delete_hook_handler))
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use axum::{body::Body, http::Request, Router};
    use serial_test::serial;
    use tempfile::TempDir;
    use tower::ServiceExt;

    // ── helpers ──────────────────────────────────────────────────────────────

    /// Set HOME to `dir` and create `dir/.claude/`.
    fn set_home(dir: &TempDir) {
        std::env::set_var("HOME", dir.path());
        std::fs::create_dir_all(dir.path().join(".claude")).unwrap();
    }

    /// Read settings.json from a fake-home TempDir.
    fn read_test_settings(dir: &TempDir) -> Value {
        let path = dir.path().join(".claude/settings.json");
        let s = std::fs::read_to_string(path).unwrap_or_else(|_| "{}".to_string());
        serde_json::from_str(&s).unwrap()
    }

    /// Build a minimal test router with a dummy DashboardState.
    fn test_router() -> Router {
        use crate::dashboard::DashboardState;
        use std::sync::Arc;

        // Handlers are stateless (no token check); state is required by Router type only.
        let state = DashboardState {
            token: Arc::new("test-token".to_string()),
            provider_registry: None,
            routing_ring: None,
            middleware: Default::default(),
        };
        router().with_state(state)
    }

    // ── unit tests (no HTTP stack) ────────────────────────────────────────────

    #[test]
    fn test_add_hook_to_empty_settings() {
        let mut settings = Value::Object(Map::new());
        add_hook_to_settings(&mut settings, "PreToolUse", "echo hi", "label1", None);
        let arr = settings["hooks"]["PreToolUse"].as_array().unwrap();
        assert_eq!(arr.len(), 1);
        assert_eq!(arr[0]["command"], "echo hi");
        assert_eq!(arr[0]["label"], "label1");
    }

    #[test]
    fn test_add_hook_appends_to_existing() {
        let mut settings = json!({
            "hooks": { "PreToolUse": [{ "command": "first" }] }
        });
        add_hook_to_settings(&mut settings, "PreToolUse", "second", "", None);
        let arr = settings["hooks"]["PreToolUse"].as_array().unwrap();
        assert_eq!(arr.len(), 2);
        assert_eq!(arr[1]["command"], "second");
    }

    #[test]
    fn test_add_hook_with_matcher() {
        let mut settings = Value::Object(Map::new());
        add_hook_to_settings(&mut settings, "PreToolUse", "echo x", "", Some("Bash"));
        let item = &settings["hooks"]["PreToolUse"][0];
        assert_eq!(item["matcher"], "Bash");
        assert_eq!(item["command"], "echo x");
    }

    #[test]
    fn test_remove_hook_by_index() {
        let mut settings = json!({
            "hooks": {
                "PreToolUse": [
                    { "command": "cmd-0" },
                    { "command": "cmd-1" }
                ]
            }
        });
        let removed = remove_hook_from_settings(&mut settings, "PreToolUse", 0);
        assert!(removed);
        let arr = settings["hooks"]["PreToolUse"].as_array().unwrap();
        assert_eq!(arr.len(), 1);
        assert_eq!(arr[0]["command"], "cmd-1");
    }

    #[test]
    fn test_remove_hook_index_out_of_range_returns_false() {
        let mut settings = json!({
            "hooks": { "Stop": [{ "command": "x" }] }
        });
        assert!(!remove_hook_from_settings(&mut settings, "Stop", 5));
    }

    #[test]
    fn test_remove_hook_missing_event_returns_false() {
        let mut settings = json!({ "hooks": {} });
        assert!(!remove_hook_from_settings(&mut settings, "PreToolUse", 0));
    }

    #[test]
    fn test_parse_hook_id_valid() {
        assert_eq!(
            parse_hook_id("PreToolUse:2"),
            Some(("PreToolUse".to_string(), 2))
        );
        assert_eq!(parse_hook_id("Stop:0"), Some(("Stop".to_string(), 0)));
    }

    #[test]
    fn test_parse_hook_id_invalid() {
        assert_eq!(parse_hook_id("NoColon"), None);
        assert_eq!(parse_hook_id(":0"), None);
        assert_eq!(parse_hook_id("PreToolUse:abc"), None);
    }

    #[test]
    fn test_is_allowed_event() {
        assert!(is_allowed_event("PreToolUse"));
        assert!(is_allowed_event("Stop"));
        assert!(!is_allowed_event("Unknown"));
        assert!(!is_allowed_event(""));
    }

    // ── atomic write test ─────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn test_atomic_write_creates_and_reads_back() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        set_home(&tmp);

        let path = tmp.path().join(".claude/settings.json");
        let val = json!({ "hooks": { "Stop": [{ "command": "echo done" }] } });
        write_settings_atomic(&path, &val).unwrap();

        let back = read_settings(&path).unwrap();
        assert_eq!(back["hooks"]["Stop"][0]["command"], "echo done");

        // .tmp file must not be left behind.
        assert!(!path.with_extension("tmp").exists());
    }

    // ── HTTP handler integration tests ────────────────────────────────────────

    #[tokio::test]
    #[serial(global_env)]
    #[allow(clippy::await_holding_lock)] // Deliberate: env-mutation test lock spans await to keep process-env writes serialized.
    async fn test_post_hook_adds_to_empty_settings() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        set_home(&tmp);

        let app = test_router();
        let req = Request::builder()
            .method("POST")
            .uri("/hooks-write")
            .header("content-type", "application/json")
            .body(Body::from(
                r#"{"event":"Stop","command":"echo done","label":"lbl"}"#,
            ))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);

        let settings = read_test_settings(&tmp);
        let arr = settings["hooks"]["Stop"].as_array().unwrap();
        assert_eq!(arr.len(), 1);
        assert_eq!(arr[0]["command"], "echo done");
    }

    #[tokio::test]
    #[serial(global_env)]
    #[allow(clippy::await_holding_lock)] // Deliberate: env-mutation test lock spans await to keep process-env writes serialized.
    async fn test_post_hook_invalid_event_returns_400() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        set_home(&tmp);

        let app = test_router();
        let req = Request::builder()
            .method("POST")
            .uri("/hooks-write")
            .header("content-type", "application/json")
            .body(Body::from(
                r#"{"event":"BadEvent","command":"echo x","label":""}"#,
            ))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::BAD_REQUEST);

        let bytes = axum::body::to_bytes(resp.into_body(), usize::MAX)
            .await
            .unwrap();
        let body: Value = serde_json::from_slice(&bytes).unwrap();
        assert!(body["error"].as_str().unwrap().contains("unknown event"));
    }

    #[tokio::test]
    #[serial(global_env)]
    #[allow(clippy::await_holding_lock)] // Deliberate: env-mutation test lock spans await to keep process-env writes serialized.
    async fn test_post_hook_empty_command_returns_400() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        set_home(&tmp);

        let app = test_router();
        let req = Request::builder()
            .method("POST")
            .uri("/hooks-write")
            .header("content-type", "application/json")
            .body(Body::from(r#"{"event":"Stop","command":"  ","label":""}"#))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::BAD_REQUEST);

        let bytes = axum::body::to_bytes(resp.into_body(), usize::MAX)
            .await
            .unwrap();
        let body: Value = serde_json::from_slice(&bytes).unwrap();
        assert_eq!(body["error"], "command must not be empty");
    }

    #[tokio::test]
    #[serial(global_env)]
    #[allow(clippy::await_holding_lock)] // Deliberate: env-mutation test lock spans await to keep process-env writes serialized.
    async fn test_delete_hook_removes_entry() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        set_home(&tmp);

        // Pre-populate settings.json with one hook.
        let settings_path = tmp.path().join(".claude/settings.json");
        let initial = json!({
            "hooks": { "Stop": [{ "command": "cmd-to-remove" }] }
        });
        write_settings_atomic(&settings_path, &initial).unwrap();

        let app = test_router();
        let req = Request::builder()
            .method("DELETE")
            .uri("/hooks-write/Stop:0")
            .body(Body::empty())
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);

        let settings = read_test_settings(&tmp);
        let arr = settings["hooks"]["Stop"].as_array().unwrap();
        assert_eq!(arr.len(), 0);
    }

    #[tokio::test]
    #[serial(global_env)]
    #[allow(clippy::await_holding_lock)] // Deliberate: env-mutation test lock spans await to keep process-env writes serialized.
    async fn test_delete_hook_not_found_returns_404() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        set_home(&tmp);

        let app = test_router();
        let req = Request::builder()
            .method("DELETE")
            .uri("/hooks-write/Stop:99")
            .body(Body::empty())
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::NOT_FOUND);
    }
}
