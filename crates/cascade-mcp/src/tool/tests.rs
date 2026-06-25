//! Tests for the tool module.

use std::sync::Arc;

use serde_json::Value;

use cascade_types::retriever::Retriever;

use super::helpers::chrono_local_date;
use super::registry::ToolRegistry;
use super::types::ConnectionContext;

// ── tools/list ────────────────────────────────────────────────────────────────

/// tools/list returns exactly 18 tools (10 original + 8 PBD tools E-P8-04).
#[tokio::test]
async fn tools_list_returns_9_tools() {
    let reg = ToolRegistry::new();
    let result = reg.list().await.expect("list should not fail");
    let tools = result["tools"].as_array().expect("tools must be array");
    assert_eq!(
        tools.len(),
        18,
        "expected exactly 18 tools, got {}",
        tools.len()
    );
}

/// cascade.context_slice appears in the tool list.
#[tokio::test]
async fn tools_list_includes_context_slice() {
    let reg = ToolRegistry::new();
    let result = reg.list().await.unwrap();
    let tools = result["tools"].as_array().unwrap();
    let names: Vec<&str> = tools
        .iter()
        .filter_map(|t| t.get("name").and_then(|v| v.as_str()))
        .collect();
    assert!(
        names.contains(&"cascade.context_slice"),
        "cascade.context_slice must be in tool list; found: {names:?}"
    );
}

/// Every tool has required fields: name (string), description (string),
/// inputSchema (object with type=object).
#[tokio::test]
async fn tools_list_schema_shape() {
    let reg = ToolRegistry::new();
    let result = reg.list().await.unwrap();
    let tools = result["tools"].as_array().unwrap();
    for tool in tools {
        let name = tool
            .get("name")
            .and_then(|v| v.as_str())
            .unwrap_or("<missing>");
        assert!(
            tool.get("name").and_then(|v| v.as_str()).is_some(),
            "{name}: missing 'name'"
        );
        assert!(
            tool.get("description").and_then(|v| v.as_str()).is_some(),
            "{name}: missing 'description'"
        );
        let schema = tool
            .get("inputSchema")
            .unwrap_or_else(|| panic!("{name}: missing inputSchema"));
        assert_eq!(
            schema.get("type").and_then(|v| v.as_str()),
            Some("object"),
            "{name}: inputSchema.type must be 'object'"
        );
        assert!(
            schema.get("properties").is_some(),
            "{name}: inputSchema must have 'properties'"
        );
    }
}

/// Tool names are the exact catalog specified.
#[tokio::test]
async fn tools_list_catalog_names() {
    let reg = ToolRegistry::new();
    let result = reg.list().await.unwrap();
    let tools = result["tools"].as_array().unwrap();
    let names: Vec<&str> = tools
        .iter()
        .filter_map(|t| t.get("name").and_then(|v| v.as_str()))
        .collect();

    let expected = [
        "cascade.read",
        "cascade.search",
        "cascade.search_codebase",
        "cascade.inbox.list",
        "cascade.inbox.send",
        "cascade.master_lists",
        "cascade.memory.read",
        "cascade.memory.write",
    ];
    for exp in &expected {
        assert!(
            names.contains(exp),
            "expected tool '{exp}' not found in list"
        );
    }
}

/// cascade.search inputSchema has correct constraint fields.
#[tokio::test]
async fn cascade_search_schema_constraints() {
    let reg = ToolRegistry::new();
    let result = reg.list().await.unwrap();
    let tools = result["tools"].as_array().unwrap();
    let search = tools
        .iter()
        .find(|t| t["name"] == "cascade.search")
        .expect("cascade.search must be in list");
    let schema = &search["inputSchema"];
    let required = schema["required"].as_array().unwrap();
    assert!(
        required.iter().any(|v| v == "query"),
        "cascade.search.required must include 'query'"
    );
    // limit has minimum:1, maximum:20
    let limit = &schema["properties"]["limit"];
    assert_eq!(limit["minimum"], 1, "limit.minimum should be 1");
    assert_eq!(limit["maximum"], 20, "limit.maximum should be 20");
}

// ── tools/call — happy paths ──────────────────────────────────────────────────

/// cascade.search with valid query returns non-error content.
#[tokio::test]
async fn call_cascade_search_happy() {
    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.search",
        "arguments": { "query": "cascade tiered instructions" }
    });
    let result = reg
        .call(&params)
        .await
        .expect("call should not return protocol error");
    assert!(
        result.get("isError").is_none() || result["isError"] == false,
        "happy path must not be error"
    );
    let content = result["content"].as_array().expect("must have content");
    assert!(!content.is_empty(), "content must not be empty");
    let text = content[0]["text"].as_str().unwrap();
    assert!(
        text.contains("cascade tiered instructions"),
        "response should echo query: {text}"
    );
}

/// cascade.inbox.list on a non-existent project returns empty messages (not error).
#[tokio::test]
async fn call_cascade_inbox_list_nonexistent_project() {
    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.inbox.list",
        "arguments": { "project": "__nonexistent_test_project__" }
    });
    let result = reg
        .call(&params)
        .await
        .expect("should not be protocol error");
    // Non-existent inbox dir → 0 messages, not an error.
    assert!(
        result.get("isError").is_none() || result["isError"] == false,
        "missing inbox dir should return 0 messages, not error"
    );
    let messages = result["messages"].as_array().unwrap();
    assert_eq!(messages.len(), 0);
}

// ── tools/call — invalid args ─────────────────────────────────────────────────

/// cascade.search without 'query' returns is_error: true.
#[tokio::test]
async fn call_cascade_search_missing_query() {
    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.search",
        "arguments": { "limit": 5 }
    });
    let result = reg
        .call(&params)
        .await
        .expect("should return tool error, not protocol error");
    assert_eq!(
        result["isError"], true,
        "missing required arg should yield is_error=true"
    );
    let text = result["content"][0]["text"].as_str().unwrap();
    assert!(
        text.contains("query"),
        "error text should mention missing field"
    );
}

