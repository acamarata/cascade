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
    agent::{Agent, AgentMeta, AgentResponse, Context, Tier},
    chunker::{Chunk, ChunkOpts, Chunker, Document},
    embedding_provider::{EmbedOpts, Embedding, EmbeddingProvider},
    error::{CascadeError, Result},
    parser::{Parser, ParserKind},
    query_strategy::{ExpandedQuery, QueryStrategy, StrategyKind},
    reranker::{RerankOpts, RerankResult, Reranker},
    retriever::{RetrievalHit, RetrieveOpts, Retriever},
};
use std::sync::Arc;

use crate::sandbox::WasmPlugin;

// ── Error conversion helpers ──────────────────────────────────────────────────

/// Convert a `SandboxError` into the `CascadeError::Boxed` variant so `?` works
/// in all dispatcher impls without requiring `From<SandboxError> for CascadeError`
/// on the types crate (which must stay vendor-neutral).
fn sandbox_err(e: crate::sandbox::SandboxError) -> CascadeError {
    CascadeError::Boxed(Box::new(e))
}

fn json_err(e: serde_json::Error) -> CascadeError {
    CascadeError::Boxed(Box::new(e))
}

// ── Chunker dispatcher ────────────────────────────────────────────────────────

/// Dispatches `Chunker` trait calls through the WASM ABI.
pub struct WasmChunker {
    plugin: Arc<WasmPlugin>,
}

impl WasmChunker {
    /// Construct a new WASM-backed chunker dispatcher.
    pub fn new(plugin: Arc<WasmPlugin>) -> Self {
        Self { plugin }
    }
}

#[async_trait]
impl Chunker for WasmChunker {
    async fn chunk(&self, doc: &Document, opts: &ChunkOpts) -> Result<Vec<Chunk>> {
        let input = serde_json::json!({ "doc": doc, "opts": opts });
        let result = self
            .plugin
            .call_export("chunker_chunk", &input)
            .await
            .map_err(sandbox_err)?;
        serde_json::from_value(result).map_err(json_err)
    }
}

// ── Retriever dispatcher ──────────────────────────────────────────────────────

/// Dispatches `Retriever` trait calls through the WASM ABI.
pub struct WasmRetriever {
    plugin: Arc<WasmPlugin>,
}

impl WasmRetriever {
    /// Construct a new WASM-backed retriever dispatcher.
    pub fn new(plugin: Arc<WasmPlugin>) -> Self {
        Self { plugin }
    }
}

#[async_trait]
impl Retriever for WasmRetriever {
    async fn retrieve(&self, query: &str, opts: &RetrieveOpts) -> Result<Vec<RetrievalHit>> {
        let input = serde_json::json!({ "query": query, "opts": opts });
        let result = self
            .plugin
            .call_export("retriever_retrieve", &input)
            .await
            .map_err(sandbox_err)?;
        serde_json::from_value(result).map_err(json_err)
    }
}

// ── EmbeddingProvider dispatcher ──────────────────────────────────────────────

/// Dispatches `EmbeddingProvider` trait calls through the WASM ABI.
pub struct WasmEmbeddingProvider {
    plugin: Arc<WasmPlugin>,
}

impl WasmEmbeddingProvider {
    /// Construct a new WASM-backed embedding provider dispatcher.
    pub fn new(plugin: Arc<WasmPlugin>) -> Self {
        Self { plugin }
    }
}

#[async_trait]
impl EmbeddingProvider for WasmEmbeddingProvider {
    async fn embed(&self, texts: &[&str], opts: &EmbedOpts) -> Result<Vec<Embedding>> {
        let input = serde_json::json!({ "texts": texts, "opts": opts });
        let result = self
            .plugin
            .call_export("provider_embed", &input)
            .await
            .map_err(sandbox_err)?;
        serde_json::from_value(result).map_err(json_err)
    }

    fn dimension(&self) -> usize {
        // WHY: WASM plugins declare their embedding dimension in the manifest;
        // the sandbox reads it at load time. Stub returns 0 until the manifest
        // accessor is wired (P4 deliverable).
        self.plugin.declared_dimension().unwrap_or(0)
    }
}

// ── Agent dispatcher ──────────────────────────────────────────────────────────

/// Dispatches `Agent` trait calls through the WASM ABI.
pub struct WasmAgent {
    plugin: Arc<WasmPlugin>,
}

