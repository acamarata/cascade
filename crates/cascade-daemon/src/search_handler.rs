//! # search_handler
//!
//! IPC method handlers for the `rag.*` method family.
//!
//! ## Purpose
//!
//! Exposes four JSON-RPC methods over the daemon's Unix socket so that cascade-app,
//! cascade-cli, and MCP clients can drive the RAG pipeline without importing
//! cascade-rag directly:
//!
//! | Method             | Action                                        |
//! |--------------------|-----------------------------------------------|
//! | `rag.search`       | FTS5 + optional dense vector search via RRF   |
//! | `rag.ingest_file`  | Ingest a single file into the project index   |
//! | `rag.list_sources` | List all indexed sources for a project        |
//! | `rag.index_stats`  | Return file/chunk counts + last-updated time  |
//!
//! ## Connection strategy
//!
//! `IndexManager` owns a single `Mutex<Connection>` for its project; it does not
//! expose that connection to callers.  Instead, each `rag.*` handler opens a
//! **separate read connection** directly from `IndexManager::db_path()`.  SQLite
//! WAL mode allows unlimited concurrent readers against the same database file
//! without blocking the writer.  `ingest_file` opens a write connection — it
//! holds it only for the duration of the ingest and releases it immediately.
//!
//! ## Inputs
//!
//! All methods receive JSON params whose shape is documented on each handler below.
//!
//! ## Outputs
//!
//! Each method returns a JSON `result` object on success, or a typed JSON-RPC
//! error (`code` + `message`) on failure.  No method panics on a missing or
//! empty index — empty inputs produce empty outputs.
//!
//! ## Constraints
//!
//! - The `IndexRegistry` mutex is held **only during the map lookup** (O(1)); it
//!   is released before any blocking DB work begins.
//! - `search()` calls `tokio::task::spawn_blocking` internally (rusqlite is sync);
//!   all four handlers are safe to call from any async context.
//! - Embedder injection: the `EmbedModel` passed at construction is reused for
//!   every search call. Production: Multilingual E5 Large via the legacy
//!   `bge-m3` compatibility key; tests: `MockEmbedModel`.
//!
//! ## Ticket
//!
//! T-P4-E01-29
//!
//! SPORT: MASTER-CRATES.md → cascade-daemon::search_handler

use std::path::PathBuf;
use std::sync::Arc;

use rusqlite::Connection;
use serde::{Deserialize, Serialize};
use tokio::sync::Mutex;
use tokio::time::Instant;
use tracing::{debug, instrument};

use async_trait::async_trait;
use cascade_providers::{CompletionRequest, ProviderAdapter};
use cascade_rag::embed::EmbedModel;
use cascade_rag::index_manager::IndexRegistry;
use cascade_rag::ingest::{IngestConfig, IngestPipeline, IngestResult};
use cascade_rag::rerank::bge::{BgeReranker, BgeRerankerOptions};
use cascade_rag::rerank::Reranker;
use cascade_rag::search::{search, HydeLlm, RerankerModelConfig, SearchConfig};
use cascade_rag::{RagCitation, SourceInfo};
use cascade_types::error::{CascadeError, Result as CascadeResult};

// ── ProviderHydeLlm ───────────────────────────────────────────────────────────

/// [`HydeLlm`] implementation that delegates to the daemon's [`ProviderAdapter`].
///
/// # Purpose
///
/// Bridges the `cascade-rag` `HydeLlm` trait to the `cascade-providers`
/// `ProviderAdapter` trait so the daemon can inject a real LLM into the HyDE
/// query-expansion channel without introducing a `cascade-providers` dependency
/// inside `cascade-rag`.
///
/// # Prompt
///
/// Sends a one-shot user message asking the model to write a short hypothetical
/// passage that would answer the query.  The response text is used as the dense
/// query vector instead of the raw query, bridging vocabulary gaps.
///
/// # Fallback
///
/// On any provider error, `generate_hypothetical` returns `Err`.  The caller
/// (`cascade_rag::search`) handles the fallback to the raw query transparently.
///
/// # Constraints
///
/// - `max_tokens: Some(256)` — HyDE passages need only be 1–3 sentences; cap
///   prevents runaway costs on pay-per-token providers.
/// - `stream: false` — the full response must arrive before embedding begins.
// HyDE LLM bridge — injected into RagSearchHandler at startup when a provider is available.
#[allow(dead_code)]
pub struct ProviderHydeLlm {
    adapter: Arc<dyn ProviderAdapter>,
    model: String,
}

impl ProviderHydeLlm {
    /// Create a new `ProviderHydeLlm`.
    ///
    /// - `adapter` — any registered [`ProviderAdapter`]; production typically
    ///   uses the Chat-class provider resolved by the routing table.
    /// - `model` — model identifier passed through to the provider (e.g.
    ///   `"claude-3-haiku-20240307"`).
    // Constructor — called at daemon startup to wire in HyDE query expansion.
    #[allow(dead_code)]
    pub fn new(adapter: Arc<dyn ProviderAdapter>, model: impl Into<String>) -> Self {
        Self {
            adapter,
            model: model.into(),
        }
    }
}

