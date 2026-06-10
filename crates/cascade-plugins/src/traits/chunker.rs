//! Chunker plugin trait — host-side plugin-facing API.
//!
//! Purpose: define the `ChunkPlugin` trait that a WASM plugin must satisfy to
//!   be registered as a Chunker in the cascade-plugins host. The I/O types use
//!   plain JSON-serde-able shapes suitable for the WASM JSON ptr/len protocol.
//! Inputs: `ChunkQuery` — raw document bytes (base64), mime-type, and opts.
//! Outputs: `Vec<Chunk>` — text + byte range + metadata map per chunk.
//! Constraints: all public types are `Serialize + Deserialize`; trait must be
//!   object-safe; `Send + Sync + 'static` bounds required for async dispatch.
//! SPORT: cascade-plugins / chunker plugin trait (T-P4-E03-01)

use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use thiserror::Error;

// ── I/O shapes ────────────────────────────────────────────────────────────────

/// Plugin-facing request for a chunk operation.
///
/// The `content` field carries the raw document text (UTF-8). The `mime_type`
/// hint lets plugins specialize (Markdown vs. Rust vs. plain text).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Query {
    /// Document text content (UTF-8).
    pub content: String,

    /// MIME type or format hint (e.g. `"text/markdown"`, `"text/x-rust"`).
    /// `None` means the plugin should auto-detect or apply default behavior.
    pub mime_type: Option<String>,

    /// Target token/character count per chunk (soft maximum).
    #[serde(default = "default_target_size")]
    pub target_size: usize,

    /// Overlap in tokens/characters between consecutive chunks.
    #[serde(default = "default_overlap")]
    pub overlap: usize,
}

fn default_target_size() -> usize {
    512
}
fn default_overlap() -> usize {
    64
}

/// A single chunk produced by a `ChunkPlugin`.
///
/// `byte_start` and `byte_end` are offsets into the original UTF-8 document
/// content (not the plugin-local copy). `metadata` carries any plugin-defined
/// provenance keys (e.g. `"section"`, `"heading"`).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Chunk {
    /// The text content of this chunk.
    pub text: String,

    /// Byte offset (inclusive) in the source document where this chunk starts.
    pub byte_start: usize,

    /// Byte offset (exclusive) where this chunk ends.
    pub byte_end: usize,

    /// Arbitrary string key/value metadata (e.g. `"lang"`, `"section_title"`).
    #[serde(default)]
    pub metadata: HashMap<String, String>,
}

// ── Error type ────────────────────────────────────────────────────────────────

/// Errors that a `ChunkPlugin` may return.
///
/// Variants correspond to the three WASM error classes documented in the plugin
/// ABI guide: I/O failure, parse/decode failure, and transient unavailability.
#[derive(Debug, Error, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum ChunkPluginError {
    /// The plugin could not read the document bytes (e.g. base64 decode failure).
    #[error("IO error in chunker plugin: {message}")]
    Io { message: String },

    /// The document format was not parseable by this plugin.
    #[error("parse error in chunker plugin: {message}")]
    Parse { message: String },

    /// The plugin is temporarily unavailable (resource exhausted, model loading).
    #[error("chunker plugin unavailable: {message}")]
    Unavailable { message: String },
}

// ── Capability declarations ───────────────────────────────────────────────────

/// Capability declaration returned by `ChunkPlugin::capabilities()`.
///
/// The host inspects this at plugin registration time to decide sandbox limits
/// and enforce the plugin sandbox policy.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ChunkPluginCapabilities {
    /// Whether this plugin requires filesystem read access (e.g. for grammar files).
    #[serde(default)]
    pub needs_fs_read: bool,

    /// Whether this plugin requires outbound network access (e.g. remote tokenizer).
    #[serde(default)]
    pub needs_net_outbound: bool,

    /// Maximum WASM memory this plugin requests in MiB (capped at 64 for chunkers).
    #[serde(default = "default_chunker_memory_mb")]
    pub max_memory_mb: u32,
}

fn default_chunker_memory_mb() -> u32 {
    64
}

impl Default for ChunkPluginCapabilities {
    fn default() -> Self {
        Self {
            needs_fs_read: false,
            needs_net_outbound: false,
            max_memory_mb: default_chunker_memory_mb(),
        }
    }
}

// ── Trait ─────────────────────────────────────────────────────────────────────

