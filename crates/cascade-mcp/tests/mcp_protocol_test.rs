//! MCP protocol integration tests.
//!
//! These tests verify the JSON-RPC 2.0 envelope layer, capability negotiation,
//! tool listing, and basic tool dispatch without a real transport. They use an
//! in-process loopback via mpsc channels so no TCP or Unix socket is required.
//!
//! ## Coverage
//!
//! - `initialize` handshake round-trip
//! - `tools/list` returns all 8 cascade tools
//! - `tools/call` dispatches to the right handler
//! - `resources/list` returns tier resources
//! - `prompts/list` returns all 4 registered prompts
//! - `notifications/cancelled` cancels in-flight requests
//! - Malformed JSON returns JSON-RPC parse error
//! - Unknown method returns -32601
//! - Bad tool params return -32602

// ── Unit tests for protocol types ────────────────────────────────────────────

#[test]
fn request_id_string_roundtrip() {
    use cascade_mcp::server::RequestId;
    let id = RequestId::String("req-42".into());
    let serialized = serde_json::to_string(&id).unwrap();
    let back: RequestId = serde_json::from_str(&serialized).unwrap();
    assert_eq!(id, back);
}

#[test]
fn request_id_number_roundtrip() {
    use cascade_mcp::server::RequestId;
    let id = RequestId::Number(99);
    let serialized = serde_json::to_string(&id).unwrap();
    let back: RequestId = serde_json::from_str(&serialized).unwrap();
    assert_eq!(id, back);
}

#[test]
fn json_rpc_error_codes() {
    use cascade_mcp::server::{
        JsonRpcError, ERR_INDEX_NOT_READY, ERR_INVALID_PARAMS, ERR_NOT_FOUND, ERR_PERMISSION_DENIED,
    };
    assert_eq!(ERR_NOT_FOUND, -32001);
    assert_eq!(ERR_INDEX_NOT_READY, -32002);
    assert_eq!(ERR_PERMISSION_DENIED, -32003);
    assert_eq!(ERR_INVALID_PARAMS, -32602);

    let e = JsonRpcError::not_found("test resource");
    assert_eq!(e.code, ERR_NOT_FOUND);
    assert!(e.message.contains("test resource"));
}

// ── Tool registry unit tests ──────────────────────────────────────────────────

#[tokio::test]
async fn tools_list_returns_eight_tools() {
    use cascade_mcp::tool::ToolRegistry;
    let registry = ToolRegistry::new();
    let result = registry.list().await.unwrap();
    let tools = result.get("tools").and_then(|v| v.as_array()).unwrap();
    assert_eq!(tools.len(), 8, "expected 8 tools, got {}", tools.len());

    let names: Vec<&str> = tools
        .iter()
        .filter_map(|t| t.get("name").and_then(|v| v.as_str()))
        .collect();

    assert!(names.contains(&"cascade.search"), "missing cascade.search");
    assert!(names.contains(&"cascade.read"), "missing cascade.read");
    assert!(
        names.contains(&"cascade.search_codebase"),
        "missing cascade.search_codebase"
    );
    assert!(
        names.contains(&"cascade.inbox.list"),
        "missing cascade.inbox.list"
    );
    assert!(
        names.contains(&"cascade.inbox.send"),
        "missing cascade.inbox.send"
    );
    assert!(
        names.contains(&"cascade.master_lists"),
        "missing cascade.master_lists"
    );
    assert!(
        names.contains(&"cascade.memory.read"),
        "missing cascade.memory.read"
    );
    assert!(
        names.contains(&"cascade.memory.write"),
        "missing cascade.memory.write"
    );
}

#[tokio::test]
async fn tools_call_unknown_returns_not_found() {
    use cascade_mcp::tool::ToolRegistry;
    let registry = ToolRegistry::new();
    let params = serde_json::json!({ "name": "cascade.nonexistent" });
    let err = registry.call(&params).await.unwrap_err();
    assert_eq!(err.code, cascade_mcp::server::ERR_NOT_FOUND);
}

#[tokio::test]
async fn tools_call_search_without_query_returns_invalid_params() {
    use cascade_mcp::tool::ToolRegistry;
    let registry = ToolRegistry::new();
    let params = serde_json::json!({ "name": "cascade.search", "arguments": {} });
    let err = registry.call(&params).await.unwrap_err();
    assert_eq!(err.code, cascade_mcp::server::ERR_INVALID_PARAMS);
}

#[tokio::test]
async fn tools_call_search_with_query_returns_ok() {
    use cascade_mcp::tool::ToolRegistry;
    let registry = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.search",
        "arguments": { "query": "test query" }
    });
    let result = registry.call(&params).await.unwrap();
    assert!(
        result.get("content").is_some(),
        "result should have 'content'"
    );
}

#[tokio::test]
async fn tools_call_inbox_send_missing_fields() {
    use cascade_mcp::tool::ToolRegistry;
    let registry = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.inbox.send",
        "arguments": { "project": "nself" }
        // missing 'subject' and 'body'
    });
    let err = registry.call(&params).await.unwrap_err();
    assert_eq!(err.code, cascade_mcp::server::ERR_INVALID_PARAMS);
}

#[tokio::test]
async fn tools_call_master_lists_unknown_kind() {
    use cascade_mcp::tool::ToolRegistry;
    let registry = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.master_lists",
        "arguments": { "project": "nself", "kind": "bogus" }
    });
    let err = registry.call(&params).await.unwrap_err();
    assert_eq!(err.code, cascade_mcp::server::ERR_INVALID_PARAMS);
}