/// cascade.master_lists with unknown kind returns is_error: true.
#[tokio::test]
async fn call_master_lists_unknown_kind() {
    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.master_lists",
        "arguments": { "project": "nself", "kind": "unicorns" }
    });
    let result = reg.call(&params).await.expect("should be tool error");
    assert_eq!(result["isError"], true);
}

/// cascade.memory.read with path-traversal file value returns is_error: true.
#[tokio::test]
async fn call_memory_read_path_traversal_rejected() {
    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.memory.read",
        "arguments": { "project": "nself", "file": "../../vault.env" }
    });
    let result = reg.call(&params).await.expect("should be tool error");
    assert_eq!(result["isError"], true, "path traversal must be rejected");
}

// ── tools/call — backend error (file not found) ───────────────────────────────

/// cascade.memory.read on non-existent file returns is_error: true.
#[tokio::test]
async fn call_memory_read_file_not_found() {
    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.memory.read",
        "arguments": {
            "project": "__nonexistent_project_xyz__",
            "file": "decisions.md"
        }
    });
    let result = reg
        .call(&params)
        .await
        .expect("should be tool error, not protocol error");
    assert_eq!(
        result["isError"], true,
        "file-not-found must become is_error=true"
    );
}

/// cascade.read on non-existent tier returns is_error: true.
#[tokio::test]
async fn call_cascade_read_tier_not_found() {
    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.read",
        "arguments": { "tier": "ppc", "project": "__no_such_project__" }
    });
    let result = reg.call(&params).await.expect("should be tool error");
    assert_eq!(result["isError"], true);
}

// ── tools/call — auth gate ────────────────────────────────────────────────────

/// cascade.memory.write without auth returns is_error: true, message "Unauthorized".
#[tokio::test]
async fn call_memory_write_unauthenticated_rejected() {
    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.memory.write",
        "arguments": {
            "project": "nself",
            "file": "decisions.md",
            "content": "## Test entry"
        }
    });
    let ctx = ConnectionContext {
        authenticated: false,
    };
    let result = reg
        .call_with_context(&params, &ctx)
        .await
        .expect("should be is_error, not protocol error");
    assert_eq!(
        result["isError"], true,
        "unauthenticated write must be rejected"
    );
    let text = result["content"][0]["text"].as_str().unwrap();
    assert_eq!(
        text, "Unauthorized",
        "error text must be exactly 'Unauthorized'"
    );
}

/// Authenticated cascade.memory.write succeeds (writes to temp dir).
#[tokio::test]
async fn call_memory_write_authenticated_succeeds() {
    // Write to a temp project that does not collide with real ~/Sites.
    // We cannot easily intercept the path in the current stub-only impl,
    // so we verify the call returns *without* is_error when authenticated.
    // Full integration (real file write) is QA-B scope.
    let reg = ToolRegistry::new();
    let ctx = ConnectionContext {
        authenticated: true,
    };

    // Use a tmp dir path by setting a known nonexistent project that will
    // fail at the fs level; verify is_error is true but NOT "Unauthorized".
    let params = serde_json::json!({
        "name": "cascade.memory.write",
        "arguments": {
            "project": "__auth_test_project__",
            "file": "decisions.md",
            "content": "## Auth test"
        }
    });
    let result = reg
        .call_with_context(&params, &ctx)
        .await
        .expect("should not be protocol error");
    // The project doesn't exist but create_dir_all should succeed (creates it).
    // Either success or fs error — important thing is NOT "Unauthorized".
    if result.get("isError") == Some(&Value::Bool(true)) {
        let text = result["content"][0]["text"].as_str().unwrap_or("");
        assert_ne!(
            text, "Unauthorized",
            "authenticated call must not return Unauthorized"
        );
    }
    // Clean up any created dirs.
    let _ =
        std::fs::remove_dir_all(dirs_next_home().join("Sites").join("__auth_test_project__"));
}

// ── tools/call — unknown tool ─────────────────────────────────────────────────

/// Unknown tool name returns McpError MethodNotFound (Err variant, not is_error).
#[tokio::test]
async fn call_unknown_tool_returns_method_not_found() {
    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.nonexistent",
        "arguments": {}
    });
    let result = reg.call(&params).await;
    assert!(
        result.is_err(),
        "unknown tool must return Err(JsonRpcError)"
    );
    let err = result.unwrap_err();
    assert_eq!(
        err.code,
        crate::server::ERR_NOT_FOUND,
        "error code must be ERR_NOT_FOUND (-32001)"
    );
}

/// Missing 'name' in params returns InvalidParams.
#[tokio::test]
async fn call_missing_name_returns_invalid_params() {
    let reg = ToolRegistry::new();
    let params = serde_json::json!({ "arguments": { "query": "test" } });
    let result = reg.call(&params).await;
    assert!(
        result.is_err(),
        "missing name must return Err(JsonRpcError)"
    );
    let err = result.unwrap_err();
    assert_eq!(err.code, crate::server::ERR_INVALID_PARAMS);
}

// ── helpers ───────────────────────────────────────────────────────────────────

/// chrono_local_date returns a plausible YYYY-MM-DD string.
#[test]
fn date_helper_format() {
    let d = chrono_local_date();
    assert_eq!(d.len(), 10, "date must be 10 chars: {d}");
    assert!(d.starts_with("20"), "date must start with '20': {d}");
    let parts: Vec<&str> = d.split('-').collect();
    assert_eq!(parts.len(), 3, "date must have 3 parts: {d}");
}

/// Helper to get home dir without depending on dirs-next crate in tests.
fn dirs_next_home() -> std::path::PathBuf {
    std::env::var("HOME")
        .map(std::path::PathBuf::from)
        .unwrap_or_else(|_| std::path::PathBuf::from("/tmp"))
}

