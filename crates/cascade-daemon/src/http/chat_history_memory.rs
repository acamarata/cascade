//! Purpose: HTTP handlers for the RAG-08 chat_history API.
//!   Exposes POST and GET endpoints for persisting and retrieving chat turns
//!   per scope + namespace.
//!
//! # Routes (nested at /api/memory by the parent router)
//!   POST /chat        — insert a chat message
//!   GET  /chat        — list recent chat messages
//!
//! # Inputs
//!   POST body: { scope, namespace, role, content, opt_in? }
//!   GET  query: scope, namespace, limit?, opt_in?
//!
//! # Outputs
//!   POST: 200 { id }
//!   GET:  200 { messages: [...] }
//!
//! # Constraints
//!   - Personal namespace blocked without opt_in=true (firewall enforced at DB layer).
//!   - rusqlite is sync; all DB work runs in spawn_blocking.
//!   - DB path: ~/.cascade/memory.db (shared with MCP memory handlers).
//!
//! // TODO app-01: swap app's localStorage to use this endpoint

use axum::{
    extract::Query,
    http::StatusCode,
    response::IntoResponse,
    routing::{get, post},
    Json, Router,
};
use serde::{Deserialize, Serialize};
use std::collections::HashMap;

use cascade_rag::db::run_migrations;
use cascade_rag::memory::{
    chat::{insert_chat, list_chat, ChatMessage},
    namespace::validate_with_firewall,
};

use crate::dashboard::DashboardState;

// ── Request / Response types ─────────────────────────────────────────────────

#[derive(Debug, Deserialize)]
pub struct InsertChatRequest {
    pub scope: String,
    pub namespace: String,
    pub role: String,
    pub content: String,
    #[serde(default)]
    pub opt_in: bool,
}

#[derive(Debug, Serialize)]
pub struct InsertChatResponse {
    pub id: String,
}

#[derive(Debug, Serialize)]
pub struct ListChatResponse {
    pub messages: Vec<ChatMessageDto>,
}

#[derive(Debug, Serialize)]
pub struct ChatMessageDto {
    pub id: String,
    pub scope: String,
    pub namespace: String,
    pub role: String,
    pub content: String,
    pub created_at: String,
}

impl From<ChatMessage> for ChatMessageDto {
    fn from(m: ChatMessage) -> Self {
        Self {
            id: m.id,
            scope: m.scope,
            namespace: m.namespace,
            role: m.role,
            content: m.content,
            created_at: m.created_at,
        }
    }
}

// ── Router ────────────────────────────────────────────────────────────────────

pub fn router() -> Router<DashboardState> {
    Router::new()
        .route("/chat", post(insert_chat_handler))
        .route("/chat", get(list_chat_handler))
}

// ── DB helper ────────────────────────────────────────────────────────────────

fn open_memory_db() -> std::result::Result<rusqlite::Connection, String> {
    let path = cascade_mcp_paths::memory_db_path();
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent)
            .map_err(|e| format!("failed to create ~/.cascade/: {e}"))?;
    }
    let conn = rusqlite::Connection::open(&path)
        .map_err(|e| format!("failed to open memory DB: {e}"))?;
    run_migrations(&conn).map_err(|e| format!("migration failed: {e}"))?;
    Ok(conn)
}

/// Re-use the same path resolution as cascade-mcp.
mod cascade_mcp_paths {
    use std::path::PathBuf;
    use cascade_types::paths::home_dir;
    pub fn memory_db_path() -> PathBuf {
        home_dir().join(".cascade").join("memory.db")
    }
}

// ── Handlers ─────────────────────────────────────────────────────────────────