#[async_trait]
impl HydeLlm for ProviderHydeLlm {
    async fn generate_hypothetical(&self, query: &str) -> CascadeResult<String> {
        let prompt = format!(
            "Write a short hypothetical passage (1–3 sentences) that would directly answer \
             the following question. Return only the passage, no preamble:\n\n{query}"
        );
        let req = CompletionRequest {
            model: self.model.clone(),
            messages: vec![cascade_providers::Message::user(prompt)],
            max_tokens: Some(256),
            temperature: Some(0.3),
            stream: false,
            system: None,
        };
        let resp = self
            .adapter
            .complete(req)
            .await
            .map_err(|e| CascadeError::Other(format!("HyDE provider error: {e}")))?;
        Ok(resp.content)
    }
}

// ── Request / response shapes ─────────────────────────────────────────────────

/// Params for `rag.search`.
#[derive(Debug, Deserialize)]
pub struct RagSearchParams {
    /// User query string.
    pub query: String,
    /// Absolute path to the project root (selects the `IndexManager`).
    pub project_root: String,
    /// Number of results to return. Defaults to 10.
    #[serde(default = "default_k")]
    pub k: u32,
    /// Optional per-call search config overrides.
    #[serde(default)]
    pub config: RagSearchConfigOverride,
}

fn default_k() -> u32 {
    10
}

/// Caller-supplied overrides for [`SearchConfig`] fields.
#[derive(Debug, Deserialize)]
pub struct RagSearchConfigOverride {
    /// Enable FTS5 BM25 keyword retrieval tier. Defaults to `true`.
    #[serde(default = "bool_true")]
    pub fts5_enabled: bool,
    /// Enable dense vector KNN retrieval tier. Defaults to `false`.
    #[serde(default)]
    pub vec_enabled: bool,
    /// Enable cross-encoder reranking. Defaults to `false`.
    #[serde(default)]
    pub rerank_enabled: bool,
    /// Enable the ColBERT/multi-vec late-interaction retrieval channel.
    ///
    /// `None` (the default, i.e. the key is absent from the request) defers to
    /// the daemon-global `[rag] multi_vec` setting from `config.toml`.
    /// `Some(true)`/`Some(false)` always win over the global, so an explicit
    /// `false` disables the channel even when the global default is on.
    ///
    /// Only effective when compiled with the `rag-multivec` feature AND token
    /// embeddings exist in the index.  The 3-channel RRF (fts5/dense/sparse)
    /// is unchanged when the resolved value is `false`.
    ///
    /// Wire format is unchanged from the pre-`Option` era: `colbert_enabled:
    /// true` / `colbert_enabled: false` parse exactly as before; only the
    /// meaning of an absent key changed (used to mean `false`, now means
    /// "defer to global" — which is `false` unless the install opted in).
    #[serde(default)]
    pub colbert_enabled: Option<bool>,
}

/// Hand-written so `Default` agrees with the serde defaults above.
///
/// WHY NOT `#[derive(Default)]`: `fts5_enabled` carries
/// `#[serde(default = "bool_true")]`, which applies ONLY when deserializing.
/// A derived `Default` would give `false`, so Rust-side callers using
/// `RagSearchConfigOverride::default()` silently got FTS5 DISABLED while every
/// deserialized request got it enabled — contradicting this type's own doc
/// comment ("Defaults to `true`") and producing empty search results that look
/// like an indexing bug. Keep this impl in sync with the serde attributes.
impl Default for RagSearchConfigOverride {
    fn default() -> Self {
        Self {
            fts5_enabled: bool_true(),
            vec_enabled: false,
            rerank_enabled: false,
            colbert_enabled: None,
        }
    }
}

fn bool_true() -> bool {
    true
}

/// Response for `rag.search`.
#[derive(Debug, Serialize)]
pub struct RagSearchResponse {
    /// Ordered citations (highest RRF score first).
    pub citations: Vec<RagCitation>,
    /// Wall-clock time for the full retrieval pipeline.
    pub duration_ms: u64,
}

/// Params for `rag.ingest_file`.
#[derive(Debug, Deserialize)]
pub struct RagIngestParams {
    /// Absolute path to the file to ingest.
    pub path: String,
    /// Project root — used to locate the correct `IndexManager`.
    pub project_root: String,
}

/// Serialisable version of [`IngestResult`].
#[derive(Debug, Serialize)]
pub struct RagIngestResponse {
    pub source_id: i64,
    pub chunks_created: usize,
    pub skipped: bool,
}

impl From<IngestResult> for RagIngestResponse {
    fn from(r: IngestResult) -> Self {
        Self {
            source_id: r.source_id,
            chunks_created: r.chunks_created,
            skipped: r.skipped,
        }
    }
}

/// Params for `rag.list_sources`.
#[derive(Debug, Deserialize)]
pub struct RagListSourcesParams {
    pub project_root: String,
}

/// Response for `rag.list_sources`.
#[derive(Debug, Serialize)]
pub struct RagListSourcesResponse {
    pub sources: Vec<SourceInfo>,
}

/// Params for `rag.index_stats`.
#[derive(Debug, Deserialize)]
pub struct RagIndexStatsParams {
    pub project_root: String,
}

/// Response for `rag.index_stats`.
#[derive(Debug, Serialize)]
pub struct RagIndexStatsResponse {
    /// Number of indexed source files.
    pub file_count: u64,
    /// Total number of indexed chunks across all source files.
    pub chunk_count: u64,
    /// Size of the SQLite database file in bytes.
    pub index_size_bytes: u64,
    /// Unix timestamp (seconds) of the most recent source update, if any.
    pub last_updated: Option<i64>,
}

// ── Handler ───────────────────────────────────────────────────────────────────