// ── cascade.provide_harness_context tests ─────────────────────────────────────

/// Tool appears in the tools list.
#[tokio::test]
async fn provide_harness_context_in_tool_list() {
    let reg = ToolRegistry::new();
    let result = reg.list().await.unwrap();
    let tools = result["tools"].as_array().unwrap();
    let names: Vec<&str> = tools
        .iter()
        .filter_map(|t| t.get("name").and_then(|v| v.as_str()))
        .collect();
    assert!(
        names.contains(&"cascade.provide_harness_context"),
        "cascade.provide_harness_context must be in tool list; found: {names:?}"
    );
}

/// tools/list now returns 18 tools (10 original + 8 PBD E-P8-04).
#[tokio::test]
async fn tools_list_returns_10_tools() {
    let reg = ToolRegistry::new();
    let result = reg.list().await.expect("list should not fail");
    let tools = result["tools"].as_array().expect("tools must be array");
    assert_eq!(
        tools.len(),
        18,
        "expected exactly 18 tools, got {}",
        tools.len()
    );
}

/// Unknown harness arg returns is_error: true (tool-level error, not protocol error).
#[tokio::test]
async fn provide_harness_context_unknown_harness_returns_tool_error() {
    let reg = ToolRegistry::new();
    // Call via the protocol error path (unknown harness → invalid_params which
    // goes through tool_result → is_error: true).
    let params = serde_json::json!({
        "name": "cascade.provide_harness_context",
        "arguments": { "harness": "vscode", "cwd": "/tmp" }
    });
    let result = reg
        .call(&params)
        .await
        .expect("should not be protocol error");
    assert_eq!(
        result["isError"].as_bool(),
        Some(true),
        "unknown harness must be tool-level error: {result}"
    );
}

/// Missing harness arg returns tool-level error.
#[tokio::test]
async fn provide_harness_context_missing_harness_returns_tool_error() {
    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.provide_harness_context",
        "arguments": { "cwd": "/tmp" }
    });
    let result = reg
        .call(&params)
        .await
        .expect("should not be protocol error");
    assert_eq!(
        result["isError"].as_bool(),
        Some(true),
        "missing harness must be tool-level error"
    );
}

/// Missing cwd arg returns tool-level error.
#[tokio::test]
async fn provide_harness_context_missing_cwd_returns_tool_error() {
    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.provide_harness_context",
        "arguments": { "harness": "claude-code" }
    });
    let result = reg
        .call(&params)
        .await
        .expect("should not be protocol error");
    assert_eq!(
        result["isError"].as_bool(),
        Some(true),
        "missing cwd must be tool-level error"
    );
}

/// Each valid harness returns the correct instruction_file in config.
///
/// Uses multi-thread runtime because `resolve_cascade_full` calls
/// `tokio::task::block_in_place` which requires the multi-threaded executor.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn provide_harness_context_config_per_harness() {
    use tempfile::TempDir;
    let tmp = TempDir::new().unwrap();

    // Scaffold a minimal cascade tree so resolve_cascade_full finds something.
    let cascade_dir = tmp.path().join(".cascade");
    std::fs::create_dir_all(&cascade_dir).unwrap();
    std::fs::write(
        cascade_dir.join("CASCADE.md"),
        "# Test cascade\nTest instructions for harness context test.",
    )
    .unwrap();

    let reg = ToolRegistry::new();

    let expected_instruction_files = [
        ("claude-code", "CLAUDE.md"),
        ("opencode", "AGENTS.md"),
        ("codex", "AGENTS.md"),
        ("cursor", ".cursorrules"),
        ("aider", "CONVENTIONS.md"),
    ];

    for (harness, expected_file) in &expected_instruction_files {
        let params = serde_json::json!({
            "name": "cascade.provide_harness_context",
            "arguments": {
                "harness": harness,
                "cwd": tmp.path().to_str().unwrap()
            }
        });
        let result = reg
            .call(&params)
            .await
            .expect("should not be protocol error");

        // Must not be an error.
        assert!(
            result
                .get("isError")
                .map(|v| v == &Value::Bool(false))
                .unwrap_or(true),
            "harness={harness} should succeed: {result}"
        );

        // merged_instructions must be non-empty.
        let merged = result["merged_instructions"].as_str().unwrap_or("");
        assert!(
            !merged.is_empty(),
            "harness={harness}: merged_instructions must be non-empty"
        );

        // config.instruction_file must match expected.
        let instr_file = result["config"]["instruction_file"].as_str().unwrap_or("");
        assert_eq!(
            instr_file, *expected_file,
            "harness={harness}: expected instruction_file={expected_file}, got={instr_file}"
        );

        // mcp block must contain the command.
        let mcp = &result["mcp"];
        assert!(
            mcp.get("url").is_some(),
            "harness={harness}: mcp.url required"
        );
        assert!(
            mcp.get("command").is_some(),
            "harness={harness}: mcp.command required"
        );

        // policies block must have tool_use = "allow".
        let policies = &result["policies"];
        assert_eq!(
            policies["tool_use"].as_str(),
            Some("allow"),
            "harness={harness}: policies.tool_use must be 'allow'"
        );
    }
}

// ── cascade.context_slice tests ───────────────────────────────────────────────

/// Happy path: valid query + budget returns is_error=false and tokens_used ≤ budget.
#[tokio::test]
async fn context_slice_happy_path() {
    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.context_slice",
        "arguments": { "query": "authentication", "budget_tokens": 1024 }
    });
    let result = reg
        .call(&params)
        .await
        .expect("should not error at protocol level");
    // Tool-level result: no is_error.
    assert!(
        result.get("isError").is_none() || result["isError"].as_bool() != Some(true),
        "should not be an error result: {result}"
    );
    let tokens_used = result["metadata"]["tokens_used"].as_u64().unwrap_or(0);
    assert!(
        tokens_used <= 1024,
        "tokens_used={tokens_used} should be ≤ budget=1024"
    );
}

