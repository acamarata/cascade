//! Tests for the MCP resource subsystem.

use std::collections::HashMap as StdHashMap;
use std::sync::Arc;

use async_trait::async_trait;

use crate::error::McpServerError;
use crate::handler::McpHandler;

use super::backend::{is_safe_segment, validate_uri_safety};
use super::registry::{ResourceRegistry, ResourcesListHandler, ResourcesReadHandler};
use super::types::ContentBackend;

// ── MockContentBackend ──────────────────────────────────────────────────────

struct MockContentBackend {
    data: StdHashMap<String, String>,
}

impl MockContentBackend {
    fn new(data: StdHashMap<String, String>) -> Self {
        Self { data }
    }
}

#[async_trait]
impl ContentBackend for MockContentBackend {
    async fn read_uri(&self, uri: &str) -> Result<Option<String>, McpServerError> {
        validate_uri_safety(uri)?;
        if !uri.starts_with("cascade://") {
            return Err(McpServerError::InvalidParams {
                detail: format!("unknown URI scheme: {uri}"),
            });
        }
        let tail = uri.trim_start_matches("cascade://");
        for seg in tail.split('/') {
            if !seg.is_empty() && !is_safe_segment(seg) {
                return Err(McpServerError::InvalidParams {
                    detail: format!("unsafe segment: {seg}"),
                });
            }
        }
        Ok(self.data.get(uri).cloned())
    }
}

fn mock_registry() -> ResourceRegistry {
    let mut data = StdHashMap::new();
    data.insert(
        "cascade://tier/gci".into(),
        "# GCI content\n\nGlobal instructions.".into(),
    );
    data.insert(
        "cascade://tier/asi".into(),
        "# ASI content\n\nAll-sites instructions.".into(),
    );
    data.insert(
        "cascade://memory/acamarata/decisions.md".into(),
        "# Decisions\n\n- ADR-001: use Rust".into(),
    );
    data.insert(
        "cascade://inbox/acamarata".into(),
        r#"[{"file":"msg-2026-01-01-test.md","content":"hello"}]"#.into(),
    );
    ResourceRegistry::with_backend(Arc::new(MockContentBackend::new(data)))
}

// ── resources/list ──────────────────────────────────────────────────────────

#[tokio::test]
async fn list_returns_resources_array() {
    let reg = mock_registry();
    let result = reg.list(None).await.unwrap();
    let resources = result["resources"].as_array().unwrap();
    assert!(
        !resources.is_empty(),
        "resources/list must return at least one resource"
    );
}

#[tokio::test]
async fn list_contains_gci_uri() {
    let reg = mock_registry();
    let result = reg.list(None).await.unwrap();
    let resources = result["resources"].as_array().unwrap();
    let uris: Vec<&str> = resources
        .iter()
        .filter_map(|r| r.get("uri").and_then(|u| u.as_str()))
        .collect();
    assert!(
        uris.contains(&"cascade://tier/gci"),
        "resources list must contain cascade://tier/gci; got: {uris:?}"
    );
}

#[tokio::test]
async fn list_contains_memory_uri() {
    let reg = mock_registry();
    let result = reg.list(None).await.unwrap();
    let resources = result["resources"].as_array().unwrap();
    let uris: Vec<&str> = resources
        .iter()
        .filter_map(|r| r.get("uri").and_then(|u| u.as_str()))
        .collect();
    assert!(
        uris.iter().any(|u| u.starts_with("cascade://memory/")),
        "resources list must contain at least one memory URI; got: {uris:?}"
    );
}

#[tokio::test]
async fn list_pagination_cursor() {
    let reg = mock_registry();
    let page0 = reg.list(None).await.unwrap();
    let resources0 = page0["resources"].as_array().unwrap();
    assert!(!resources0.is_empty(), "first page must not be empty");

    if let Some(cursor) = page0.get("nextCursor").and_then(|c| c.as_str()) {
        let params = serde_json::json!({ "cursor": cursor });
        let page1 = reg.list(Some(&params)).await.unwrap();
        let resources1 = page1["resources"].as_array().unwrap();
        let uri0 = resources0[0].get("uri").and_then(|u| u.as_str()).unwrap();
        let uri1 = resources1[0].get("uri").and_then(|u| u.as_str()).unwrap();
        assert_ne!(uri0, uri1, "page 1 must start after page 0");
    }
}

// ── resources/read ──────────────────────────────────────────────────────────

#[tokio::test]
async fn read_gci_returns_text_contents() {
    let reg = mock_registry();
    let params = serde_json::json!({ "uri": "cascade://tier/gci" });
    let result = reg.read(Some(&params)).await.unwrap();
    let contents = result["contents"].as_array().unwrap();
    assert_eq!(contents.len(), 1);
    let item = &contents[0];
    assert_eq!(item["uri"], "cascade://tier/gci");
    assert!(
        item["text"].as_str().unwrap().contains("GCI"),
        "text must contain GCI content"
    );
    assert_eq!(item["mimeType"], "text/markdown");
}

