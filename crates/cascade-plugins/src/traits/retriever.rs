//! Retriever plugin trait — host-side plugin-facing API.
//!
//! Purpose: define the `RetrieverPlugin` trait that a WASM plugin must satisfy
//!   to be registered as a Retriever in the cascade-plugins host. I/O types use
//!   serde-able JSON shapes for the WASM ABI.
//! Inputs: `RetrieveQuery` — query string, top-k limit, optional filters.
//! Outputs: `Vec<RetrievalResult>` — score + text + chunk reference per hit.
//! Constraints: all public types are `Serialize + Deserialize`; trait must be
//!   object-safe; `Send + Sync + 'static` bounds required for async dispatch.
//! SPORT: cascade-plugins / retriever plugin trait (T-P4-E03-01)

use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use thiserror::Error;

// ── I/O shapes ────────────────────────────────────────────────────────────────

/// Plugin-facing request for a retrieve operation.
///
/// `filters` is an open key/value map so individual retriever plugins can define
/// their own filter vocabulary without a fixed enum list.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RetrieveQuery {
    /// Natural-language query string.
    pub query_text: String,

    /// Maximum number of results to return.
    #[serde(default = "default_top_k")]
    pub top_k: usize,

    /// Optional key/value filters applied before scoring.
    /// Keys are plugin-defined (e.g. `"lang"`, `"tier"`, `"source"`).
    #[serde(default)]
    pub filters: HashMap<String, String>,
}

fn default_top_k() -> usize {
    10
}

/// A single retrieval result returned by a `RetrieverPlugin`.
///
/// `score` is in the range `[0.0, 1.0]` where `1.0` is most relevant.
/// The scale is implementation-defined; callers should use it only for ordering.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RetrievalResult {
    /// Raw relevance score (plugin-defined scale; higher = more relevant).
    pub score: f32,

    /// The text content of the matching chunk.
    pub text: String,

    /// Opaque chunk identifier (plugin-scoped; used for deduplication and provenance).
    pub chunk_id: String,

    /// Optional source file path (relative or absolute, as the plugin chooses).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub source_path: Option<String>,

    /// Optional provenance metadata (e.g. `"line_start"`, `"tier"`).
    #[serde(default)]
    pub metadata: HashMap<String, String>,
}

// ── Error type ────────────────────────────────────────────────────────────────

/// Errors that a `RetrieverPlugin` may return.
#[derive(Debug, Error, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum RetrieverPluginError {
    /// The plugin encountered an I/O failure reading the index.
    #[error("IO error in retriever plugin: {message}")]
    Io { message: String },

    /// The query could not be parsed (e.g. invalid filter syntax).
    #[error("parse error in retriever plugin: {message}")]
    Parse { message: String },

    /// The plugin is temporarily unavailable (index loading, cold start).
    #[error("retriever plugin unavailable: {message}")]
    Unavailable { message: String },
}

// ── Capability declarations ───────────────────────────────────────────────────

/// Capability declaration for a `RetrieverPlugin`.
///
/// Retriever plugins may request large memory (e.g. for in-memory ONNX models or
/// corpus caches). The host clamps to `MEM_LARGE_MB` (256 MiB by default).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RetrieverPluginCapabilities {
    /// Whether this plugin requires filesystem read access (e.g. FAISS index file).
    #[serde(default)]
    pub needs_fs_read: bool,

    /// Whether this plugin requires outbound network access (e.g. remote index API).
    #[serde(default)]
    pub needs_net_outbound: bool,

    /// Requested WASM memory in MiB. Host may cap to `MEM_LARGE_MB` (256).
    #[serde(default = "default_retriever_memory_mb")]
    pub max_memory_mb: u32,
}

fn default_retriever_memory_mb() -> u32 {
    256
}

impl Default for RetrieverPluginCapabilities {
    fn default() -> Self {
        Self {
            needs_fs_read: false,
            needs_net_outbound: false,
            max_memory_mb: default_retriever_memory_mb(),
        }
    }
}

// ── Trait ─────────────────────────────────────────────────────────────────────