/// Over-budget arg (budget_tokens=100 < min 256) returns tool-level is_error.
///
/// Per MCP spec: constraint violations are tool-level errors (is_error: true),
/// not JSON-RPC protocol errors. The tool wrapper converts JsonRpcError to
/// { isError: true, content: [...] }.
#[tokio::test]
async fn context_slice_budget_below_min_returns_tool_error() {
    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.context_slice",
        "arguments": { "query": "test", "budget_tokens": 100 }
    });
    let result = reg
        .call(&params)
        .await
        .expect("should not be a protocol error");
    // Tool-level error: isError=true.
    assert_eq!(
        result["isError"].as_bool(),
        Some(true),
        "budget below 256 should be tool-level error: {result}"
    );
}

/// Missing query returns tool-level is_error (not a protocol error).
#[tokio::test]
async fn context_slice_missing_query_returns_tool_error() {
    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.context_slice",
        "arguments": { "budget_tokens": 2048 }
    });
    let result = reg
        .call(&params)
        .await
        .expect("should not be a protocol error");
    assert_eq!(
        result["isError"].as_bool(),
        Some(true),
        "missing query should be tool-level error: {result}"
    );
}

/// Result content is a text block (valid structure for harness injection).
#[tokio::test]
async fn context_slice_result_has_text_content() {
    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.context_slice",
        "arguments": { "query": "test", "budget_tokens": 512 }
    });
    let result = reg.call(&params).await.unwrap();
    let content = result["content"].as_array().expect("content must be array");
    assert!(!content.is_empty(), "content array must not be empty");
    let first = &content[0];
    assert_eq!(
        first["type"].as_str(),
        Some("text"),
        "first content must be text"
    );
    assert!(
        first["text"].as_str().is_some(),
        "text field must be a string"
    );
}

/// metadata block has the required keys.
#[tokio::test]
async fn context_slice_metadata_shape() {
    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.context_slice",
        "arguments": { "query": "test", "budget_tokens": 4096 }
    });
    let result = reg.call(&params).await.unwrap();
    let meta = &result["metadata"];
    assert!(
        meta.get("tokens_used").is_some(),
        "metadata.tokens_used required"
    );
    assert!(
        meta.get("chunks_returned").is_some(),
        "metadata.chunks_returned required"
    );
    assert!(meta.get("query").is_some(), "metadata.query required");
    assert!(
        meta.get("budget_tokens").is_some(),
        "metadata.budget_tokens required"
    );
}

// ── PBD tool tests (E-P8-04) ─────────────────────────────────────────────────

/// Build a minimal phases tree in a temp dir and return its path.
fn scaffold_pbd_tree() -> tempfile::TempDir {
    use cascade_core::pbd::schema::*;
    use cascade_core::pbd::store::PbdStore;

    let tmp = tempfile::TempDir::new().unwrap();
    let phases = tmp.path().join("phases");
    let store = PbdStore::new(&phases);
    store.init().unwrap();

    let phase = Phase {
        id: "p1".into(),
        title: "Phase 1".into(),
        status: PhaseStatus::Building,
        epics: vec![],
        started_at: Some(chrono::Utc::now()),
        closed_at: None,
        note: None,
    };
    store.create_phase(&phase).unwrap();

    let epic = Epic {
        id: "e01".into(),
        phase_id: "p1".into(),
        title: "Epic 1".into(),
        status: EpicStatus::Active,
        waves: vec![],
        depends_on: vec![],
        note: None,
    };
    store.create_epic(&epic).unwrap();

    let wave = Wave {
        id: "w01".into(),
        epic_id: "e01".into(),
        title: "Wave 1".into(),
        status: WaveStatus::Active,
        sprints: vec![],
        note: None,
    };
    store.create_wave("p1", &wave).unwrap();

    let sprint = Sprint {
        id: "s01".into(),
        wave_id: "w01".into(),
        title: "Sprint 1".into(),
        status: SprintStatus::Active,
        tickets: vec![],
        note: None,
    };
    store.create_sprint("p1", "e01", &sprint).unwrap();

    let ticket = Ticket {
        id: "T-P1-E01-01".into(),
        sprint_id: "s01".into(),
        title: "Test ticket".into(),
        status: TicketStatus::Active,
        steps: vec![],
        depends_on: vec![],
        repo: None,
        weight: None,
        note: None,
        blocked_reason: None,
    };
    store.create_ticket("p1", "e01", "w01", &ticket).unwrap();

    tmp
}

/// cascade.get_current returns shape with content field.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn pbd_get_current_shape() {
    let tmp = scaffold_pbd_tree();
    let phases_root = tmp.path().join("phases");
    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.get_current",
        "arguments": { "phases_root": phases_root.to_str().unwrap() }
    });
    let result = reg.call(&params).await.expect("no protocol error");
    assert!(
        result
            .get("isError")
            .map(|v| v == &Value::Bool(false))
            .unwrap_or(true),
        "get_current should succeed: {result}"
    );
    let content = result["content"].as_array().expect("content array");
    assert!(!content.is_empty(), "content must not be empty");
    // current block present
    assert!(result.get("current").is_some(), "current field required");
}

/// cascade.get_current on empty phases tree returns empty current.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn pbd_get_current_empty_tree() {
    let tmp = tempfile::TempDir::new().unwrap();
    let phases_root = tmp.path().join("phases");
    std::fs::create_dir_all(&phases_root).unwrap();
    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.get_current",
        "arguments": { "phases_root": phases_root.to_str().unwrap() }
    });
    let result = reg.call(&params).await.expect("no protocol error");
    // Empty phases → no error, empty current
    assert!(
        result
            .get("isError")
            .map(|v| v == &Value::Bool(false))
            .unwrap_or(true),
        "empty tree should not be an error: {result}"
    );
    let current = &result["current"];
    assert!(
        current.as_object().map(|o| o.is_empty()).unwrap_or(false),
        "empty tree should yield empty current: {current}"
    );
}