#[tokio::test]
async fn read_unknown_uri_returns_invalid_params() {
    let reg = mock_registry();
    let params = serde_json::json!({ "uri": "unknown://foo/bar" });
    let err = reg.read(Some(&params)).await.unwrap_err();
    assert!(
        matches!(err, McpServerError::InvalidParams { .. }),
        "unexpected error variant: {err:?}"
    );
}

#[tokio::test]
async fn read_memory_file_returns_content() {
    let reg = mock_registry();
    let params = serde_json::json!({ "uri": "cascade://memory/acamarata/decisions.md" });
    let result = reg.read(Some(&params)).await.unwrap();
    let text = result["contents"][0]["text"].as_str().unwrap();
    assert!(
        text.contains("Decisions"),
        "memory read must return decisions content"
    );
}

#[tokio::test]
async fn read_inbox_returns_json_mime() {
    let reg = mock_registry();
    let params = serde_json::json!({ "uri": "cascade://inbox/acamarata" });
    let result = reg.read(Some(&params)).await.unwrap();
    let mime = result["contents"][0]["mimeType"].as_str().unwrap();
    assert_eq!(mime, "application/json");
}

#[tokio::test]
async fn read_missing_uri_field_returns_invalid_params() {
    let reg = mock_registry();
    let params = serde_json::json!({ "something": "else" });
    let err = reg.read(Some(&params)).await.unwrap_err();
    assert!(
        matches!(err, McpServerError::InvalidParams { .. }),
        "missing uri field must return InvalidParams"
    );
}

// ── security: path traversal ─────────────────────────────────────────────────

#[tokio::test]
async fn read_path_traversal_returns_invalid_params() {
    let reg = mock_registry();
    for evil_uri in &[
        "cascade://tier/../../etc/passwd",
        "cascade://memory/../../../etc/shadow",
        "cascade://tier/gci\0evil",
    ] {
        let params = serde_json::json!({ "uri": evil_uri });
        let result = reg.read(Some(&params)).await;
        assert!(
            result.is_err(),
            "path traversal URI must be rejected: {evil_uri}"
        );
    }
}

// ── resources/subscribe + unsubscribe ───────────────────────────────────────

#[tokio::test]
async fn subscribe_unsubscribe_round_trip() {
    let reg = mock_registry();
    let conn = "conn-abc";
    let uri = "cascade://tier/gci";

    assert!(
        !reg.subscriptions.is_subscribed(conn, uri).await,
        "must not be subscribed before subscribe"
    );

    let params = serde_json::json!({ "uri": uri, "connectionId": conn });
    reg.subscribe(Some(&params)).await.unwrap();
    assert!(
        reg.subscriptions.is_subscribed(conn, uri).await,
        "must be subscribed after subscribe"
    );

    reg.unsubscribe(Some(&params)).await.unwrap();
    assert!(
        !reg.subscriptions.is_subscribed(conn, uri).await,
        "must not be subscribed after unsubscribe"
    );
}

#[tokio::test]
async fn subscribe_multiple_uris_per_connection() {
    let reg = mock_registry();
    let conn = "conn-xyz";
    let uris = [
        "cascade://tier/gci",
        "cascade://tier/asi",
        "cascade://memory/acamarata/decisions.md",
    ];

    for uri in &uris {
        let params = serde_json::json!({ "uri": uri, "connectionId": conn });
        reg.subscribe(Some(&params)).await.unwrap();
    }

    let subs = reg.subscriptions.subscriptions_for(conn).await;
    assert_eq!(subs.len(), 3, "all three URIs must be subscribed");

    let params = serde_json::json!({ "uri": uris[0], "connectionId": conn });
    reg.unsubscribe(Some(&params)).await.unwrap();
    let subs = reg.subscriptions.subscriptions_for(conn).await;
    assert_eq!(subs.len(), 2, "two URIs must remain after one unsubscribe");
}

// ── McpHandler wrappers ─────────────────────────────────────────────────────

#[tokio::test]
async fn handler_wrapper_list_dispatches() {
    let reg = Arc::new(mock_registry());
    let handler = ResourcesListHandler(Arc::clone(&reg));
    let result = handler.handle(None).await.unwrap();
    assert!(
        result["resources"].is_array(),
        "wrapper must return resources array"
    );
}

#[tokio::test]
async fn handler_wrapper_read_dispatches() {
    let reg = Arc::new(mock_registry());
    let handler = ResourcesReadHandler(Arc::clone(&reg));
    let params = serde_json::json!({ "uri": "cascade://tier/gci" });
    let result = handler.handle(Some(params)).await.unwrap();
    assert!(result["contents"].is_array());
}

// ── T-P4-E02-30: new resources ──────────────────────────────────────────────

struct NewResourcesMock {
    quota_content: Option<String>,
}

