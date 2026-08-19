// Tests for all ipc sub-modules.
#![cfg(test)]

use crate::ipc::*;
use serde_json;

/// Helper: serialize to JSON then deserialize back, assert equality.
fn roundtrip<
    T: serde::Serialize + for<'de> serde::Deserialize<'de> + PartialEq + std::fmt::Debug,
>(
    value: &T,
) {
    let json = serde_json::to_string(value).expect("serialize");
    let back: T = serde_json::from_str(&json).expect("deserialize");
    assert_eq!(value, &back, "roundtrip mismatch for {json}");
}

// ── Core envelope types ──────────────────────────────────────────────────

#[test]
fn test_request_id_roundtrip() {
    roundtrip(&RequestId::Number(42));
    roundtrip(&RequestId::String("abc".to_string()));
    roundtrip(&RequestId::Null);
}

#[test]
fn test_json_rpc_version_roundtrip() {
    roundtrip(&JsonRpcVersion::V2_0);
}

#[test]
fn test_rpc_error_roundtrip() {
    roundtrip(&RpcError {
        code: INTERNAL_ERROR,
        message: "oops".to_string(),
        data: Some(serde_json::json!({"key": "val"})),
    });
    roundtrip(&RpcError {
        code: METHOD_NOT_FOUND,
        message: "no such method".to_string(),
        data: None,
    });
}

#[test]
fn test_request_envelope_roundtrip() {
    let req = Request {
        jsonrpc: JsonRpcVersion::V2_0,
        id: RequestId::Number(1),
        method: "ping".to_string(),
        params: Some(PingParams {
            echo: Some("hello".to_string()),
        }),
        protocol_version: PROTOCOL_VERSION,
    };
    roundtrip(&req);
}

#[test]
fn test_response_envelope_roundtrip() {
    let resp: Response<PingResult> = Response {
        jsonrpc: JsonRpcVersion::V2_0,
        id: RequestId::Number(1),
        result: Some(PingResult {
            pong: "hello".to_string(),
        }),
        error: None,
    };
    roundtrip(&resp);
}

#[test]
fn test_response_error_roundtrip() {
    let resp: Response<PingResult> = Response {
        jsonrpc: JsonRpcVersion::V2_0,
        id: RequestId::Number(2),
        result: None,
        error: Some(RpcError {
            code: INTERNAL_ERROR,
            message: "daemon failure".to_string(),
            data: None,
        }),
    };
    roundtrip(&resp);
}

// ── Method-specific params / result pairs ────────────────────────────────

#[test]
fn test_config_get_roundtrip() {
    roundtrip(&ConfigGetParams {
        key: "daemon.socket_path".to_string(),
    });
    roundtrip(&ConfigGetResult {
        key: "daemon.socket_path".to_string(),
        value: serde_json::json!("/tmp/cascade.sock"),
    });
}

#[test]
fn test_config_set_roundtrip() {
    roundtrip(&ConfigSetParams {
        key: "rag.top_k".to_string(),
        value: serde_json::json!(20),
    });
    roundtrip(&ConfigSetResult {
        key: "rag.top_k".to_string(),
        previous: Some(serde_json::json!(10)),
    });
    roundtrip(&ConfigSetResult {
        key: "rag.top_k".to_string(),
        previous: None,
    });
}

#[test]
fn test_daemon_stop_roundtrip() {
    roundtrip(&DaemonStopParams {});
    roundtrip(&DaemonStopResult {
        status: "stopping".to_string(),
    });
}

#[test]
fn test_health_roundtrip() {
    roundtrip(&HealthParams {});
    roundtrip(&HealthCheck {
        name: "sqlite".to_string(),
        ok: true,
        detail: None,
    });
    roundtrip(&HealthCheck {
        name: "rag_index".to_string(),
        ok: false,
        detail: Some("index file missing".to_string()),
    });
    roundtrip(&HealthResult {
        ok: false,
        checks: vec![
            HealthCheck {
                name: "sqlite".to_string(),
                ok: true,
                detail: None,
            },
            HealthCheck {
                name: "rag_index".to_string(),
                ok: false,
                detail: Some("stale".to_string()),
            },
        ],
    });
}

#[test]
fn test_hotword_lookup_roundtrip() {
    roundtrip(&HotwordLookupParams {
        word: "claude".to_string(),
    });
    roundtrip(&HotwordLookupResult {
        block: Some("claude_code".to_string()),
    });
    roundtrip(&HotwordLookupResult { block: None });
}

#[test]
fn test_inbox_summary_roundtrip() {
    roundtrip(&InboxSummaryParams { limit: Some(5) });
    roundtrip(&InboxSummaryParams { limit: None });
    roundtrip(&InboxItem {
        id: "msg-2026-01-01-test".to_string(),
        subject: "Test message".to_string(),
        from: "cascade".to_string(),
        priority: "high".to_string(),
        created: "2026-01-01T00:00:00Z".to_string(),
    });
    roundtrip(&InboxSummaryResult {
        items: vec![InboxItem {
            id: "msg-001".to_string(),
            subject: "Hello".to_string(),
            from: "system".to_string(),
            priority: "low".to_string(),
            created: "2026-06-01T12:00:00Z".to_string(),
        }],
    });
}