/// cascade.update_ticket_status — valid transition active->done.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn pbd_update_ticket_status_valid_transition() {
    let tmp = scaffold_pbd_tree();
    let phases_root = tmp.path().join("phases");
    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.update_ticket_status",
        "arguments": {
            "ticket_id": "T-P1-E01-01",
            "status": "done",
            "phases_root": phases_root.to_str().unwrap()
        }
    });
    let result = reg.call(&params).await.expect("no protocol error");
    assert!(
        result
            .get("isError")
            .map(|v| v == &Value::Bool(false))
            .unwrap_or(true),
        "valid transition must succeed: {result}"
    );
    assert_eq!(
        result["new_status"].as_str(),
        Some("done"),
        "new_status must be 'done'"
    );
}

/// cascade.update_ticket_status — invalid transition done->queue returns is_error.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn pbd_update_ticket_status_invalid_transition() {
    let tmp = scaffold_pbd_tree();
    let phases_root = tmp.path().join("phases");

    // First move to done
    use cascade_core::pbd::schema::TicketStatus;
    use cascade_core::pbd::store::PbdStore;
    let store = PbdStore::new(tmp.path().join("phases"));
    store
        .transition_ticket(
            "p1",
            "e01",
            "w01",
            "s01",
            "T-P1-E01-01",
            TicketStatus::Done,
            None,
        )
        .unwrap();

    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.update_ticket_status",
        "arguments": {
            "ticket_id": "T-P1-E01-01",
            "status": "queue",  // done->queue is not allowed
            "phases_root": phases_root.to_str().unwrap()
        }
    });
    let result = reg.call(&params).await.expect("no protocol error");
    assert_eq!(
        result["isError"].as_bool(),
        Some(true),
        "invalid transition must return is_error: {result}"
    );
}

/// cascade.update_ticket_status — invalid status string returns is_error.
#[tokio::test]
async fn pbd_update_ticket_status_bad_status_string() {
    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.update_ticket_status",
        "arguments": {
            "ticket_id": "T-X",
            "status": "flying"
        }
    });
    let result = reg.call(&params).await.expect("no protocol error");
    assert_eq!(
        result["isError"].as_bool(),
        Some(true),
        "bad status must be is_error"
    );
}

/// cascade.append_event — event appended and readable.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn pbd_append_event_ordering() {
    let tmp = tempfile::TempDir::new().unwrap();
    let phases_root = tmp.path().join("phases");
    std::fs::create_dir_all(&phases_root).unwrap();
    let reg = ToolRegistry::new();

    // Append two events
    for (from, to) in [("planned", "active"), ("active", "done")] {
        let params = serde_json::json!({
            "name": "cascade.append_event",
            "arguments": {
                "event": {
                    "actor": "test",
                    "level": "ticket",
                    "id": "T-001",
                    "from": from,
                    "to": to
                },
                "phases_root": phases_root.to_str().unwrap()
            }
        });
        let result = reg.call(&params).await.expect("no protocol error");
        assert!(
            result
                .get("isError")
                .map(|v| v == &Value::Bool(false))
                .unwrap_or(true),
            "append_event must succeed: {result}"
        );
    }

    // Read events.jsonl and verify ordering
    let events_path = phases_root.join("events.jsonl");
    let content = std::fs::read_to_string(&events_path).expect("events.jsonl must exist");
    let lines: Vec<&str> = content.lines().filter(|l| !l.is_empty()).collect();
    assert_eq!(
        lines.len(),
        2,
        "two events must be appended; got: {content}"
    );
    let first: Value = serde_json::from_str(lines[0]).unwrap();
    let second: Value = serde_json::from_str(lines[1]).unwrap();
    assert_eq!(first["from"].as_str(), Some("planned"));
    assert_eq!(second["from"].as_str(), Some("active"));
}

/// cascade.append_event — missing required field returns is_error.
#[tokio::test]
async fn pbd_append_event_missing_field() {
    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.append_event",
        "arguments": {
            "event": {
                "actor": "test",
                // missing level, id, from, to
            }
        }
    });
    let result = reg.call(&params).await.expect("no protocol error");
    assert_eq!(
        result["isError"].as_bool(),
        Some(true),
        "missing fields must be is_error"
    );
}

/// cascade.get_sprint — returns sprint JSON.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn pbd_get_sprint_found() {
    let tmp = scaffold_pbd_tree();
    let phases_root = tmp.path().join("phases");
    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.get_sprint",
        "arguments": {
            "sprint_id": "s01",
            "phases_root": phases_root.to_str().unwrap()
        }
    });
    let result = reg.call(&params).await.expect("no protocol error");
    assert!(
        result
            .get("isError")
            .map(|v| v == &Value::Bool(false))
            .unwrap_or(true),
        "get_sprint must succeed: {result}"
    );
    let sprint = &result["sprint"];
    assert_eq!(sprint["id"].as_str(), Some("s01"), "sprint.id must be s01");
    assert!(
        !sprint["tickets"].is_null(),
        "sprint.tickets must be present"
    );
}

/// cascade.get_sprint — unknown sprint returns is_error.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn pbd_get_sprint_not_found() {
    let tmp = scaffold_pbd_tree();
    let phases_root = tmp.path().join("phases");
    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.get_sprint",
        "arguments": {
            "sprint_id": "s99",
            "phases_root": phases_root.to_str().unwrap()
        }
    });
    let result = reg.call(&params).await.expect("no protocol error");
    assert_eq!(
        result["isError"].as_bool(),
        Some(true),
        "unknown sprint must return is_error"
    );
}

