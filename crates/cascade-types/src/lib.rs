//! # cascade-types
//!
//! Core traits, types, and constants for the Cascade AI context framework.
//!
//! This crate is the single shared foundation that all other Cascade crates
//! depend on. It defines:
//!
//! - **8 cross-cutting traits** (`Chunker`, `Retriever`, `Parser`,
//!   `EmbeddingProvider`, `Reranker`, `QueryStrategy`, `Agent`, `KeyStorage`)
//! - **Cascade tier enums** (`CascadeTier`, `InboxTier`)
//! - **Canonical path constants** (`paths` module)
//! - **Unified config schema** (`config` module)
//! - **Error type** (`CascadeError` + `Result<T>` alias)
//!
//! ## Design invariants
//!
//! 1. **Zero vendor coupling.** No model names, no provider SDKs. All provider
//!    references use the `ProviderKind` enum or configuration strings.
//! 2. **No circular dependencies.** This crate depends on `serde`,
//!    `async-trait`, `thiserror`, `tokio` (minimal), and nothing else in the
//!    Cascade workspace. It is the DAG root.
//! 3. **Object-safe traits.** All eight traits are confirmed object-safe via
//!    the `tests/traits_compile.rs` integration test; callers can store them
//!    as `Box<dyn Trait>`.
//! 4. **`Send + Sync`** on all traits enables use inside `tokio::spawn` and
//!    multi-threaded async executors without wrapping in `Arc<Mutex<…>>`.
//!
//! ## Trait overview
//!
//! | Trait | Purpose |
//! |-------|---------|
//! | [`Chunker`] | Split a `Document` into `Chunk`s |
//! | [`Retriever`] | Query an index; return `RetrievalHit`s |
//! | [`Parser`] | Read a file; produce a `Document` |
//! | [`EmbeddingProvider`] | Embed text strings into float vectors |
//! | [`Reranker`] | Re-score candidate chunks with a cross-encoder |
//! | [`QueryStrategy`] | Expand a user query into retrieval queries |
//! | [`Agent`] | Process a prompt + context; return a response |
//! | [`KeyStorage`] | Store/retrieve secret key material |
//!
//! ## Example
//!
//! ```rust,no_run
//! use cascade_types::chunker::{Chunker, NoopChunker, Document, ChunkOpts};
//! use cascade_types::retriever::{Retriever, NoopRetriever};
//! use cascade_types::error::Result;
//!
//! # async fn example() -> Result<()> {
//! let chunker = NoopChunker;
//! let doc = Document::from_text("Hello, cascade!");
//! let chunks = chunker.chunk(&doc, &ChunkOpts::default()).await?;
//! println!("{} chunk(s) produced", chunks.len());
//! # Ok(())
//! # }
//! ```

// ── Module declarations ───────────────────────────────────────────────────────

pub mod accounts;
pub mod agent;
pub mod background_task;
pub mod auto_auth;
pub mod cascade_tier;
pub mod chunker;
pub mod codec;
pub mod config;
pub mod embedding_provider;
pub mod error;
pub mod harness;
pub mod hook;
pub mod ipc;
pub mod key_storage;
pub mod mcp;
pub mod model_ids;
pub mod model_profile_types;
pub mod model_registry;
pub mod parser;
pub mod paths;
pub mod policy;
pub mod pool;
pub mod provision;
pub mod query_strategy;
pub mod quota_store;
pub mod rag;
pub mod reranker;
pub mod retriever;
pub mod project;
pub mod scheduled_task;
pub mod task;
pub mod template;
pub mod tiers;
pub mod usage;
pub mod usage_analytics;

// ── Top-level re-exports ──────────────────────────────────────────────────────
//
// Re-export the most commonly used items so callers can write
// `use cascade_types::{CascadeError, CascadeTier}` without navigating sub-modules.

pub use accounts::{
    Account, AccountFamily, AccountRole, AccountsRegistry, AccessMethod, ModelMatrixEntry,
    ModelRoute, TaskClass, ACCOUNTS_SCHEMA_VERSION,
};
pub use agent::{Agent, AgentMeta, AgentResponse, Context, NoopAgent, Tier};
pub use cascade_tier::{CascadeTier, InboxTier};
pub use chunker::{Chunk, ChunkMetadata, ChunkOpts, Chunker, Document, NoopChunker};
pub use config::{
    AiFolder, CascadeConfig, CONFIG_SCHEMA_VERSION, DaemonConfig, GfpConfig, HarnessConfig,
    HarnessMcpConfig, McpConfig, PluginConfig, ProviderConfig, RagConfig,
};
pub use embedding_provider::{
    EmbedOpts, EmbedUsage, Embedding, EmbeddingProvider, NoopEmbeddingProvider, ProviderKind,
};
pub use error::{CascadeError, Result};
pub use hook::HookConfigEntry;
pub use key_storage::{InMemoryKeyStorage, KeyInfo, KeyStorage, SecretBytes};
pub use parser::{ParserKind, PlainTextParser};
pub use paths::{
    daemon_pid, daemon_socket, global_cascade_dir, global_config, home_dir, project_cascade_dir,
    project_cascade_md, project_subdir, subdirs, AGENTS_MD_NAME, CASCADE_DIR_NAME, CASCADE_MD_NAME,
    CLAUDE_MD_NAME, DAEMON_PIPE_WIN,
};
pub use policy::{
    Decision, PolicyAction, PolicyResult, PolicyRule, PolicyScope, PolicySet, PolicySetEntry,
    PolicyTableConfig,
};
pub use query_strategy::{
    ExpandedQuery, PureVectorStrategy, QueryFilters, QueryStrategy, StrategyKind,
};
pub use quota_store::{
    AccountEntry, HistoryEntry, ModelUsage, QuotaState, QuotaStore, RateWindow,
    PROVIDER_CLAUDE_MAX, PROVIDER_GFP, PROVIDER_GOOGLE_AGY, PROVIDER_OC_GO,
    PROVIDER_OPENAI_CODEX, QUOTA_STORE_SCHEMA_VERSION,
};
pub use reranker::{NoopReranker, RerankOpts, RerankResult, Reranker};
pub use retriever::{NoopRetriever, RetrievalHit, RetrieveOpts, Retriever};
pub use task::{
    Task, TaskCreateParams, TaskCreateResult, TaskDeleteParams, TaskDeleteResult, TaskFilter,
    TaskGetParams, TaskGetResult, TaskListParams, TaskListResult, TaskMoveParams, TaskMoveResult,
    TaskPriority, TaskStatus, TaskUpdateParams, TaskUpdateResult,
};
pub use template::{
    ApplyResult, DiffResult, TemplateManifest, TemplateRecord, TemplateTier, UpgradeResult,
};
pub use project::{IndexStatus, ProjectRecord, ProjectType};