impl WasmAgent {
    /// Construct a new WASM-backed agent dispatcher.
    pub fn new(plugin: Arc<WasmPlugin>) -> Self {
        Self { plugin }
    }
}

#[async_trait]
impl Agent for WasmAgent {
    fn tier(&self) -> Tier {
        // WHY: WASM agents run at T3 by default (bounded, cheap work).
        // A future manifest field can override this; T3 is the safe default.
        Tier::T3
    }

    async fn handle(&self, prompt: &str, ctx: &Context) -> Result<AgentResponse> {
        let payload = serde_json::json!({ "prompt": prompt, "ctx": ctx });
        let result = self
            .plugin
            .call_export("agent_handle", &payload)
            .await
            .map_err(sandbox_err)?;
        serde_json::from_value::<AgentResponse>(result).map_err(json_err)
    }

    fn name(&self) -> &str {
        "wasm-agent"
    }
}

// ── Parser dispatcher ─────────────────────────────────────────────────────────

/// Dispatches `Parser` trait calls through the WASM ABI.
pub struct WasmParser {
    plugin: Arc<WasmPlugin>,
}

impl WasmParser {
    /// Construct a new WASM-backed parser dispatcher.
    pub fn new(plugin: Arc<WasmPlugin>) -> Self {
        Self { plugin }
    }
}

#[async_trait]
impl Parser for WasmParser {
    fn kind(&self) -> ParserKind {
        // WHY: plugins declare their parser kind in the manifest; the sandbox
        // reads it at load time. PlainText is the safe fallback until the
        // manifest accessor is wired (P4 deliverable).
        ParserKind::PlainText
    }

    async fn parse(&self, path: &std::path::Path) -> Result<Document> {
        let input = serde_json::json!({ "path": path.to_string_lossy() });
        let result = self
            .plugin
            .call_export("parser_parse", &input)
            .await
            .map_err(sandbox_err)?;
        serde_json::from_value(result).map_err(json_err)
    }
}

// ── Reranker dispatcher ───────────────────────────────────────────────────────

/// Dispatches `Reranker` trait calls through the WASM ABI.
pub struct WasmReranker {
    plugin: Arc<WasmPlugin>,
}

impl WasmReranker {
    /// Construct a new WASM-backed reranker dispatcher.
    pub fn new(plugin: Arc<WasmPlugin>) -> Self {
        Self { plugin }
    }
}

#[async_trait]
impl Reranker for WasmReranker {
    async fn rerank(
        &self,
        query: &str,
        candidates: &[Chunk],
        opts: &RerankOpts,
    ) -> Result<Vec<RerankResult>> {
        let input = serde_json::json!({ "query": query, "candidates": candidates, "opts": opts });
        let result = self
            .plugin
            .call_export("reranker_rerank", &input)
            .await
            .map_err(sandbox_err)?;
        serde_json::from_value(result).map_err(json_err)
    }
}

// ── QueryStrategy dispatcher ──────────────────────────────────────────────────

/// Dispatches `QueryStrategy` trait calls through the WASM ABI.
pub struct WasmQueryStrategy {
    plugin: Arc<WasmPlugin>,
}

impl WasmQueryStrategy {
    /// Construct a new WASM-backed query strategy dispatcher.
    pub fn new(plugin: Arc<WasmPlugin>) -> Self {
        Self { plugin }
    }
}

#[async_trait]
impl QueryStrategy for WasmQueryStrategy {
    fn kind(&self) -> StrategyKind {
        // WHY: plugins declare their strategy kind in the manifest; stub returns
        // PureVector (identity pass-through) until the accessor is wired (P4).
        StrategyKind::PureVector
    }

    async fn expand(&self, query: &str) -> Result<ExpandedQuery> {
        let input = serde_json::json!({ "query": query });
        let result = self
            .plugin
            .call_export("strategy_expand", &input)
            .await
            .map_err(sandbox_err)?;
        serde_json::from_value(result).map_err(json_err)
    }
}

// ── Unused import suppression (AgentMeta used via AgentResponse default) ─────
// WHY: AgentMeta is imported because AgentResponse carries it; the dispatcher
// does not construct it directly (the WASM plugin returns JSON).
fn _use_agent_meta(_: AgentMeta) {}