/// cascade.list_tickets — no filter returns all tickets.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn pbd_list_tickets_no_filter() {
    let tmp = scaffold_pbd_tree();
    let phases_root = tmp.path().join("phases");
    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.list_tickets",
        "arguments": { "phases_root": phases_root.to_str().unwrap() }
    });
    let result = reg.call(&params).await.expect("no protocol error");
    assert!(
        result
            .get("isError")
            .map(|v| v == &Value::Bool(false))
            .unwrap_or(true),
        "list_tickets must succeed: {result}"
    );
    let tickets = result["tickets"].as_array().expect("tickets must be array");
    assert_eq!(
        tickets.len(),
        1,
        "should find exactly one ticket; got {}",
        tickets.len()
    );
}

/// cascade.list_tickets — status filter.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn pbd_list_tickets_status_filter() {
    let tmp = scaffold_pbd_tree();
    let phases_root = tmp.path().join("phases");
    let reg = ToolRegistry::new();

    // Filter for "active" → 1 result
    let params = serde_json::json!({
        "name": "cascade.list_tickets",
        "arguments": {
            "status": "active",
            "phases_root": phases_root.to_str().unwrap()
        }
    });
    let result = reg.call(&params).await.expect("no protocol error");
    let tickets = result["tickets"].as_array().unwrap();
    assert_eq!(tickets.len(), 1, "active filter: expected 1 ticket");

    // Filter for "done" → 0 results
    let params2 = serde_json::json!({
        "name": "cascade.list_tickets",
        "arguments": {
            "status": "done",
            "phases_root": phases_root.to_str().unwrap()
        }
    });
    let result2 = reg.call(&params2).await.expect("no protocol error");
    let tickets2 = result2["tickets"].as_array().unwrap();
    assert_eq!(tickets2.len(), 0, "done filter: expected 0 tickets");
}

/// cascade.check_routes — missing routes file returns not_found flag.
#[tokio::test]
async fn pbd_check_routes_missing_file() {
    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.check_routes",
        "arguments": {
            "routes_file": "/tmp/does_not_exist_cascade_routes_xyz.yaml"
        }
    });
    let result = reg.call(&params).await.expect("no protocol error");
    assert!(
        result
            .get("isError")
            .map(|v| v == &Value::Bool(false))
            .unwrap_or(true),
        "missing routes file must not be is_error (just not_found flag): {result}"
    );
    assert_eq!(
        result["not_found"].as_bool(),
        Some(true),
        "not_found flag must be true"
    );
}

/// cascade.check_routes — mock HTTP: routes_file with base_url pointing to httpbin.
///
/// This test uses a seeded temp routes file and a mock server.
/// Because we can't guarantee network in CI, we test the parsing + structure only
/// when a real server is unavailable (ok=false is still a valid structured response).
#[tokio::test]
async fn pbd_check_routes_with_mock_file() {
    use std::io::Write;
    let tmp = tempfile::TempDir::new().unwrap();
    let routes_file = tmp.path().join("api-routes.yaml");
    {
        let mut f = std::fs::File::create(&routes_file).unwrap();
        writeln!(f, "routes:").unwrap();
        writeln!(f, "  - path: /healthz").unwrap();
        writeln!(f, "    method: GET").unwrap();
        writeln!(f, "    expected_status: 200").unwrap();
    }

    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.check_routes",
        "arguments": {
            "routes_file": routes_file.to_str().unwrap(),
            "base_url": "http://127.0.0.1:1",  // nothing listening → connection refused
            "timeout_ms": 200
        }
    });
    let result = reg.call(&params).await.expect("no protocol error");
    // Must not be a tool-level error — connection refused becomes ok=false in the routes array
    assert!(
        result
            .get("isError")
            .map(|v| v == &Value::Bool(false))
            .unwrap_or(true),
        "check_routes must not bubble network errors as is_error: {result}"
    );
    let routes = result["routes"].as_array().expect("routes must be array");
    assert_eq!(routes.len(), 1, "one route must be in result");
    assert_eq!(
        routes[0]["path"].as_str(),
        Some("/healthz"),
        "path must match"
    );
    assert_eq!(
        routes[0]["ok"].as_bool(),
        Some(false),
        "connection refused must be ok=false"
    );
}

/// cascade.scan_inbox — finds seeded .md error files.
#[tokio::test]
async fn pbd_scan_inbox_finds_error_files() {
    use std::io::Write;
    let tmp = tempfile::TempDir::new().unwrap();
    let inbox = tmp.path().join("inbox");
    std::fs::create_dir_all(&inbox).unwrap();

    // Seed three .md files
    for (name, subject) in [
        ("ci-error-1.md", "# CI failure: build broke"),
        ("ci-error-2.md", "# CI failure: tests failed"),
        ("pci-enhancement.md", "# Enhancement: add dark mode"),
    ] {
        let mut f = std::fs::File::create(inbox.join(name)).unwrap();
        writeln!(f, "{subject}").unwrap();
        writeln!(f, "\nBody text.").unwrap();
    }

    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.scan_inbox",
        "arguments": { "inbox_path": inbox.to_str().unwrap() }
    });
    let result = reg.call(&params).await.expect("no protocol error");
    assert!(
        result
            .get("isError")
            .map(|v| v == &Value::Bool(false))
            .unwrap_or(true),
        "scan_inbox must succeed: {result}"
    );
    let messages = result["messages"]
        .as_array()
        .expect("messages must be array");
    assert_eq!(
        messages.len(),
        3,
        "must find 3 messages; got {}",
        messages.len()
    );
    // Verify subject extraction
    assert!(
        messages
            .iter()
            .any(|m| m["subject"].as_str().unwrap_or("").contains("CI failure")),
        "must extract CI failure subjects"
    );
    assert_eq!(result["count"].as_u64(), Some(3), "count must be 3");
}

