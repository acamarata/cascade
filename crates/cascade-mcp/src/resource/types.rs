//! Wire types and the `ContentBackend` trait for the MCP resource subsystem.

use async_trait::async_trait;
use serde::{Deserialize, Serialize};

use crate::error::McpServerError;

// ── Constants ─────────────────────────────────────────────────────────────────

/// Number of resources returned per page in `resources/list`.
pub const PAGE_SIZE: usize = 50;

// ── MCP resource wire types ───────────────────────────────────────────────────

/// A single resource entry returned by `resources/list`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct McpResource {
    /// Unique resource URI (`cascade://...`).
    pub uri: String,
    /// Human-readable name.
    pub name: String,
    /// Optional human-readable description.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
    /// MIME type; defaults to `text/markdown` for tier/memory resources, `application/json`
    /// for inbox resources.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub mime_type: Option<String>,
}

/// Text resource contents returned inside the `contents` array by `resources/read`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct TextResourceContents {
    pub uri: String,
    pub mime_type: String,
    /// UTF-8 text payload.
    pub text: String,
}

// ── ContentBackend trait (mockable) ──────────────────────────────────────────

/// Backend that resolves `cascade://` URIs to their text content.
///
/// The production implementation reads from the filesystem via `cascade_mcp::paths`.
/// Test code can inject a `MockContentBackend` without touching the filesystem.
#[async_trait]
pub trait ContentBackend: Send + Sync {
    /// Resolve `uri` to its text content.
    ///
    /// Returns `Ok(Some(text))` when found, `Ok(None)` when the resource
    /// exists in the catalog but has no content yet (e.g. tier file absent),
    /// and `Err(McpServerError)` for unrecognised URI schemes or I/O errors.
    async fn read_uri(&self, uri: &str) -> Result<Option<String>, McpServerError>;
}