/// Shared handler state for the `rag.*` IPC method family.
///
/// # Purpose
///
/// Holds the [`IndexRegistry`] (lazy per-project `IndexManager` pool), the
/// [`EmbedModel`] used for dense query embedding, and an optional pre-built
/// [`Reranker`] for cross-encoder reranking.  All fields are `Send + Sync +
/// 'static` and share-able across connection tasks via `Arc<RagSearchHandler>`.
///
/// # Inputs
///
/// Constructed once at daemon startup via `RagSearchHandler::new(registry,
/// embed, multi_vec)`.  `multi_vec` is the daemon-global `[rag] multi_vec`
/// default resolved from `config.toml` (see [`load_multi_vec_default`]).
/// Call `RagSearchHandler::new_with_reranker` to supply a pre-built reranker.
///
/// # Constraints
///
/// The `IndexRegistry` mutex is held only during map lookup (O(1)); it is
/// released before any blocking DB work begins.
///
/// SPORT: MASTER-CRATES.md → cascade-daemon::search_handler::RagSearchHandler
pub struct RagSearchHandler {
    registry: Arc<IndexRegistry>,
    embed: Arc<dyn EmbedModel>,
    /// Daemon-global default for the ColBERT/multi-vec channel, resolved from
    /// `[rag] multi_vec` in `config.toml` at startup.  Used when a request
    /// does not specify `colbert_enabled`.  Defaults to `false` so existing
    /// installs see no retrieval change on upgrade.
    multi_vec: bool,
    /// Cross-encoder reranker, swappable at runtime.  `None` → reranking is
    /// skipped.  Wrapped in `RwLock` so a background task can load the ~580 MB
    /// BGE reranker model off the startup critical path and swap it in once
    /// ready (see [`spawn_load_reranker`](Self::spawn_load_reranker)).
    reranker: std::sync::RwLock<Option<Arc<dyn Reranker>>>,
    /// Optional HyDE LLM for query expansion.  `None` → HyDE is disabled even
    /// when `SearchConfig::hyde_enabled` is set.  Set by
    /// [`RagSearchHandler::with_hyde_llm`] after construction.
    hyde_llm: Option<Arc<dyn HydeLlm>>,
}

impl RagSearchHandler {
    /// Create a new handler without a reranker.
    ///
    /// `embed` is injected so callers can pass `MockEmbedModel` in tests and a
    /// real Multilingual E5 Large model in production. `multi_vec` is the
    /// daemon-global `[rag] multi_vec` default (see [`load_multi_vec_default`]);
    /// it becomes the `colbert_enabled` default for requests that do not set
    /// the field explicitly. Reranking is disabled; call
    /// [`spawn_load_reranker`](Self::spawn_load_reranker) to load it in the
    /// background, or use [`new_with_reranker`](Self::new_with_reranker) to
    /// supply a pre-built one.
    pub fn new(
        registry: Arc<IndexRegistry>,
        embed: Arc<dyn EmbedModel>,
        multi_vec: bool,
    ) -> Arc<Self> {
        Arc::new(Self {
            registry,
            embed,
            multi_vec,
            reranker: std::sync::RwLock::new(None),
            hyde_llm: None,
        })
    }

    /// Create a new handler with a pre-built reranker.
    ///
    /// `multi_vec` carries the same daemon-global `[rag] multi_vec` default as
    /// [`new`](Self::new).
    ///
    /// When `config.rerank_enabled = true` in a search call and this handler
    /// has a reranker, it is passed to [`search`] for cross-encoder re-scoring.
    // Used in integration tests and when a preloaded BGE reranker is available at startup.
    #[allow(dead_code)]
    pub fn new_with_reranker(
        registry: Arc<IndexRegistry>,
        embed: Arc<dyn EmbedModel>,
        multi_vec: bool,
        reranker: Arc<dyn Reranker>,
    ) -> Arc<Self> {
        Arc::new(Self {
            registry,
            embed,
            multi_vec,
            reranker: std::sync::RwLock::new(Some(reranker)),
            hyde_llm: None,
        })
    }

    /// Attach a [`HydeLlm`] to this handler.
    ///
    /// When a `ProviderHydeLlm` (or any other implementation) is attached,
    /// `handle_search` will pass it into `search()` as the HyDE channel
    /// whenever `SearchConfig::hyde_enabled` is also `true`.
    ///
    /// HyDE is **off by default** — call this method at startup only when a
    /// provider adapter is confirmed available.
    // Wired at startup (T-P4-E01-29) when a Chat-class provider is resolved.
    #[allow(dead_code)]
    pub fn with_hyde_llm(self: Arc<Self>, llm: Arc<dyn HydeLlm>) -> Arc<Self> {
        // SAFETY: we are the sole Arc owner at construction time (called right
        // after `new()`), so get_mut is guaranteed to succeed.
        // If multiple Arcs already exist (tests), fall back to a re-wrap.
        match Arc::try_unwrap(self) {
            Ok(mut inner) => {
                inner.hyde_llm = Some(llm);
                Arc::new(inner)
            }
            Err(existing) => {
                // Already shared — return as-is (HyDE silently stays off).
                // In production this path is never taken because with_hyde_llm
                // is called during the single-threaded startup sequence.
                tracing::warn!(
                    "RagSearchHandler::with_hyde_llm called after Arc was shared; \
                     HyDE LLM not installed"
                );
                existing
            }
        }
    }