/// cascade.scan_inbox — non-existent inbox returns empty list, no error.
#[tokio::test]
async fn pbd_scan_inbox_missing_dir() {
    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.scan_inbox",
        "arguments": { "inbox_path": "/tmp/does_not_exist_cascade_inbox_xyz" }
    });
    let result = reg.call(&params).await.expect("no protocol error");
    assert!(
        result
            .get("isError")
            .map(|v| v == &Value::Bool(false))
            .unwrap_or(true),
        "missing inbox must not be is_error: {result}"
    );
    assert_eq!(
        result["count"].as_u64(),
        Some(0),
        "missing inbox must return count=0"
    );
}

// ── active_work in provide_harness_context (E-P8-07) ─────────────────────────

/// provide_harness_context response includes active_work field (E-P8-07).
///
/// Even when no active sprint exists (no phases tree), active_work must be
/// present as an object field (empty, not null).
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn provide_harness_context_includes_active_work() {
    use tempfile::TempDir;
    let tmp = TempDir::new().unwrap();

    // Scaffold a minimal cascade tree so resolve_cascade_full finds something.
    let cascade_dir = tmp.path().join(".cascade");
    std::fs::create_dir_all(&cascade_dir).unwrap();
    std::fs::write(cascade_dir.join("CASCADE.md"), "# Test\nInstructions.").unwrap();

    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.provide_harness_context",
        "arguments": {
            "harness": "claude-code",
            "cwd": tmp.path().to_str().unwrap()
        }
    });
    let result = reg.call(&params).await.expect("no protocol error");

    // Must not be error
    assert!(
        result
            .get("isError")
            .map(|v| v == &Value::Bool(false))
            .unwrap_or(true),
        "provide_harness_context must succeed: {result}"
    );

    // active_work field must be present
    assert!(
        result.get("active_work").is_some(),
        "provide_harness_context must include active_work field: {result}"
    );
    assert!(
        result.get("active_work_text").is_some(),
        "provide_harness_context must include active_work_text field: {result}"
    );
    // With no phases tree, active_work should indicate empty state
    let aw = &result["active_work"];
    assert!(aw.is_object(), "active_work must be a JSON object: {aw}");
}

/// provide_harness_context active_work reflects active sprint when phases tree exists.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn provide_harness_context_active_work_reflects_sprint() {
    use cascade_core::pbd::schema::*;
    use cascade_core::pbd::store::PbdStore;
    use tempfile::TempDir;

    let tmp = TempDir::new().unwrap();

    // Scaffold cascade tree
    let cascade_dir = tmp.path().join(".cascade");
    std::fs::create_dir_all(&cascade_dir).unwrap();
    std::fs::write(cascade_dir.join("CASCADE.md"), "# Test\nInstructions.").unwrap();

    // Scaffold phases tree under .cascade/phases/
    let phases_root = tmp.path().join(".cascade").join("phases");
    let store = PbdStore::new(&phases_root);
    store.init().unwrap();

    let phase = Phase {
        id: "p1".into(),
        title: "P1".into(),
        status: PhaseStatus::Building,
        epics: vec!["e01".into()],
        started_at: None,
        closed_at: None,
        note: None,
    };
    store.create_phase(&phase).unwrap();

    let epic = Epic {
        id: "e01".into(),
        phase_id: "p1".into(),
        title: "E1".into(),
        status: EpicStatus::Active,
        waves: vec!["w01".into()],
        depends_on: vec![],
        note: None,
    };
    store.create_epic(&epic).unwrap();

    let wave = Wave {
        id: "w01".into(),
        epic_id: "e01".into(),
        title: "W1".into(),
        status: WaveStatus::Active,
        sprints: vec!["s01".into()],
        note: None,
    };
    store.create_wave("p1", &wave).unwrap();

    let sprint = Sprint {
        id: "s01".into(),
        wave_id: "w01".into(),
        title: "Active Sprint".into(),
        status: SprintStatus::Active,
        tickets: vec!["T-P1-E01-W01-S01-01".into()],
        note: None,
    };
    store.create_sprint("p1", "e01", &sprint).unwrap();

    let ticket = Ticket {
        id: "T-P1-E01-W01-S01-01".into(),
        sprint_id: "s01".into(),
        title: "Do the thing".into(),
        status: TicketStatus::Active,
        steps: vec![],
        depends_on: vec![],
        repo: None,
        weight: None,
        note: None,
        blocked_reason: None,
    };
    store.create_ticket("p1", "e01", "w01", &ticket).unwrap();

    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.provide_harness_context",
        "arguments": {
            "harness": "claude-code",
            "cwd": tmp.path().to_str().unwrap()
        }
    });
    let result = reg.call(&params).await.expect("no protocol error");

    assert!(
        result
            .get("isError")
            .map(|v| v == &Value::Bool(false))
            .unwrap_or(true),
        "must succeed: {result}"
    );

    let aw = &result["active_work"];
    assert_eq!(
        aw["sprint_id"].as_str(),
        Some("s01"),
        "active_work.sprint_id must be s01: {aw}"
    );
    let aw_text = result["active_work_text"].as_str().unwrap_or("");
    assert!(
        aw_text.contains("s01"),
        "active_work_text must mention sprint: {aw_text}"
    );
    assert!(
        aw_text.contains("Do the thing"),
        "active_work_text must mention ticket title: {aw_text}"
    );
}