#[async_trait]
impl ContentBackend for NewResourcesMock {
    async fn read_uri(&self, uri: &str) -> Result<Option<String>, McpServerError> {
        validate_uri_safety(uri)?;
        match uri {
            "cascade://quota_state" => Ok(Some(
                self.quota_content.clone().unwrap_or_else(|| "{}".into()),
            )),
            "cascade://project_state" => Ok(Some(
                r#"{"project":"test","phases_found":1,"active_phases":[]}"#.into(),
            )),
            u if u.starts_with("cascade://instructions/") => {
                let tier = u.trim_start_matches("cascade://instructions/");
                const VALID: &[&str] = &["gci", "pci", "apc", "ppc", "prc", "pac"];
                if !VALID.contains(&tier) {
                    return Err(McpServerError::InvalidParams {
                        detail: format!("unknown tier '{tier}'"),
                    });
                }
                Ok(Some(format!(
                    "# {} instructions\n\nContent for {tier}.",
                    tier.to_uppercase()
                )))
            }
            _ => Err(McpServerError::InvalidParams {
                detail: format!("unknown URI: {uri}"),
            }),
        }
    }
}

fn new_resources_registry(quota: Option<&str>) -> ResourceRegistry {
    ResourceRegistry::with_backend(Arc::new(NewResourcesMock {
        quota_content: quota.map(|s| s.to_string()),
    }))
}

#[tokio::test]
async fn quota_state_returns_json_mime() {
    let reg = new_resources_registry(Some(r#"{"cc":{"quota_hit":false}}"#));
    let params = serde_json::json!({ "uri": "cascade://quota_state" });
    let result = reg.read(Some(&params)).await.unwrap();
    let mime = result["contents"][0]["mimeType"].as_str().unwrap();
    assert_eq!(mime, "application/json");
    let text = result["contents"][0]["text"].as_str().unwrap();
    assert!(text.contains("quota_hit"), "quota content missing");
}

#[tokio::test]
async fn quota_state_returns_empty_object_when_missing() {
    let reg = new_resources_registry(None);
    let params = serde_json::json!({ "uri": "cascade://quota_state" });
    let result = reg.read(Some(&params)).await.unwrap();
    let text = result["contents"][0]["text"].as_str().unwrap();
    assert_eq!(text, "{}", "missing quota-state must return {{}}");
}

#[tokio::test]
async fn project_state_returns_json_mime() {
    let reg = new_resources_registry(None);
    let params = serde_json::json!({ "uri": "cascade://project_state" });
    let result = reg.read(Some(&params)).await.unwrap();
    let mime = result["contents"][0]["mimeType"].as_str().unwrap();
    assert_eq!(mime, "application/json");
    let text = result["contents"][0]["text"].as_str().unwrap();
    serde_json::from_str::<serde_json::Value>(text)
        .expect("project_state must return valid JSON");
}

#[tokio::test]
async fn instructions_ppc_returns_markdown() {
    let reg = new_resources_registry(None);
    let params = serde_json::json!({ "uri": "cascade://instructions/ppc" });
    let result = reg.read(Some(&params)).await.unwrap();
    let mime = result["contents"][0]["mimeType"].as_str().unwrap();
    assert_eq!(mime, "text/markdown");
    let text = result["contents"][0]["text"].as_str().unwrap();
    assert!(text.contains("PPC"), "instructions must mention tier");
}

#[tokio::test]
async fn instructions_invalid_tier_returns_error() {
    let reg = new_resources_registry(None);
    let params = serde_json::json!({ "uri": "cascade://instructions/invalid" });
    let err = reg.read(Some(&params)).await.unwrap_err();
    assert!(
        matches!(err, McpServerError::InvalidParams { .. }),
        "invalid tier must return InvalidParams, got: {err:?}"
    );
}

#[tokio::test]
async fn catalog_contains_new_resource_uris() {
    let reg = new_resources_registry(None);
    let result = reg.list(None).await.unwrap();
    let resources = result["resources"].as_array().unwrap();
    let uris: Vec<&str> = resources
        .iter()
        .filter_map(|r| r.get("uri").and_then(|u| u.as_str()))
        .collect();
    assert!(
        uris.contains(&"cascade://project_state"),
        "catalog must contain project_state"
    );
    assert!(
        uris.contains(&"cascade://quota_state"),
        "catalog must contain quota_state"
    );
    assert!(
        uris.contains(&"cascade://instructions/gci"),
        "catalog must contain instructions/gci"
    );
    assert!(
        uris.contains(&"cascade://instructions/ppc"),
        "catalog must contain instructions/ppc"
    );
}

// ── is_safe_segment ─────────────────────────────────────────────────────────

#[test]
fn safe_segment_rejects_traversal() {
    assert!(!is_safe_segment(".."));
    assert!(!is_safe_segment("."));
    assert!(!is_safe_segment(""));
    assert!(!is_safe_segment("/absolute"));
    assert!(is_safe_segment("gci"));
    assert!(is_safe_segment("decisions.md"));
    assert!(is_safe_segment("MASTER-ROUTES.md"));
}