    /// Spawn a background task that builds the BGE Reranker V2-M3 and swaps it in.
    ///
    /// Returns immediately so daemon startup is never blocked by the ~580 MB
    /// reranker model load.  Until the load completes, search calls run without
    /// reranking.  On load failure the handler keeps `None` — reranking stays
    /// disabled, no panic.
    ///
    /// # Requires
    ///
    /// A tokio runtime must be active (uses `tokio::spawn`).
    pub fn spawn_load_reranker(self: &Arc<Self>) {
        let this = Arc::clone(self);
        tokio::spawn(async move {
            let opts = BgeRerankerOptions::default();
            match BgeReranker::new(opts).await {
                Ok(rr) => {
                    if let Ok(mut guard) = this.reranker.write() {
                        *guard = Some(Arc::new(rr));
                        tracing::info!("RAG: BGE reranker loaded and installed");
                    } else {
                        tracing::warn!("RAG: reranker lock poisoned; reranking stays disabled");
                    }
                }
                Err(e) => {
                    tracing::warn!("RAG: BGE reranker init failed (reranking disabled): {e}");
                }
            }
        });
    }

    // ── rag.search ────────────────────────────────────────────────────────────

    /// Build the effective [`SearchConfig`] for a `rag.search` request.
    ///
    /// Resolution rule for the multi-vec channel (T-P7-E20-30): a per-request
    /// `colbert_enabled` (`Some(true)`/`Some(false)`) always wins; when the
    /// request leaves it absent (`None`), the daemon-global `[rag] multi_vec`
    /// setting captured at construction is the default.  The global itself
    /// defaults to `false`, so absent key + unconfigured install resolves to
    /// `false` — identical to pre-`Option` behaviour.
    fn search_config_for(&self, params: &RagSearchParams) -> SearchConfig {
        SearchConfig {
            k: params.k as usize,
            fts5_enabled: params.config.fts5_enabled,
            vec_enabled: params.config.vec_enabled,
            rerank_enabled: params.config.rerank_enabled,
            reranker_model: RerankerModelConfig::Bge,
            colbert_enabled: params.config.colbert_enabled.unwrap_or(self.multi_vec),
            ..Default::default()
        }
    }

    /// Handle `rag.search`.
    ///
    /// Opens a fresh read connection to the project's SQLite index (WAL mode
    /// allows this alongside the `IndexManager`'s own writer connection), then
    /// delegates to `cascade_rag::search()`.
    ///
    /// Returns `Ok(RagSearchResponse)` on success.  An empty query or empty
    /// index produces `citations: []` without error.
    #[instrument(skip(self), fields(query = %params.query, project = %params.project_root))]
    pub async fn handle_search(
        &self,
        params: RagSearchParams,
    ) -> Result<RagSearchResponse, String> {
        if params.project_root.is_empty() {
            return Err("project_root must not be empty".into());
        }

        let project_root = PathBuf::from(&params.project_root);
        let mgr = self
            .registry
            .get_or_open(&project_root)
            .await
            .map_err(|e| format!("open index: {e}"))?;

        let db_path = mgr.db_path().to_path_buf();

        // Open a fresh read connection — WAL allows concurrent readers.
        let conn = tokio::task::spawn_blocking(move || {
            let c = cascade_db::open_configured(&db_path)
                .map_err(|e| format!("open read conn: {e}"))?;
            Ok::<Connection, String>(c)
        })
        .await
        .map_err(|e| format!("spawn_blocking: {e}"))??;

        let conn_arc: Arc<Mutex<Connection>> = Arc::new(Mutex::new(conn));
        let config = self.search_config_for(&params);

        // Build the reranker arg: pass Some(reranker) only when rerank is
        // requested AND a reranker has been installed (loaded in the background).
        let reranker_arg: Option<Arc<dyn Reranker>> = if config.rerank_enabled {
            self.reranker
                .read()
                .ok()
                .and_then(|g| g.as_ref().map(Arc::clone))
        } else {
            None
        };

        let embed = Arc::clone(&self.embed);

        // Pass the HyDE LLM when one is installed AND vec search is enabled.
        // HyDE only helps the dense channel; without vec_enabled it is a no-op.
        let hyde_arg: Option<Arc<dyn HydeLlm>> = if config.vec_enabled {
            self.hyde_llm.as_ref().map(Arc::clone)
        } else {
            None
        };

        let t0 = Instant::now();

        let citations = search(
            &params.query,
            &config,
            conn_arc,
            embed,
            reranker_arg,
            hyde_arg,
        )
        .await
        .map_err(|e| format!("search: {e}"))?;

        let duration_ms = t0.elapsed().as_millis() as u64;
        debug!(hits = citations.len(), duration_ms, "rag.search complete");

        Ok(RagSearchResponse {
            citations,
            duration_ms,
        })
    }

    // ── rag.ingest_file ───────────────────────────────────────────────────────

    /// Handle `rag.ingest_file`.
    ///
    /// Runs the ingest pipeline for a single file path.  Idempotent: if the
    /// file content hash is unchanged, returns `skipped: true`.
    pub async fn handle_ingest_file(
        &self,
        params: RagIngestParams,
    ) -> Result<RagIngestResponse, String> {
        if params.path.is_empty() {
            return Err("path must not be empty".into());
        }

        let file_path = PathBuf::from(&params.path);
        let project_root = PathBuf::from(&params.project_root);
        let mgr = self
            .registry
            .get_or_open(&project_root)
            .await
            .map_err(|e| format!("open index: {e}"))?;

        let db_path = mgr.db_path().to_path_buf();
        let embed = Arc::clone(&self.embed);

        let result = tokio::task::spawn_blocking(move || {
            let conn = cascade_db::open_configured(&db_path).map_err(|e| {
                cascade_types::error::CascadeError::Other(format!("open write conn: {e}"))
            })?;
            let pipeline = IngestPipeline::new(conn, embed, IngestConfig::default());
            pipeline.ingest_file(&file_path)
        })
        .await
        .map_err(|e| format!("spawn_blocking: {e}"))?
        .map_err(|e| format!("ingest: {e}"))?;

        Ok(RagIngestResponse::from(result))
    }

