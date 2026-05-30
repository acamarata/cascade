//! Plugin trait dispatchers — bridges cascade-types interfaces to WASM ABI calls.
//!
//! Purpose: each dispatcher holds a loaded `WasmPlugin` and implements the
//!   corresponding cascade-types trait by serialising the call args to the ABI
//!   wire format, invoking the WASM function, and deserialising the result.
//! Inputs: `WasmPlugin` handle (sandbox + store) + typed args per trait method.
//! Outputs: same `Result<T>` that the cascade-types trait contract requires.
//! Constraints: all cross-boundary values are length-prefixed byte slices;
//!   no Rust-specific types cross the WASM boundary.
//! SPORT: cascade-plugins / dispatcher layer

use async_trait::async_trait;
use cascade_types::{
    agent::{Agent, AgentInput, AgentOutput},
    chunker::{ChunkOpts, Chunker, Document},
    embedding_provider::{EmbedOpts, EmbeddingProvider},
    error::Result,
    parser::Parser,
    query_strategy::QueryStrategy,
    reranker::Reranker,
    retriever::{RetrievalQuery, RetrievalResult, Retriever},
};
use std::sync::Arc;

use crate::sandbox::WasmPlugin;

// ── Chunker dispatcher ────────────────────────────────────────────────────────

/// Dispatches `Chunker` trait calls through the WASM ABI.
pub struct WasmChunker {
    plugin: Arc<WasmPlugin>,
}

impl WasmChunker {
    pub fn new(plugin: Arc<WasmPlugin>) -> Self {
        Self { plugin }
    }
}

#[async_trait]
impl Chunker for WasmChunker {
    async fn chunk(&self, doc: &Document, opts: &ChunkOpts) -> Result<Vec<cascade_types::chunker::Chunk>> {
        let input = serde_json::json!({ "doc": doc, "opts": opts });
        let result = self.plugin.call_export("chunker_chunk", &input).await?;
        Ok(serde_json::from_value(result)?)
    }
}

// ── Retriever dispatcher ──────────────────────────────────────────────────────

/// Dispatches `Retriever` trait calls through the WASM ABI.
pub struct WasmRetriever {
    plugin: Arc<WasmPlugin>,
}

impl WasmRetriever {
    pub fn new(plugin: Arc<WasmPlugin>) -> Self {
        Self { plugin }
    }
}

#[async_trait]
impl Retriever for WasmRetriever {
    async fn retrieve(&self, query: &RetrievalQuery) -> Result<RetrievalResult> {
        let input = serde_json::json!({ "query": query });
        let result = self.plugin.call_export("retriever_retrieve", &input).await?;
        Ok(serde_json::from_value(result)?)
    }
}

// ── EmbeddingProvider dispatcher ──────────────────────────────────────────────

/// Dispatches `EmbeddingProvider` trait calls through the WASM ABI.
pub struct WasmEmbeddingProvider {
    plugin: Arc<WasmPlugin>,
}

impl WasmEmbeddingProvider {
    pub fn new(plugin: Arc<WasmPlugin>) -> Self {
        Self { plugin }
    }
}

#[async_trait]
impl EmbeddingProvider for WasmEmbeddingProvider {
    async fn embed(&self, texts: &[String], opts: &EmbedOpts) -> Result<Vec<Vec<f32>>> {
        let input = serde_json::json!({ "texts": texts, "opts": opts });
        let result = self.plugin.call_export("provider_embed", &input).await?;
        Ok(serde_json::from_value(result)?)
    }
}

// ── Agent dispatcher ──────────────────────────────────────────────────────────

/// Dispatches `Agent` trait calls through the WASM ABI.
pub struct WasmAgent {
    plugin: Arc<WasmPlugin>,
}

impl WasmAgent {
    pub fn new(plugin: Arc<WasmPlugin>) -> Self {
        Self { plugin }
    }
}

#[async_trait]
impl Agent for WasmAgent {
    async fn run(&self, input: AgentInput) -> Result<AgentOutput> {
        let payload = serde_json::json!({ "input": input });
        let result = self.plugin.call_export("agent_run", &payload).await?;
        Ok(serde_json::from_value(result)?)
    }
}

// ── Parser dispatcher ─────────────────────────────────────────────────────────

/// Dispatches `Parser` trait calls through the WASM ABI.
pub struct WasmParser {
    plugin: Arc<WasmPlugin>,
}

impl WasmParser {
    pub fn new(plugin: Arc<WasmPlugin>) -> Self {
        Self { plugin }
    }
}

#[async_trait]
impl Parser for WasmParser {
    async fn parse(&self, path: &std::path::Path) -> Result<Document> {
        let input = serde_json::json!({ "path": path.to_string_lossy() });
        let result = self.plugin.call_export("parser_parse", &input).await?;
        Ok(serde_json::from_value(result)?)
    }

    fn supports_extension(&self, ext: &str) -> bool {
        // Synchronous ABI call — plugins declare supported extensions at load time.
        self.plugin.declared_extensions().contains(&ext.to_ascii_lowercase())
    }
}

// ── Reranker dispatcher ───────────────────────────────────────────────────────

/// Dispatches `Reranker` trait calls through the WASM ABI.
pub struct WasmReranker {
    plugin: Arc<WasmPlugin>,
}

impl WasmReranker {
    pub fn new(plugin: Arc<WasmPlugin>) -> Self {
        Self { plugin }
    }
}

#[async_trait]
impl Reranker for WasmReranker {
    async fn rerank(
        &self,
        query: &str,
        hits: Vec<cascade_types::reranker::ScoredChunk>,
    ) -> Result<Vec<cascade_types::reranker::ScoredChunk>> {
        let input = serde_json::json!({ "query": query, "hits": hits });
        let result = self.plugin.call_export("reranker_rerank", &input).await?;
        Ok(serde_json::from_value(result)?)
    }
}

// ── QueryStrategy dispatcher ──────────────────────────────────────────────────

/// Dispatches `QueryStrategy` trait calls through the WASM ABI.
pub struct WasmQueryStrategy {
    plugin: Arc<WasmPlugin>,
}

impl WasmQueryStrategy {
    pub fn new(plugin: Arc<WasmPlugin>) -> Self {
        Self { plugin }
    }
}

#[async_trait]
impl QueryStrategy for WasmQueryStrategy {
    async fn expand(&self, query: &str) -> Result<Vec<String>> {
        let input = serde_json::json!({ "query": query });
        let result = self.plugin.call_export("strategy_expand", &input).await?;
        Ok(serde_json::from_value(result)?)
    }
}