/// Host-side trait a WASM chunker plugin must satisfy.
///
/// The host loads the plugin, wraps it in `WasmChunker` (which implements the
/// `cascade-types::Chunker` trait), and dispatches calls through this interface.
///
/// # Object-safety
///
/// This trait is object-safe. The async methods use `async_trait` to produce
/// boxed futures at the call site, ensuring `dyn ChunkPlugin` works.
///
/// # Example (mock implementation for tests)
///
/// ```rust
/// # use cascade_plugins::traits::chunker::{ChunkPlugin, ChunkPluginCapabilities, ChunkPluginError, Chunk, Query};
/// # use async_trait::async_trait;
/// struct SentenceChunker;
///
/// #[async_trait]
/// impl ChunkPlugin for SentenceChunker {
///     fn name(&self) -> &str { "sentence-chunker" }
///     fn capabilities(&self) -> ChunkPluginCapabilities { Default::default() }
///
///     async fn chunk(&self, query: Query) -> Result<Vec<Chunk>, ChunkPluginError> {
///         Ok(vec![Chunk {
///             text: query.content,
///             byte_start: 0,
///             byte_end: 0,
///             metadata: Default::default(),
///         }])
///     }
/// }
/// ```
#[async_trait]
pub trait ChunkPlugin: Send + Sync + 'static {
    /// A stable, human-readable name for this plugin (used in logging and config).
    fn name(&self) -> &str;

    /// Capability declaration — inspected by the host at plugin registration time.
    fn capabilities(&self) -> ChunkPluginCapabilities;

    /// Split `query.content` into chunks.
    ///
    /// The returned vec must be ordered by `byte_start` (ascending).
    /// Implementations must not return overlapping chunks.
    async fn chunk(&self, query: Query) -> Result<Vec<Chunk>, ChunkPluginError>;
}

// ── Object-safety static assertion ───────────────────────────────────────────

/// Confirms `ChunkPlugin` is object-safe at compile time.
fn _assert_object_safe(_: &dyn ChunkPlugin) {}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    struct MockChunker;

    #[async_trait]
    impl ChunkPlugin for MockChunker {
        fn name(&self) -> &str {
            "mock-chunker"
        }

        fn capabilities(&self) -> ChunkPluginCapabilities {
            Default::default()
        }

        async fn chunk(&self, query: Query) -> Result<Vec<Chunk>, ChunkPluginError> {
            if query.content.is_empty() {
                return Err(ChunkPluginError::Parse {
                    message: "empty document".into(),
                });
            }
            let len = query.content.len();
            Ok(vec![Chunk {
                text: query.content,
                byte_start: 0,
                byte_end: len,
                metadata: HashMap::from([("mock".into(), "true".into())]),
            }])
        }
    }

    #[tokio::test]
    async fn chunk_plugin_happy_path() {
        let plugin = MockChunker;
        let query = Query {
            content: "hello world".into(),
            mime_type: Some("text/plain".into()),
            target_size: 512,
            overlap: 64,
        };
        let chunks = plugin.chunk(query).await.unwrap();
        assert_eq!(chunks.len(), 1);
        assert_eq!(chunks[0].text, "hello world");
        assert_eq!(chunks[0].byte_start, 0);
        assert_eq!(chunks[0].byte_end, 11);
        assert_eq!(chunks[0].metadata.get("mock"), Some(&"true".into()));
    }

    #[tokio::test]
    async fn chunk_plugin_error_path() {
        let plugin = MockChunker;
        let query = Query {
            content: "".into(),
            mime_type: None,
            target_size: 512,
            overlap: 64,
        };
        let err = plugin.chunk(query).await.unwrap_err();
        assert!(matches!(err, ChunkPluginError::Parse { .. }));
    }

    #[test]
    fn chunk_serde_round_trip() {
        let chunk = Chunk {
            text: "test chunk".into(),
            byte_start: 0,
            byte_end: 10,
            metadata: HashMap::from([("key".into(), "value".into())]),
        };
        let json = serde_json::to_string(&chunk).unwrap();
        let decoded: Chunk = serde_json::from_str(&json).unwrap();
        assert_eq!(decoded.text, chunk.text);
        assert_eq!(decoded.byte_start, chunk.byte_start);
        assert_eq!(decoded.byte_end, chunk.byte_end);
        assert_eq!(decoded.metadata, chunk.metadata);
    }

    #[test]
    fn query_serde_round_trip() {
        let query = Query {
            content: "some document".into(),
            mime_type: Some("text/markdown".into()),
            target_size: 256,
            overlap: 32,
        };
        let json = serde_json::to_string(&query).unwrap();
        let decoded: Query = serde_json::from_str(&json).unwrap();
        assert_eq!(decoded.content, query.content);
        assert_eq!(decoded.mime_type, query.mime_type);
        assert_eq!(decoded.target_size, query.target_size);
    }

    #[test]
    fn chunk_plugin_error_serde_round_trip() {
        let err = ChunkPluginError::Io {
            message: "disk full".into(),
        };
        let json = serde_json::to_string(&err).unwrap();
        let decoded: ChunkPluginError = serde_json::from_str(&json).unwrap();
        let msg = decoded.to_string();
        assert!(msg.contains("disk full"));
    }

    #[test]
    fn trait_object_usable_as_box() {
        let _plugin: Box<dyn ChunkPlugin> = Box::new(MockChunker);
    }
}