    // ── rag.list_sources ──────────────────────────────────────────────────────

    /// Handle `rag.list_sources`.
    ///
    /// Delegates to `IndexManager::list_sources()` which queries `rag_sources`.
    pub async fn handle_list_sources(
        &self,
        params: RagListSourcesParams,
    ) -> Result<RagListSourcesResponse, String> {
        let project_root = PathBuf::from(&params.project_root);
        let mgr = self
            .registry
            .get_or_open(&project_root)
            .await
            .map_err(|e| format!("open index: {e}"))?;

        let sources = mgr
            .list_sources()
            .await
            .map_err(|e| format!("list_sources: {e}"))?;

        Ok(RagListSourcesResponse { sources })
    }

    // ── rag.index_stats ───────────────────────────────────────────────────────

    /// Handle `rag.index_stats`.
    ///
    /// Opens a fresh read connection to query aggregate counts and the DB file
    /// size.  Returns zero counts for an absent or empty index — never errors.
    pub async fn handle_index_stats(
        &self,
        params: RagIndexStatsParams,
    ) -> Result<RagIndexStatsResponse, String> {
        let project_root = PathBuf::from(&params.project_root);
        let mgr = self
            .registry
            .get_or_open(&project_root)
            .await
            .map_err(|e| format!("open index: {e}"))?;

        let db_path = mgr.db_path().to_path_buf();

        let stats = tokio::task::spawn_blocking(move || {
            let conn = cascade_db::open_configured(&db_path)
                .map_err(|e| format!("open stats conn: {e}"))?;

            let file_count: u64 = conn
                .query_row("SELECT COUNT(*) FROM rag_sources", [], |r| r.get(0))
                .unwrap_or(0);

            let chunk_count: u64 = conn
                .query_row("SELECT COUNT(*) FROM rag_chunks", [], |r| r.get(0))
                .unwrap_or(0);

            let last_updated: Option<i64> = conn
                .query_row("SELECT MAX(indexed_at) FROM rag_sources", [], |r| r.get(0))
                .unwrap_or(None);

            let index_size_bytes = std::fs::metadata(&db_path).map(|m| m.len()).unwrap_or(0);

            Ok::<RagIndexStatsResponse, String>(RagIndexStatsResponse {
                file_count,
                chunk_count,
                index_size_bytes,
                last_updated,
            })
        })
        .await
        .map_err(|e| format!("spawn_blocking: {e}"))??;

        Ok(stats)
    }
}

// ── Daemon-global multi_vec resolution ────────────────────────────────────────

/// Resolve the daemon-global `[rag] multi_vec` default from `config.toml`.
///
/// # Purpose
///
/// Bridges `RagConfig.multi_vec` (cascade-types, persisted by
/// `cascade config set rag.multi_vec <bool>` into `~/.cascade/config.toml`)
/// into [`RagSearchHandler`] so the setting actually gates the ColBERT
/// channel instead of being written but never read (T-P7-E20-30).
///
/// # Fallback
///
/// The file is read through the `CascadeConfig` schema; sections the daemon's
/// own config view owns (`[budget]`, `[daemon]`, …) are ignored, and vice
/// versa.  An absent or unparseable file resolves to
/// `CascadeConfig::default().rag.multi_vec == false` — the daemon must never
/// fail to start over this, and existing installs see no retrieval change.
///
/// # Constraints
///
/// Read once at handler construction (daemon startup); later `config set`
/// changes take effect after the next daemon restart.
pub(crate) fn load_multi_vec_default(config_dir: &std::path::Path) -> bool {
    std::fs::read_to_string(config_dir.join("config.toml"))
        .ok()
        .and_then(|raw| toml::from_str::<cascade_types::config::CascadeConfig>(&raw).ok())
        .unwrap_or_default()
        .rag
        .multi_vec
}

// ── Dispatch helper ───────────────────────────────────────────────────────────

