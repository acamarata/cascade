//! MCP Resources — `cascade://` URI scheme for tier files.
//!
//! Resources expose Cascade tier instruction files as MCP resources.
//! Clients (Claude Code, Claude Desktop) can browse and read tier content
//! without invoking a tool.
//!
//! ## URI scheme
//!
//! ```text
//! cascade://tiers/{tier}          — read a tier's CASCADE.md
//! cascade://tiers/{tier}/files    — list files at a tier
//! cascade://memory/{project}/{file}  — read a memory file
//! cascade://inbox/{project}       — list PCI inbox messages
//! ```
//!
//! ## MCP resource spec
//!
//! Resources respond to two JSON-RPC methods:
//! - `resources/list` → `ResourceList`
//! - `resources/read` → `ResourceContents`

use serde::{Deserialize, Serialize};
use serde_json::Value;
use tracing::debug;

use crate::paths as mcp_paths;
use cascade_types::error::{CascadeError, Result};

// ── MCP resource types ────────────────────────────────────────────────────────

/// A single resource entry returned by `resources/list`.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct McpResource {
    /// Unique resource URI.
    pub uri: String,
    /// Human-readable name.
    pub name: String,
    /// Optional description.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
    /// MIME type (default `text/markdown`).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub mime_type: Option<String>,
}

/// Contents of a resource returned by `resources/read`.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct McpResourceContents {
    pub uri: String,
    pub mime_type: String,
    /// UTF-8 text content.
    pub text: String,
}

// ── ResourceRegistry ─────────────────────────────────────────────────────────

/// Handles `resources/list` and `resources/read` for the MCP server.
///
/// Resources are resolved lazily from the filesystem; there is no in-memory
/// cache. Reading a resource always reflects the current file content.
pub struct ResourceRegistry;

impl ResourceRegistry {
    pub fn new() -> Self {
        Self
    }

    /// Handle `resources/list` — enumerate all available resources.
    ///
    /// Returns a JSON object `{ "resources": [...] }` per MCP spec.
    pub async fn list(&self) -> Result<Value> {
        debug!("resources/list");

        let mut resources = vec![
            McpResource {
                uri: "cascade://tiers/gci".into(),
                name: "GCI — Global Claude Instructions".into(),
                description: Some("Global instructions for all AI work".into()),
                mime_type: Some("text/markdown".into()),
            },
            McpResource {
                uri: "cascade://tiers/ppc".into(),
                name: "PPC — Project-level context".into(),
                description: Some("Active project instructions".into()),
                mime_type: Some("text/markdown".into()),
            },
            McpResource {
                uri: "cascade://tiers/prc".into(),
                name: "PRC — Repo-level context".into(),
                description: Some("Current repository instructions".into()),
                mime_type: Some("text/markdown".into()),
            },
        ];

        Ok(serde_json::json!({ "resources": resources }))
    }

    /// Handle `resources/read` — read the content of a single resource.
    ///
    /// Params: `{ "uri": "cascade://..." }`
    /// Returns: `{ "contents": [{ "uri": "...", "mimeType": "text/markdown", "text": "..." }] }`
    pub async fn read(&self, params: &Value) -> Result<Value> {
        let uri = params.get("uri").and_then(|v| v.as_str()).ok_or_else(|| {
            CascadeError::ConfigParse {
                path: "<params>".into(),
                detail: "missing 'uri' in resources/read params".into(),
            }
        })?;

        debug!(uri, "resources/read");

        let text = self.resolve_uri(uri).await?;
        let contents = serde_json::json!({
            "contents": [{
                "uri": uri,
                "mimeType": "text/markdown",
                "text": text,
            }]
        });

        Ok(contents)
    }

    /// Resolve a `cascade://` URI to its text content.
    ///
    /// Currently reads from the filesystem using the standard Cascade tier
    /// path resolution logic. Returns an error if the URI scheme is unknown
    /// or the file does not exist.
    async fn resolve_uri(&self, uri: &str) -> Result<String> {
        if let Some(path_tail) = uri.strip_prefix("cascade://tiers/") {
            let tier_name = path_tail.trim_end_matches("/files");
            let path = mcp_paths::tier_file(tier_name);
            tokio::fs::read_to_string(&path)
                .await
                .map_err(|e| CascadeError::Io {
                    path,
                    operation: "read_tier_file",
                    source: e,
                })
        } else if let Some(rest) = uri.strip_prefix("cascade://memory/") {
            let mut parts = rest.splitn(2, '/');
            let project = parts.next().unwrap_or_default();
            let file = parts.next().unwrap_or("decisions.md");
            let path = mcp_paths::memory_file(project, file);
            tokio::fs::read_to_string(&path)
                .await
                .map_err(|e| CascadeError::Io {
                    path,
                    operation: "read_memory_file",
                    source: e,
                })
        } else {
            Err(CascadeError::ConfigParse {
                path: "<uri>".into(),
                detail: format!("Unknown resource URI scheme: {uri}"),
            })
        }
    }
}

impl Default for ResourceRegistry {
    fn default() -> Self {
        Self::new()
    }
}