#[test]
fn test_memory_read_roundtrip() {
    roundtrip(&MemoryReadParams {
        project: "/home/user/projects/acamarata/cascade".to_string(),
        file: "decisions.md".to_string(),
    });
    roundtrip(&MemoryReadResult {
        content: "# Decisions\n\n- Use JSON-RPC 2.0\n".to_string(),
        path: "/home/user/projects/acamarata/cascade/.claude/memory/decisions.md".to_string(),
    });
}

#[test]
fn test_memory_write_roundtrip() {
    roundtrip(&MemoryWriteParams {
        project: "/home/user/projects/acamarata/cascade".to_string(),
        file: "lessons.md".to_string(),
        content: "## Lessons\n\n- Always roundtrip-test serde types\n".to_string(),
    });
    roundtrip(&MemoryWriteResult {
        path: "/home/user/projects/acamarata/cascade/.claude/memory/lessons.md".to_string(),
        bytes: 48,
    });
}

#[test]
fn test_ping_roundtrip() {
    roundtrip(&PingParams {
        echo: Some("world".to_string()),
    });
    roundtrip(&PingParams { echo: None });
    roundtrip(&PingResult {
        pong: "world".to_string(),
    });
}

#[test]
fn test_provider_quota_roundtrip() {
    roundtrip(&ProviderQuotaParams {});
    roundtrip(&ProviderEntry {
        name: "anthropic".to_string(),
        pct_used: 42.5,
        resets_at: Some("2026-06-07T00:00:00Z".to_string()),
    });
    roundtrip(&ProviderEntry {
        name: "openai".to_string(),
        pct_used: 0.0,
        resets_at: None,
    });
    roundtrip(&ProviderQuotaResult {
        providers: vec![ProviderEntry {
            name: "gemini".to_string(),
            pct_used: 15.3,
            resets_at: None,
        }],
    });
}

#[test]
fn test_resolve_roundtrip() {
    roundtrip(&ResolveParams {
        cwd: Some(std::path::PathBuf::from("/tmp/test")),
        tier: Some("gci".to_string()),
        format: Some("markdown".to_string()),
    });
    roundtrip(&ResolveParams {
        cwd: None,
        tier: None,
        format: None,
    });
    roundtrip(&ResolveResult {
        content: "# GCI\n\n...".to_string(),
        format: "markdown".to_string(),
        tier: "gci".to_string(),
    });
}

#[test]
fn test_search_roundtrip() {
    roundtrip(&SearchParams {
        query: "how to configure rag".to_string(),
        limit: Some(10),
    });
    roundtrip(&SearchParams {
        query: "daemon socket path".to_string(),
        limit: None,
    });
    roundtrip(&SearchHit {
        id: "chunk-001".to_string(),
        score: 0.92,
        excerpt: "The daemon socket is at ~/.cascade/daemon.sock".to_string(),
        source: ".claude/docs/architecture.md".to_string(),
    });
    roundtrip(&SearchResult {
        hits: vec![SearchHit {
            id: "chunk-002".to_string(),
            score: 0.85,
            excerpt: "BGE-M3 embeddings are used for semantic search".to_string(),
            source: ".claude/docs/rag.md".to_string(),
        }],
    });
}

#[test]
fn test_status_roundtrip() {
    roundtrip(&StatusParams {});
    roundtrip(&StatusResult {
        pid: 12345,
        uptime_secs: 3600,
        queue_depth: 0,
        rag_index_fresh: true,
        version: "0.1.0".to_string(),
        tcp_port: None,
        index_paused: false,
    });
}

/// Old daemon JSON (no `index_paused` key) must still deserialise into
/// `StatusResult` with `index_paused = false`.  Validates the `#[serde(default)]`
/// guard added in schema v1.2 (T-P7-E20-29).
#[test]
fn test_status_result_old_json_missing_index_paused_deserialises() {
    let old_json = r#"{
        "pid": 9999,
        "uptime_secs": 120,
        "queue_depth": 0,
        "rag_index_fresh": true,
        "version": "0.8.0",
        "tcp_port": null
    }"#;
    let result: StatusResult =
        serde_json::from_str(old_json).expect("old daemon JSON must deserialise");
    assert!(
        !result.index_paused,
        "missing index_paused must default to false"
    );
}

// ── JSON-RPC 2.0 spec compliance checks ─────────────────────────────────

