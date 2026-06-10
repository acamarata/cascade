//! Guest-side type definitions mirroring the host's WIT binding types.
//!
//! Purpose: provide no_std-compatible Rust types that serialise identically
//!   to the WIT records in `cascade-plugins/src/wit_bindings/`.
//! Constraints: all types are `Serialize + Deserialize`, no raw pointers,
//!   no std::fs / std::net / std::thread.
//! SPORT: cascade-pdk / types (T-P4-E03-10)

use serde::{Deserialize, Serialize};

// ── Plugin metadata ───────────────────────────────────────────────────────────

/// The six plugin capability categories supported by Cascade.
///
/// Maps to the `plugin_type` field in `plugin.json` and determines which
/// trait methods the host will call.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum PluginKind {
    /// Splits documents into text chunks for embedding.
    Chunker,
    /// Retrieves relevant chunks given a query.
    Retriever,
    /// Provides text embeddings.
    Provider,
    /// Extends the AI assistant with a custom tool.
    ChatTool,
    /// Acts as an autonomous agent.
    Agent,
    /// Fetches documents from an external data source for RAG ingestion.
    DataSource,
    /// Renders a read-only widget panel in the Cascade GUI.
    Widget,
}

// ── Chunker types ─────────────────────────────────────────────────────────────

/// Mirrors WIT `record chunk-meta`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ChunkMeta {
    pub index: u32,
    pub byte_offset: u32,
    pub section: Option<String>,
}

/// Mirrors WIT `record chunk`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Chunk {
    pub text: String,
    pub meta: ChunkMeta,
}

/// Mirrors WIT `record chunk-opts`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ChunkOpts {
    pub target_tokens: u32,
    pub overlap_tokens: u32,
}

/// Mirrors WIT `record document`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Document {
    pub id: String,
    pub content: String,
    pub mime_type: Option<String>,
}

// ── Retriever types ───────────────────────────────────────────────────────────

/// Mirrors WIT `record retrieval-result`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RetrievalResult {
    pub text: String,
    pub score: f32,
    pub source: String,
    pub chunk_index: u32,
}

/// Mirrors WIT `record retrieve-opts`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RetrieveOpts {
    pub top_k: u32,
    pub min_score: f32,
}

// ── Provider types ────────────────────────────────────────────────────────────

/// Mirrors WIT `record embedding`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Embedding {
    pub vector: Vec<f32>,
    pub input_index: u32,
}

/// Mirrors WIT `record embed-opts`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct EmbedOpts {
    pub model_hint: Option<String>,
    pub normalise: bool,
}

// ── Tool integration types ────────────────────────────────────────────────────

/// Mirrors WIT `record tool-call`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ToolCall {
    pub call_id: String,
    pub tool_name: String,
    pub args_json: String,
}

/// Mirrors WIT `record tool-result`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ToolResult {
    pub call_id: String,
    pub output: String,
    pub data_json: Option<String>,
}

// ── Agent types ───────────────────────────────────────────────────────────────

/// Mirrors WIT `record context-entry`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ContextEntry {
    pub key: String,
    pub value: String,
}

/// Mirrors WIT `record agent-context`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct AgentContext {
    pub session_id: String,
    pub metadata: Vec<ContextEntry>,
    pub history: Vec<String>,
}

/// Mirrors WIT `record agent-response`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct AgentResponse {
    pub text: String,
    pub is_final: bool,
}

// ── DataSource types ──────────────────────────────────────────────────────────

/// A normalized document returned by a DataSource plugin for RAG ingestion.
///
/// All DataSource plugins must map their API responses to this shape so the
/// RAG pipeline can ingest items from any source uniformly.
///
/// The `source` field must follow the pattern `"<plugin-id>:<owner>/<repo>"` or
/// `"<plugin-id>:<team>"` so documents remain attributable after ingestion.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct DataItem {
    /// Unique identifier for the item within its source (e.g. issue number as string).
    pub id: String,
    /// Short human-readable title (issue title, ticket title, etc.).
    pub title: String,
    /// Full body / description text; may be empty if the source has no body.
    pub body: String,
    /// Canonical URL for the item (for link-back in UI).
    pub url: String,
    /// Tags, labels, or categories attached to the item.
    pub labels: Vec<String>,
    /// ISO-8601 creation timestamp (UTC).
    pub created_at: String,
    /// ISO-8601 last-updated timestamp (UTC).
    pub updated_at: String,
    /// Source identifier (e.g. `"github-issues:owner/repo"`, `"linear:TEAM"`).
    pub source: String,
    /// Arbitrary extra metadata as a JSON object string; use `"{}"` if empty.
    /// Allows plugins to surface source-specific fields (priority, state, etc.)
    /// without breaking the shared DataItem shape.
    pub extra_json: String,
}

/// Arguments passed to `fetch_items` via the dispatch envelope.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct DataSourceFetchArgs {
    /// Opaque pagination cursor returned by a previous `fetch_items` call.
    /// `None` on the first call.
    pub cursor: Option<String>,
}

/// Result of a `fetch_items` call.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct DataSourcePage {
    /// Items fetched in this page.
    pub items: Vec<DataItem>,
    /// Cursor to pass to the next `fetch_items` call; `None` when exhausted.
    pub next_cursor: Option<String>,
}

// ── Widget types ──────────────────────────────────────────────────────────────

/// Render payload returned by a Widget plugin's `render()` call.
///
/// The host displays `title` as the panel header, `body` as the panel content,
/// and makes `metadata` available to the GUI for filtering / sorting.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct WidgetData {
    /// Short title shown in the widget panel header.
    pub title: String,
    /// Main content string rendered in the widget body.
    pub body: String,
    /// Arbitrary JSON metadata (use `serde_json::Value::Null` if empty).
    pub metadata: serde_json::Value,
}

// ── Error type ────────────────────────────────────────────────────────────────

/// Unified error type for all plugin operations.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum PluginError {
    /// The provided input was empty or invalid.
    InvalidInput { message: String },
    /// The plugin ran out of execution fuel.
    ResourceExhausted,
    /// The requested operation is not implemented by this plugin.
    NotImplemented,
    /// An internal plugin error with a diagnostic message.
    Internal { message: String },
}

#[cfg(not(target_arch = "wasm32"))]
impl std::fmt::Display for PluginError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            PluginError::InvalidInput { message } => write!(f, "invalid input: {message}"),
            PluginError::ResourceExhausted => write!(f, "resource exhausted"),
            PluginError::NotImplemented => write!(f, "not implemented"),
            PluginError::Internal { message } => write!(f, "internal error: {message}"),
        }
    }
}

#[cfg(not(target_arch = "wasm32"))]
impl std::error::Error for PluginError {}