/// Host-side trait a WASM retriever plugin must satisfy.
///
/// # Object-safety
///
/// This trait is object-safe. The async methods use `async_trait` to box futures.
///
/// # Example (mock implementation for tests)
///
/// ```rust
/// # use cascade_plugins::traits::retriever::{RetrieverPlugin, RetrieverPluginCapabilities, RetrieverPluginError, RetrievalResult, RetrieveQuery};
/// # use async_trait::async_trait;
/// struct EchoRetriever;
///
/// #[async_trait]
/// impl RetrieverPlugin for EchoRetriever {
///     fn name(&self) -> &str { "echo-retriever" }
///     fn capabilities(&self) -> RetrieverPluginCapabilities { Default::default() }
///
///     async fn retrieve(&self, query: RetrieveQuery) -> Result<Vec<RetrievalResult>, RetrieverPluginError> {
///         Ok(vec![RetrievalResult {
///             score: 1.0,
///             text: query.query_text.clone(),
///             chunk_id: "echo-0".into(),
///             source_path: None,
///             metadata: Default::default(),
///         }])
///     }
/// }
/// ```
#[async_trait]
pub trait RetrieverPlugin: Send + Sync + 'static {
    /// A stable, human-readable name for this plugin.
    fn name(&self) -> &str;

    /// Capability declaration — inspected at plugin registration time.
    fn capabilities(&self) -> RetrieverPluginCapabilities;

    /// Retrieve the top hits for `query.query_text`.
    ///
    /// Results must be ordered by `score` descending (highest relevance first).
    /// The returned vec length must not exceed `query.top_k`.
    async fn retrieve(
        &self,
        query: RetrieveQuery,
    ) -> Result<Vec<RetrievalResult>, RetrieverPluginError>;

    /// Returns `true` if the underlying index is ready to serve queries.
    ///
    /// The host calls this on startup to determine whether the plugin can serve
    /// immediately or needs a warm-up period.
    async fn is_ready(&self) -> bool {
        true
    }
}

// ── Object-safety static assertion ───────────────────────────────────────────

fn _assert_object_safe(_: &dyn RetrieverPlugin) {}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    struct MockRetriever;

    #[async_trait]
    impl RetrieverPlugin for MockRetriever {
        fn name(&self) -> &str {
            "mock-retriever"
        }

        fn capabilities(&self) -> RetrieverPluginCapabilities {
            Default::default()
        }

        async fn retrieve(
            &self,
            query: RetrieveQuery,
        ) -> Result<Vec<RetrievalResult>, RetrieverPluginError> {
            if query.query_text.is_empty() {
                return Err(RetrieverPluginError::Parse {
                    message: "empty query".into(),
                });
            }
            Ok(vec![RetrievalResult {
                score: 0.9,
                text: format!("result for: {}", query.query_text),
                chunk_id: "mock-0".into(),
                source_path: Some("/mock/file.md".into()),
                metadata: HashMap::from([("tier".into(), "gci".into())]),
            }])
        }
    }

    #[tokio::test]
    async fn retriever_plugin_happy_path() {
        let plugin = MockRetriever;
        let query = RetrieveQuery {
            query_text: "how to configure cascade".into(),
            top_k: 5,
            filters: HashMap::new(),
        };
        let results = plugin.retrieve(query).await.unwrap();
        assert_eq!(results.len(), 1);
        assert_eq!(results[0].score, 0.9);
        assert!(results[0].text.contains("how to configure cascade"));
        assert_eq!(results[0].chunk_id, "mock-0");
    }

    #[tokio::test]
    async fn retriever_plugin_error_path() {
        let plugin = MockRetriever;
        let query = RetrieveQuery {
            query_text: "".into(),
            top_k: 5,
            filters: HashMap::new(),
        };
        let err = plugin.retrieve(query).await.unwrap_err();
        assert!(matches!(err, RetrieverPluginError::Parse { .. }));
    }

    #[test]
    fn retrieve_query_serde_round_trip() {
        let query = RetrieveQuery {
            query_text: "test query".into(),
            top_k: 10,
            filters: HashMap::from([("lang".into(), "rust".into())]),
        };
        let json = serde_json::to_string(&query).unwrap();
        let decoded: RetrieveQuery = serde_json::from_str(&json).unwrap();
        assert_eq!(decoded.query_text, query.query_text);
        assert_eq!(decoded.top_k, query.top_k);
        assert_eq!(decoded.filters.get("lang"), Some(&"rust".into()));
    }

    #[test]
    fn retrieval_result_serde_round_trip() {
        let result = RetrievalResult {
            score: 0.75,
            text: "some chunk text".into(),
            chunk_id: "abc-123".into(),
            source_path: Some("/path/to/file.md".into()),
            metadata: HashMap::from([("tier".into(), "prc".into())]),
        };
        let json = serde_json::to_string(&result).unwrap();
        let decoded: RetrievalResult = serde_json::from_str(&json).unwrap();
        assert!((decoded.score - result.score).abs() < 1e-6);
        assert_eq!(decoded.chunk_id, result.chunk_id);
        assert_eq!(decoded.metadata.get("tier"), Some(&"prc".into()));
    }

    #[test]
    fn retriever_error_serde_round_trip() {
        let err = RetrieverPluginError::Unavailable {
            message: "index loading".into(),
        };
        let json = serde_json::to_string(&err).unwrap();
        let decoded: RetrieverPluginError = serde_json::from_str(&json).unwrap();
        let msg = decoded.to_string();
        assert!(msg.contains("index loading"));
    }

    #[test]
    fn trait_object_usable_as_box() {
        let _plugin: Box<dyn RetrieverPlugin> = Box::new(MockRetriever);
    }
}