#[test]
fn test_jsonrpc_version_serialises_as_string() {
    let json = serde_json::to_string(&JsonRpcVersion::V2_0).unwrap();
    assert_eq!(json, r#""2.0""#, "jsonrpc field must serialise as \"2.0\"");
}

#[test]
fn test_request_id_null_serialises_correctly() {
    let json = serde_json::to_string(&RequestId::Null).unwrap();
    assert_eq!(json, "null");
}

#[test]
fn test_protocol_version_constant() {
    assert_eq!(PROTOCOL_VERSION, 1);
}

#[test]
fn test_error_codes() {
    assert_eq!(METHOD_NOT_FOUND, -32601);
    assert_eq!(INVALID_PARAMS, -32602);
    assert_eq!(INTERNAL_ERROR, -32603);
    assert_eq!(DAEMON_NOT_RUNNING, -32001);
    assert_eq!(AUTH_FAILED, -32002);
    assert_eq!(RESOURCE_NOT_FOUND, -32003);
}

// ── Schema validation (T-P2-E07-11) ────────────────────────────────────

#[test]
fn deserialize_request_rejects_unknown_field() {
    // PingParams has #[serde(deny_unknown_fields)]; an extra key must fail.
    let body = br#"{"jsonrpc":"2.0","id":1,"method":"ping","protocol_version":1,"params":{"echo":"hi","bogus":true}}"#;
    let err = deserialize_request::<PingParams>(body).unwrap_err();
    assert!(
        matches!(err, crate::error::IpcError::UnknownField(_)),
        "expected UnknownField, got {err:?}"
    );
}

#[test]
fn deserialize_request_rejects_missing_field() {
    // Request<P> requires `method`; omit it to force a missing-field error.
    let body = br#"{"jsonrpc":"2.0","id":1,"protocol_version":1,"params":{"echo":"hi"}}"#;
    let err = deserialize_request::<PingParams>(body).unwrap_err();
    assert!(
        matches!(err, crate::error::IpcError::MissingField(_)),
        "expected MissingField, got {err:?}"
    );
}

#[test]
fn deserialize_request_accepts_valid() {
    let body =
        br#"{"jsonrpc":"2.0","id":1,"method":"ping","protocol_version":1,"params":{"echo":"hi"}}"#;
    let req = deserialize_request::<PingParams>(body).expect("valid request must deserialize");
    assert_eq!(req.method, "ping");
}

// ── Field-value bounds (T-P2-E07-12) ───────────────────────────────────

#[test]
fn validate_resolve_params_rejects_unknown_tier() {
    let p = ResolveParams {
        cwd: None,
        tier: Some("evil".to_string()),
        format: None,
    };
    match validate_resolve_params(&p) {
        Err(crate::error::IpcError::InvalidFieldValue { field, .. }) => {
            assert_eq!(field, "tier");
        }
        other => panic!("expected InvalidFieldValue{{tier}}, got {other:?}"),
    }
    // A valid tier passes.
    let ok = ResolveParams {
        cwd: None,
        tier: Some("gci".to_string()),
        format: None,
    };
    assert!(validate_resolve_params(&ok).is_ok());
}

#[test]
fn validate_search_params_rejects_oversized_query() {
    let p = SearchParams {
        query: "x".repeat(MAX_QUERY_LEN + 1),
        limit: None,
    };
    match validate_search_params(&p) {
        Err(crate::error::IpcError::InvalidFieldValue { field, .. }) => {
            assert_eq!(field, "query");
        }
        other => panic!("expected InvalidFieldValue{{query}}, got {other:?}"),
    }
    // Exactly at the cap is allowed.
    let ok = SearchParams {
        query: "x".repeat(MAX_QUERY_LEN),
        limit: None,
    };
    assert!(validate_search_params(&ok).is_ok());
}

#[test]
fn validate_memory_write_params_rejects_traversal_and_oversize() {
    let traversal = MemoryWriteParams {
        project: "../../etc".to_string(),
        file: "lessons.md".to_string(),
        content: "ok".to_string(),
    };
    match validate_memory_write_params(&traversal) {
        Err(crate::error::IpcError::InvalidFieldValue { field, .. }) => {
            assert_eq!(field, "project_path");
        }
        other => panic!("expected InvalidFieldValue{{project_path}}, got {other:?}"),
    }
    let oversize = MemoryWriteParams {
        project: "myproj".to_string(),
        file: "lessons.md".to_string(),
        content: "x".repeat(MAX_CONTENT_LEN + 1),
    };
    match validate_memory_write_params(&oversize) {
        Err(crate::error::IpcError::InvalidFieldValue { field, .. }) => {
            assert_eq!(field, "content");
        }
        other => panic!("expected InvalidFieldValue{{content}}, got {other:?}"),
    }
    // A clean payload passes.
    let ok = MemoryWriteParams {
        project: "myproj".to_string(),
        file: "lessons.md".to_string(),
        content: "hello".to_string(),
    };
    assert!(validate_memory_write_params(&ok).is_ok());
}