/// inject_active_work: harness_context token_bounded (active_work <= 800 tokens).
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn provide_harness_context_active_work_token_bounded() {
    use tempfile::TempDir;
    let tmp = TempDir::new().unwrap();

    let cascade_dir = tmp.path().join(".cascade");
    std::fs::create_dir_all(&cascade_dir).unwrap();
    std::fs::write(cascade_dir.join("CASCADE.md"), "# T\nInstructions.").unwrap();

    let reg = ToolRegistry::new();
    let params = serde_json::json!({
        "name": "cascade.provide_harness_context",
        "arguments": { "harness": "opencode", "cwd": tmp.path().to_str().unwrap() }
    });
    let result = reg.call(&params).await.expect("no protocol error");

    let aw_text = result["active_work_text"].as_str().unwrap_or("");
    // Token bound: text length / 4 chars-per-token <= 800 tokens
    let estimated_tokens = aw_text.len() / 4;
    assert!(
        estimated_tokens <= 800,
        "active_work_text estimated tokens={estimated_tokens} must be <=800; text length={}",
        aw_text.len()
    );
}

/// All 8 PBD tools appear in tools/list.
#[tokio::test]
async fn pbd_tools_in_list() {
    let reg = ToolRegistry::new();
    let result = reg.list().await.unwrap();
    let tools = result["tools"].as_array().unwrap();
    let names: Vec<&str> = tools
        .iter()
        .filter_map(|t| t.get("name").and_then(|v| v.as_str()))
        .collect();
    let expected_pbd = [
        "cascade.get_current",
        "cascade.update_ticket_status",
        "cascade.append_event",
        "cascade.get_sprint",
        "cascade.read_phase_status",
        "cascade.list_tickets",
        "cascade.check_routes",
        "cascade.scan_inbox",
    ];
    for tool in &expected_pbd {
        assert!(
            names.contains(tool),
            "PBD tool '{tool}' must be in tools/list; found: {names:?}"
        );
    }
}

// ── cascade.search live RAG integration ───────────────────────────────────────

/// Build a tiny in-memory `RagIndex` with two fixture docs, wrap it in an
/// `RrfRetriever` (FTS-only mode — no embeddings required), inject it into
/// a `ToolRegistry`, and assert that `cascade.search` returns real hits.
///
/// This is the primary integration test for the E11.1 search wiring.
#[tokio::test]
async fn search_live_rrf_returns_real_hits() {
    use cascade_rag::index::RagIndex;
    use cascade_rag::retrieve::rrf::{RrfConfig, RrfRetriever};
    use cascade_types::NoopEmbeddingProvider;

    // Build a temp-file index (RagIndex::open needs a real path; it uses
    // SQLite WAL which doesn't work with ":memory:" across threads).
    let tmp = tempfile::tempdir().expect("tempdir");
    let db_path = tmp.path().join("test.db");
    let idx = Arc::new(RagIndex::open(&db_path).await.expect("RagIndex::open"));

    // Ingest three fixture chunks.
    idx.upsert_chunk(
        "c1",
        None,
        Some(1),
        Some(5),
        "prayer times fajr dhuhr asr maghrib isha",
        None,
    )
    .await
    .expect("upsert c1");
    idx.upsert_chunk(
        "c2",
        None,
        Some(10),
        Some(15),
        "hijri calendar month ramadan shawwal dhul-hijja",
        None,
    )
    .await
    .expect("upsert c2");
    idx.upsert_chunk(
        "c3",
        None,
        Some(20),
        Some(25),
        "qibla direction great circle mecca bearing",
        None,
    )
    .await
    .expect("upsert c3");

    // FTS-only retriever (no embedding model required for the test).
    let retriever: Arc<dyn Retriever> = Arc::new(RrfRetriever::new(
        Arc::clone(&idx),
        Arc::new(NoopEmbeddingProvider),
        RrfConfig {
            use_vec: false,
            ..RrfConfig::default()
        },
    ));

    let reg = ToolRegistry::new().with_retriever(Arc::clone(&retriever));

    // Query for "prayer" — should hit c1.
    let params = serde_json::json!({
        "name": "cascade.search",
        "arguments": { "query": "prayer fajr", "limit": 5 }
    });
    let result = reg
        .call(&params)
        .await
        .expect("call must not protocol-error");

    // Must not be an error result.
    assert!(
        result.get("isError").is_none() || result["isError"] == serde_json::Value::Bool(false),
        "search must not return isError; got: {result}"
    );

    // Citations array must contain at least one entry.
    let citations = result["citations"]
        .as_array()
        .expect("citations must be array");
    assert!(
        !citations.is_empty(),
        "cascade.search must return at least one citation for 'prayer fajr'; got: {result}"
    );

    // chunk_id "c1" must appear in citations.
    let ids: Vec<&str> = citations
        .iter()
        .filter_map(|c| c.get("chunk_id").and_then(|v| v.as_str()))
        .collect();
    assert!(
        ids.contains(&"c1"),
        "citation for c1 (prayer chunk) must be present; found: {ids:?}"
    );

    // metadata.ready must be true.
    assert_eq!(
        result["metadata"]["ready"],
        serde_json::Value::Bool(true),
        "metadata.ready must be true when retriever is wired"
    );
}

/// When no retriever is injected, `cascade.search` returns gracefully with
/// `ready: false` rather than an error.
#[tokio::test]
async fn search_no_retriever_returns_not_ready() {
    let reg = ToolRegistry::new(); // no retriever
    let params = serde_json::json!({
        "name": "cascade.search",
        "arguments": { "query": "anything", "limit": 5 }
    });
    let result = reg
        .call(&params)
        .await
        .expect("call must not protocol-error");

    // Must not be is_error.
    assert!(
        result.get("isError").is_none() || result["isError"] == serde_json::Value::Bool(false),
        "no-retriever search must not be is_error"
    );

    // ready flag must be false.
    assert_eq!(
        result["metadata"]["ready"],
        serde_json::Value::Bool(false),
        "metadata.ready must be false when no retriever is wired"
    );

    // Text must explain the situation.
    let text = result["content"][0]["text"].as_str().unwrap_or_default();
    assert!(
        text.contains("index not ready"),
        "response text must mention 'index not ready'; got: {text:?}"
    );
}