/// Route a `rag.*` method call to the appropriate handler.
///
/// # Purpose
///
/// Used by `ipc::try_typed_dispatch` to keep the match arm for `rag.*` methods
/// out of the main dispatch fn.
///
/// # Inputs
///
/// - `handler` — shared `RagSearchHandler` reference
/// - `method`  — method name string (e.g. `"rag.search"`)
/// - `params`  — optional JSON params value from the JSON-RPC envelope
///
/// # Outputs
///
/// `Ok(serde_json::Value)` on success, `Err((code, message))` on failure.
/// `code` follows standard JSON-RPC conventions: -32602 bad params,
/// -32001 handler error, -32601 method not found.
///
/// SPORT: MASTER-CRATES.md → cascade-daemon::search_handler::dispatch_rag
pub async fn dispatch_rag(
    handler: &RagSearchHandler,
    method: &str,
    params: Option<serde_json::Value>,
) -> Result<serde_json::Value, (i32, String)> {
    let params = params.unwrap_or(serde_json::Value::Object(Default::default()));

    match method {
        "rag.search" => {
            let p: RagSearchParams = serde_json::from_value(params)
                .map_err(|e| (-32602_i32, format!("invalid params: {e}")))?;
            handler
                .handle_search(p)
                .await
                .map_err(|e| (-32001_i32, e))
                .and_then(|r| serde_json::to_value(r).map_err(|e| (-32603_i32, e.to_string())))
        }
        "rag.ingest_file" => {
            let p: RagIngestParams = serde_json::from_value(params)
                .map_err(|e| (-32602_i32, format!("invalid params: {e}")))?;
            handler
                .handle_ingest_file(p)
                .await
                .map_err(|e| (-32001_i32, e))
                .and_then(|r| serde_json::to_value(r).map_err(|e| (-32603_i32, e.to_string())))
        }
        "rag.list_sources" => {
            let p: RagListSourcesParams = serde_json::from_value(params)
                .map_err(|e| (-32602_i32, format!("invalid params: {e}")))?;
            handler
                .handle_list_sources(p)
                .await
                .map_err(|e| (-32001_i32, e))
                .and_then(|r| serde_json::to_value(r).map_err(|e| (-32603_i32, e.to_string())))
        }
        "rag.index_stats" => {
            let p: RagIndexStatsParams = serde_json::from_value(params)
                .map_err(|e| (-32602_i32, format!("invalid params: {e}")))?;
            handler
                .handle_index_stats(p)
                .await
                .map_err(|e| (-32001_i32, e))
                .and_then(|r| serde_json::to_value(r).map_err(|e| (-32603_i32, e.to_string())))
        }
        other => Err((-32601_i32, format!("method not found: {other}"))),
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_rag::embed::MockEmbedModel;
    use tempfile::TempDir;

    fn make_handler() -> Arc<RagSearchHandler> {
        make_handler_with_multi_vec(false)
    }

    /// Handler whose daemon-global `[rag] multi_vec` default is `multi_vec`.
    fn make_handler_with_multi_vec(multi_vec: bool) -> Arc<RagSearchHandler> {
        let registry = IndexRegistry::new();
        let embed: Arc<dyn EmbedModel> = Arc::new(MockEmbedModel::new(1024));
        RagSearchHandler::new(registry, embed, multi_vec)
    }

    /// Build several handlers that SHARE one `IndexRegistry`.
    ///
    /// WHY: `make_handler_with_multi_vec` calls `IndexRegistry::new()` each time,
    /// so handlers built with it never see each other's ingested data. Any test
    /// that ingests with one handler and searches with another must share the
    /// registry or it silently searches an empty index.
    fn make_handlers_sharing_registry(multi_vec_flags: &[bool]) -> Vec<Arc<RagSearchHandler>> {
        let registry = IndexRegistry::new();
        multi_vec_flags
            .iter()
            .map(|&mv| {
                let embed: Arc<dyn EmbedModel> = Arc::new(MockEmbedModel::new(1024));
                RagSearchHandler::new(Arc::clone(&registry), embed, mv)
            })
            .collect()
    }

    // ── rag.search — empty index returns empty citations ──────────────────────

    /// An empty index for a fresh project root should return zero citations without error.
    #[tokio::test]
    async fn search_empty_index_returns_empty_citations() {
        let tmp = TempDir::new().unwrap();
        let handler = make_handler();
        let project_root = tmp.path().to_str().unwrap().to_string();

        let params = RagSearchParams {
            query: "hello world".into(),
            project_root,
            k: 5,
            config: RagSearchConfigOverride::default(),
        };
        let result = handler.handle_search(params).await.unwrap();
        assert!(
            result.citations.is_empty(),
            "empty index should return no citations"
        );
    }

    // ── rag.search — empty query returns empty citations ──────────────────────

    /// A whitespace-only query should return an empty result set (search() early-out).
    #[tokio::test]
    async fn search_empty_query_returns_empty() {
        let tmp = TempDir::new().unwrap();
        let handler = make_handler();
        let project_root = tmp.path().to_str().unwrap().to_string();

        let params = RagSearchParams {
            query: "   ".into(),
            project_root,
            k: 10,
            config: RagSearchConfigOverride::default(),
        };
        let result = handler.handle_search(params).await.unwrap();
        assert!(
            result.citations.is_empty(),
            "whitespace query → no citations"
        );
    }

    // ── rag.search — empty project_root returns error ─────────────────────────

    /// An empty `project_root` is rejected immediately with a descriptive error.
    #[tokio::test]
    async fn search_empty_project_root_returns_error() {
        let handler = make_handler();
        let params = RagSearchParams {
            query: "test".into(),
            project_root: "".into(),
            k: 5,
            config: RagSearchConfigOverride::default(),
        };
        let result = handler.handle_search(params).await;
        assert!(result.is_err(), "empty project_root must return an error");
        let msg = result.unwrap_err();
        assert!(
            msg.contains("project_root"),
            "error should mention project_root; got: {msg}"
        );
    }

    // ── rag.list_sources — empty index returns empty list ─────────────────────

    /// A fresh index has no sources.
    #[tokio::test]
    async fn list_sources_empty_returns_empty() {
        let tmp = TempDir::new().unwrap();
        let handler = make_handler();
        let project_root = tmp.path().to_str().unwrap().to_string();

        let result = handler
            .handle_list_sources(RagListSourcesParams { project_root })
            .await
            .unwrap();
        assert!(result.sources.is_empty());
    }

    // ── rag.index_stats — empty index returns zero counts ────────────────────

    /// A fresh index should report zero files, chunks, and no last_updated timestamp.
    #[tokio::test]
    async fn index_stats_empty_returns_zeros() {
        let tmp = TempDir::new().unwrap();
        let handler = make_handler();
        let project_root = tmp.path().to_str().unwrap().to_string();

        let result = handler
            .handle_index_stats(RagIndexStatsParams { project_root })
            .await
            .unwrap();
        assert_eq!(result.file_count, 0, "file_count must be 0 for fresh index");
        assert_eq!(
            result.chunk_count, 0,
            "chunk_count must be 0 for fresh index"
        );
        assert!(
            result.last_updated.is_none(),
            "last_updated must be None for fresh index"
        );
    }

    // ── dispatch_rag — error mapping ─────────────────────────────────────────

    /// An unknown `rag.*` method should return JSON-RPC -32601 (method not found).
    #[tokio::test]
    async fn dispatch_unknown_method_returns_method_not_found() {
        let tmp = TempDir::new().unwrap();
        let handler = make_handler();
        let _ = tmp;

        let result = dispatch_rag(&handler, "rag.nonexistent", None).await;
        assert!(result.is_err(), "unknown method must error");
        let (code, _msg) = result.unwrap_err();
        assert_eq!(code, -32601, "unknown method must return -32601");
    }

    // ── dispatch_rag — bad params returns -32602 ──────────────────────────────

    /// Malformed params (wrong type for `k`) should return -32602 (invalid params).
    #[tokio::test]
    async fn dispatch_bad_params_returns_invalid_params() {
        let tmp = TempDir::new().unwrap();
        let handler = make_handler();
        let _ = tmp;

        // Pass `k` as a string instead of u32 — should fail deserialization.
        let bad_params = serde_json::json!({
            "query": "test",
            "project_root": "/tmp/fake",
            "k": "not-a-number"
        });
        let result = dispatch_rag(&handler, "rag.search", Some(bad_params)).await;
        assert!(result.is_err());
        let (code, _) = result.unwrap_err();
        assert_eq!(code, -32602);
    }

    // ── RagSearchConfigOverride::colbert_enabled defaults (T-P7-E20-29) ──────

    /// `colbert_enabled` defaults to `None` (defer to the daemon-global
    /// `[rag] multi_vec` setting, itself `false` by default), so the channel
    /// stays opt-in only for existing installs.
    #[test]
    fn config_override_colbert_enabled_defaults_false() {
        let ov = RagSearchConfigOverride::default();
        assert!(
            ov.colbert_enabled.is_none(),
            "colbert_enabled must default to None so the global default (false) applies"
        );
    }

    /// Deserialising a params JSON that lacks `colbert_enabled` still succeeds
    /// and the field is `None` (defer to global).
    #[test]
    fn config_override_colbert_absent_deserialises_as_false() {
        let json = serde_json::json!({"fts5_enabled": true});
        let ov: RagSearchConfigOverride = serde_json::from_value(json).unwrap();
        assert!(
            ov.colbert_enabled.is_none(),
            "absent colbert_enabled must deserialise to None (defer to global)"
        );
    }

    /// Deserialising with `colbert_enabled: true` round-trips correctly.
    #[test]
    fn config_override_colbert_enabled_true_preserved() {
        let json = serde_json::json!({"colbert_enabled": true});
        let ov: RagSearchConfigOverride = serde_json::from_value(json).unwrap();
        assert_eq!(
            ov.colbert_enabled,
            Some(true),
            "colbert_enabled: true must be preserved"
        );
    }

    /// Wire-format compatibility: an explicit `colbert_enabled: false` parses
    /// to `Some(false)` (NOT `None`), so it can beat a `true` global default.
    #[test]
    fn config_override_colbert_explicit_false_deserialises_as_some_false() {
        let json = serde_json::json!({"colbert_enabled": false});
        let ov: RagSearchConfigOverride = serde_json::from_value(json).unwrap();
        assert_eq!(
            ov.colbert_enabled,
            Some(false),
            "explicit colbert_enabled: false must deserialise to Some(false)"
        );
    }

    // ── [rag] multi_vec → colbert_enabled resolution (T-P7-E20-30) ───────────

    /// Global `multi_vec = true` + no per-request override ⇒ the effective
    /// `SearchConfig.colbert_enabled` is `true`, i.e. `cascade_rag::search`
    /// receives the flag that gates its ColBERT/multivec 4th channel
    /// (search.rs: `if config.colbert_enabled { compute_colbert_channel(...) }`).
    #[test]
    fn global_multi_vec_true_no_override_enables_colbert_channel() {
        let handler = make_handler_with_multi_vec(true);
        let params = RagSearchParams {
            query: "q".into(),
            project_root: "/tmp/x".into(),
            k: 5,
            config: RagSearchConfigOverride::default(),
        };
        let cfg = handler.search_config_for(&params);
        assert!(
            cfg.colbert_enabled,
            "global multi_vec=true with absent override must resolve colbert_enabled=true"
        );
    }

    /// Global `multi_vec = false` (the default) + no per-request override ⇒
    /// `colbert_enabled = false` — the 3-channel RRF (fts5/dense/sparse)
    /// behaviour is unchanged.
    #[test]
    fn global_multi_vec_false_no_override_keeps_colbert_disabled() {
        let handler = make_handler_with_multi_vec(false);
        let params = RagSearchParams {
            query: "q".into(),
            project_root: "/tmp/x".into(),
            k: 5,
            config: RagSearchConfigOverride::default(),
        };
        let cfg = handler.search_config_for(&params);
        assert!(
            !cfg.colbert_enabled,
            "global multi_vec=false with absent override must resolve colbert_enabled=false"
        );
    }

    /// An explicit per-request `false` wins over a `true` global default.
    #[test]
    fn explicit_false_wins_over_true_global() {
        let handler = make_handler_with_multi_vec(true);
        let params = RagSearchParams {
            query: "q".into(),
            project_root: "/tmp/x".into(),
            k: 5,
            config: RagSearchConfigOverride {
                colbert_enabled: Some(false),
                ..Default::default()
            },
        };
        let cfg = handler.search_config_for(&params);
        assert!(
            !cfg.colbert_enabled,
            "explicit per-request false must beat the global multi_vec=true default"
        );
    }

    /// An explicit per-request `true` wins over a `false` global default.
    #[test]
    fn explicit_true_wins_over_false_global() {
        let handler = make_handler_with_multi_vec(false);
        let params = RagSearchParams {
            query: "q".into(),
            project_root: "/tmp/x".into(),
            k: 5,
            config: RagSearchConfigOverride {
                colbert_enabled: Some(true),
                ..Default::default()
            },
        };
        let cfg = handler.search_config_for(&params);
        assert!(
            cfg.colbert_enabled,
            "explicit per-request true must beat the global multi_vec=false default"
        );
    }

    // ── load_multi_vec_default — config.toml → handler default ───────────────

    /// `load_multi_vec_default` reads `[rag] multi_vec = true` from the
    /// daemon's config.toml via the CascadeConfig schema.
    #[test]
    fn load_multi_vec_default_true_from_config_toml() {
        let tmp = TempDir::new().unwrap();
        std::fs::write(tmp.path().join("config.toml"), "[rag]\nmulti_vec = true\n").unwrap();
        assert!(
            load_multi_vec_default(tmp.path()),
            "[rag] multi_vec = true in config.toml must resolve to true"
        );
    }

    /// `load_multi_vec_default` is `false` when the file is absent (fresh
    /// install) and when the key is explicitly `false`.
    #[test]
    fn load_multi_vec_default_false_when_absent_or_unset() {
        let tmp = TempDir::new().unwrap();
        assert!(
            !load_multi_vec_default(tmp.path()),
            "absent config.toml must resolve multi_vec=false"
        );
        std::fs::write(tmp.path().join("config.toml"), "[rag]\nmulti_vec = false\n").unwrap();
        assert!(
            !load_multi_vec_default(tmp.path()),
            "explicit multi_vec = false must resolve to false"
        );
    }

    /// A config.toml with unrelated daemon sections (no `[rag]` table) resolves
    /// to `false` — the CascadeConfig view ignores the daemon-only sections and
    /// defaults the rag table.
    #[test]
    fn load_multi_vec_default_false_with_daemon_only_config() {
        let tmp = TempDir::new().unwrap();
        std::fs::write(
            tmp.path().join("config.toml"),
            "[daemon]\nlog_level = \"info\"\n\n[budget]\n",
        )
        .unwrap();
        assert!(
            !load_multi_vec_default(tmp.path()),
            "config.toml without a [rag] table must resolve multi_vec=false"
        );
    }

    // ── rag.search — multi_vec=true handler end-to-end (T-P7-E20-30) ─────────

    /// End-to-end: with an ingested file, a `multi_vec = true` handler (no
    /// per-request override) and a `multi_vec = false` handler return identical
    /// citation sets — enabling the global default alone does not change the
    /// 3-channel RRF results (the ColBERT channel is additive re-fusion; with
    /// no token embeddings in the index it contributes no new ranking), and
    /// searching with the flag on succeeds rather than erroring.
    #[tokio::test]
    async fn search_multi_vec_on_and_off_handlers_return_identical_citations() {
        let tmp = TempDir::new().unwrap();
        let project_root = tmp.path().to_str().unwrap().to_string();

        // Seed one markdown file with a unique term.
        let md = tmp.path().join("notes.md");
        std::fs::write(&md, "# Notes\n\nThe zephyr cascade glides over the lake.\n").unwrap();

        // All three handlers must share one registry, or the searches below query
        // an empty index and the assertions fail for the wrong reason.
        let handlers = make_handlers_sharing_registry(&[true, true, false]);
        let ingest_handler = Arc::clone(&handlers[0]);
        ingest_handler
            .handle_ingest_file(RagIngestParams {
                path: md.to_str().unwrap().to_string(),
                project_root: project_root.clone(),
            })
            .await
            .unwrap();

        let search_params = |config: RagSearchConfigOverride| RagSearchParams {
            query: "zephyr".into(),
            project_root: project_root.clone(),
            k: 5,
            config,
        };

        let on = handlers[1]
            .handle_search(search_params(RagSearchConfigOverride::default()))
            .await
            .unwrap();
        let off = handlers[2]
            .handle_search(search_params(RagSearchConfigOverride::default()))
            .await
            .unwrap();

        assert!(!on.citations.is_empty(), "fts5 must find the seeded term");
        assert!(!off.citations.is_empty(), "fts5 must find the seeded term");

        let ids_on: Vec<(i64, f64)> = on
            .citations
            .iter()
            .map(|c| (c.chunk_id, c.rrf_score))
            .collect();
        let ids_off: Vec<(i64, f64)> = off
            .citations
            .iter()
            .map(|c| (c.chunk_id, c.rrf_score))
            .collect();
        assert_eq!(
            ids_on, ids_off,
            "multi_vec global default must not alter the 3-channel RRF ranking"
        );
    }
}