/// POST /api/memory/chat — insert a chat message.
async fn insert_chat_handler(
    Json(req): Json<InsertChatRequest>,
) -> impl IntoResponse {
    let namespace = match validate_with_firewall(&req.namespace, req.opt_in) {
        Ok(ns) => ns,
        Err(e) => {
            return (
                StatusCode::FORBIDDEN,
                Json(serde_json::json!({ "error": e.to_string() })),
            )
                .into_response();
        }
    };

    let scope = req.scope.clone();
    let role = req.role.clone();
    let content = req.content.clone();

    let result = tokio::task::spawn_blocking(move || {
        let conn = open_memory_db()?;
        insert_chat(&conn, &scope, &namespace, &role, &content)
            .map_err(|e| format!("insert_chat failed: {e}"))
    })
    .await;

    match result {
        Ok(Ok(id)) => (StatusCode::OK, Json(serde_json::json!({ "id": id }))).into_response(),
        Ok(Err(e)) => (
            StatusCode::INTERNAL_SERVER_ERROR,
            Json(serde_json::json!({ "error": e })),
        )
            .into_response(),
        Err(e) => (
            StatusCode::INTERNAL_SERVER_ERROR,
            Json(serde_json::json!({ "error": format!("spawn_blocking panic: {e}") })),
        )
            .into_response(),
    }
}

/// GET /api/memory/chat?scope=...&namespace=...&limit=...&opt_in=...
async fn list_chat_handler(
    Query(params): Query<HashMap<String, String>>,
) -> impl IntoResponse {
    let scope = match params.get("scope") {
        Some(s) => s.clone(),
        None => {
            return (
                StatusCode::BAD_REQUEST,
                Json(serde_json::json!({ "error": "'scope' query param is required" })),
            )
                .into_response();
        }
    };
    let namespace_raw = match params.get("namespace") {
        Some(n) => n.clone(),
        None => {
            return (
                StatusCode::BAD_REQUEST,
                Json(serde_json::json!({ "error": "'namespace' query param is required" })),
            )
                .into_response();
        }
    };
    let opt_in = params
        .get("opt_in")
        .and_then(|v| v.parse::<bool>().ok())
        .unwrap_or(false);
    let limit = params
        .get("limit")
        .and_then(|v| v.parse::<usize>().ok())
        .unwrap_or(50);

    let namespace = match validate_with_firewall(&namespace_raw, opt_in) {
        Ok(ns) => ns,
        Err(e) => {
            return (
                StatusCode::FORBIDDEN,
                Json(serde_json::json!({ "error": e.to_string() })),
            )
                .into_response();
        }
    };

    let result = tokio::task::spawn_blocking(move || {
        let conn = open_memory_db()?;
        list_chat(&conn, &scope, &namespace, limit)
            .map_err(|e| format!("list_chat failed: {e}"))
    })
    .await;

    match result {
        Ok(Ok(msgs)) => {
            let dtos: Vec<ChatMessageDto> = msgs.into_iter().map(ChatMessageDto::from).collect();
            (StatusCode::OK, Json(serde_json::json!({ "messages": dtos }))).into_response()
        }
        Ok(Err(e)) => (
            StatusCode::INTERNAL_SERVER_ERROR,
            Json(serde_json::json!({ "error": e })),
        )
            .into_response(),
        Err(e) => (
            StatusCode::INTERNAL_SERVER_ERROR,
            Json(serde_json::json!({ "error": format!("spawn_blocking panic: {e}") })),
        )
            .into_response(),
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use axum::{
        body::Body,
        http::{Request, StatusCode},
    };
    use tower::ServiceExt;

    use super::router;
    use crate::dashboard::DashboardState;

    fn test_state() -> DashboardState {
        DashboardState {
            token: std::sync::Arc::new("test-token".to_string()),
            provider_registry: None,
            routing_ring: None,
        }
    }

    #[tokio::test]
    async fn insert_chat_blocks_personal_without_opt_in() {
        let app = router().with_state(test_state());
        let body = serde_json::json!({
            "scope": "cascade-app",
            "namespace": "personal",
            "role": "user",
            "content": "secret"
        });
        let response = app
            .oneshot(
                Request::builder()
                    .method("POST")
                    .uri("/chat")
                    .header("content-type", "application/json")
                    .body(Body::from(body.to_string()))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::FORBIDDEN);
    }

    #[tokio::test]
    async fn list_chat_missing_scope_returns_400() {
        let app = router().with_state(test_state());
        let response = app
            .oneshot(
                Request::builder()
                    .uri("/chat?namespace=dev-test")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::BAD_REQUEST);
    }

    #[tokio::test]
    async fn list_chat_missing_namespace_returns_400() {
        let app = router().with_state(test_state());
        let response = app
            .oneshot(
                Request::builder()
                    .uri("/chat?scope=cascade-app")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::BAD_REQUEST);
    }
}