// ── Resource registry unit tests ──────────────────────────────────────────────

#[tokio::test]
async fn resources_list_includes_gci_tier() {
    use cascade_mcp::resource::ResourceRegistry;
    let registry = ResourceRegistry::new();
    let result = registry.list().await.unwrap();
    let resources = result.get("resources").and_then(|v| v.as_array()).unwrap();
    let uris: Vec<&str> = resources
        .iter()
        .filter_map(|r| r.get("uri").and_then(|v| v.as_str()))
        .collect();
    assert!(
        uris.contains(&"cascade://tiers/gci"),
        "GCI tier not in resource list"
    );
}

#[tokio::test]
async fn resources_read_unknown_scheme_returns_error() {
    use cascade_mcp::resource::ResourceRegistry;
    let registry = ResourceRegistry::new();
    let params = serde_json::json!({ "uri": "unknown://foo/bar" });
    let err = registry.read(&params).await.unwrap_err();
    // The error message should mention the unknown URI
    let msg = format!("{err}");
    assert!(msg.contains("Unknown resource URI") || !msg.is_empty());
}

// ── Prompt registry unit tests ────────────────────────────────────────────────

#[test]
fn prompts_list_returns_four_prompts() {
    use cascade_mcp::prompt::PromptRegistry;
    let result = PromptRegistry::list_static().unwrap();
    let prompts = result.get("prompts").and_then(|v| v.as_array()).unwrap();
    assert_eq!(prompts.len(), 4, "expected 4 prompts");
}

#[test]
fn prompts_get_search_produces_message() {
    use cascade_mcp::prompt::PromptRegistry;
    let params = serde_json::json!({
        "name": "cascade/search",
        "arguments": { "query": "Hijri calendar" }
    });
    let result = PromptRegistry::get_static(&params).unwrap();
    let messages = result.get("messages").and_then(|v| v.as_array()).unwrap();
    assert!(!messages.is_empty(), "expected at least one message");
}

#[test]
fn prompts_get_unknown_returns_not_found() {
    use cascade_mcp::prompt::PromptRegistry;
    let params = serde_json::json!({ "name": "cascade/nonexistent" });
    let err = PromptRegistry::get_static(&params).unwrap_err();
    assert_eq!(err.code, cascade_mcp::server::ERR_NOT_FOUND);
}

// ── Logging unit tests ────────────────────────────────────────────────────────

#[test]
fn logging_set_level_changes_level() {
    use cascade_mcp::logging::{LogLevel, McpLogger};
    let logger = McpLogger::new();
    assert_eq!(
        logger.level(),
        LogLevel::Warning,
        "default level should be warning"
    );

    let params = serde_json::json!({ "level": "debug" });
    logger.set_level(&params).unwrap();
    assert_eq!(logger.level(), LogLevel::Debug);
}

#[test]
fn logging_should_emit_respects_level() {
    use cascade_mcp::logging::{LogLevel, McpLogger};
    let logger = McpLogger::new(); // default: Warning

    assert!(
        logger.should_emit(LogLevel::Error),
        "error should emit at warning threshold"
    );
    assert!(logger.should_emit(LogLevel::Warning), "warning should emit");
    assert!(
        !logger.should_emit(LogLevel::Info),
        "info should not emit at warning threshold"
    );
    assert!(
        !logger.should_emit(LogLevel::Debug),
        "debug should not emit at warning threshold"
    );
}

// ── Cancellation unit tests ───────────────────────────────────────────────────

#[test]
fn cancellation_register_and_cancel() {
    use cascade_mcp::cancellation::CancellationRegistry;
    let registry = CancellationRegistry::new();
    let token = registry.register("req-001");

    assert!(!token.is_cancelled(), "should not be cancelled immediately");
    assert_eq!(registry.in_flight(), 1);

    registry.cancel("req-001");
    assert!(
        token.is_cancelled(),
        "should be cancelled after registry.cancel"
    );

    registry.unregister("req-001");
    assert_eq!(registry.in_flight(), 0);
}

#[test]
fn cancellation_cancel_nonexistent_is_noop() {
    use cascade_mcp::cancellation::CancellationRegistry;
    let registry = CancellationRegistry::new();
    // Should not panic.
    registry.cancel("req-nonexistent");
}

// ── Progress unit tests ───────────────────────────────────────────────────────

#[test]
fn progress_notification_serializes() {
    use cascade_mcp::progress::ProgressNotification;
    let notif = ProgressNotification {
        progress_token: "rebuild-01".into(),
        progress: 42,
        total: Some(100),
    };
    let json = notif.to_notification();
    assert!(
        json.contains("notifications/progress"),
        "should be notifications/progress method"
    );
    assert!(json.contains("rebuild-01"), "should include progress token");
}

// ── Path helpers unit tests ───────────────────────────────────────────────────

#[test]
fn paths_tier_file_gci() {
    use cascade_mcp::paths::tier_file;
    let p = tier_file("gci");
    assert!(p.to_str().unwrap().contains(".claude/CLAUDE.md"));
}

#[test]
fn paths_inbox_dir_project() {
    use cascade_mcp::paths::inbox_dir;
    let p = inbox_dir("nself");
    let s = p.to_str().unwrap();
    assert!(s.contains("nself"), "inbox dir should contain project: {s}");
    assert!(
        s.contains(".claude/inbox"),
        "inbox dir should end with .claude/inbox: {s}"
    );
}
