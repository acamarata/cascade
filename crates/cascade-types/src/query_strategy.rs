//! QueryStrategy trait and associated types.
//!
//! A `QueryStrategy` transforms a raw user query into one or more expanded
//! queries that are then used to drive retrieval. Different strategies trade
//! off latency, recall, and precision.

use crate::error::Result;
use async_trait::async_trait;
use serde::{Deserialize, Serialize};

// ── StrategyKind ──────────────────────────────────────────────────────────────

/// The query expansion strategies supported by Cascade.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
#[non_exhaustive]
pub enum StrategyKind {
    /// No expansion — use the raw query as-is for dense vector search.
    PureVector,

    /// No expansion — use the raw query for BM25 full-text search only.
    PureFts,

    /// Run both dense and BM25 searches and merge with Reciprocal Rank Fusion.
    HybridRrf,

    /// Hypothetical Document Embedding: generate a synthetic answer, embed it,
    /// and retrieve documents similar to the hypothetical answer.
    Hyde,

    /// Step-back prompting: ask a broader question to retrieve background
    /// knowledge before answering the specific query.
    StepBack,

    /// Multi-query: generate N paraphrased variants of the query and union
    /// the result sets before deduplication.
    MultiQuery,

    /// Parent-document: retrieve child chunks but return the surrounding parent
    /// chunk for richer context.
    ParentDoc,

    /// Self-querying: extract structured filters from the natural-language query
    /// and apply them as metadata filters before vector search.
    SelfQuery,
}

impl StrategyKind {
    /// Returns the config key string for this strategy.
    pub fn config_key(&self) -> &'static str {
        match self {
            Self::PureVector => "pure-vector",
            Self::PureFts => "pure-fts",
            Self::HybridRrf => "hybrid-rrf",
            Self::Hyde => "hyde",
            Self::StepBack => "step-back",
            Self::MultiQuery => "multi-query",
            Self::ParentDoc => "parent-doc",
            Self::SelfQuery => "self-query",
        }
    }

    /// Returns `true` if this strategy requires an LLM call to expand the query.
    pub fn requires_llm(&self) -> bool {
        matches!(
            self,
            Self::Hyde | Self::StepBack | Self::MultiQuery | Self::SelfQuery
        )
    }
}

impl std::fmt::Display for StrategyKind {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.config_key())
    }
}

// ── ExpandedQuery ─────────────────────────────────────────────────────────────

/// The result of query expansion: one or more query strings to use for retrieval.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ExpandedQuery {
    /// The expanded query strings.
    ///
    /// For `PureVector` and `PureFts`, this is just `[original_query]`.
    /// For `MultiQuery`, this contains the original plus N paraphrases.
    /// For `Hyde`, this contains the hypothetical document text.
    pub queries: Vec<String>,

    /// Optional structured metadata filters extracted from the query
    /// (populated by `SelfQuery` strategy).
    pub filters: Option<QueryFilters>,

    /// The strategy that produced this expansion.
    pub strategy: StrategyKind,
}

/// Structured metadata filters extracted from a natural-language query.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct QueryFilters {
    /// Cascade tier filter (e.g. restrict to `gci` or `prc` only).
    pub tier: Option<String>,

    /// File path glob filter (e.g. `"*.md"` or `"src/**"`).
    pub path_glob: Option<String>,

    /// Arbitrary key-value metadata filters.
    pub extra: std::collections::HashMap<String, serde_json::Value>,
}

// ── Trait ─────────────────────────────────────────────────────────────────────

/// Transforms a raw user query into one or more retrieval queries.
///
/// The returned [`ExpandedQuery`] is consumed by the retrieval layer.
///
/// # Example
///
/// ```rust,no_run
/// # use cascade_types::query_strategy::{QueryStrategy, ExpandedQuery, StrategyKind};
/// # use cascade_types::error::Result;
/// # use async_trait::async_trait;
/// struct MultiQueryStrategy;
///
/// #[async_trait]
/// impl QueryStrategy for MultiQueryStrategy {
///     fn kind(&self) -> StrategyKind { StrategyKind::MultiQuery }
///
///     async fn expand(&self, query: &str) -> Result<ExpandedQuery> {
///         // call LLM to generate N paraphrases …
///         Ok(ExpandedQuery {
///             queries: vec![query.to_owned()],
///             filters: None,
///             strategy: StrategyKind::MultiQuery,
///         })
///     }
/// }
/// ```
#[async_trait]
pub trait QueryStrategy: Send + Sync {
    /// The strategy variant this implementation provides.
    fn kind(&self) -> StrategyKind;

    /// Expand `query` into one or more retrieval queries.
    ///
    /// For strategies that do not require LLM calls (`PureVector`, `PureFts`,
    /// `HybridRrf`), this is synchronous work wrapped in async for interface
    /// uniformity.
    async fn expand(&self, query: &str) -> Result<ExpandedQuery>;
}

// ── No-op implementations ─────────────────────────────────────────────────────

/// Pass-through strategy: returns the query unchanged.
#[derive(Debug, Default)]
pub struct PureVectorStrategy;

#[async_trait]
impl QueryStrategy for PureVectorStrategy {
    fn kind(&self) -> StrategyKind {
        StrategyKind::PureVector
    }

    async fn expand(&self, query: &str) -> Result<ExpandedQuery> {
        Ok(ExpandedQuery {
            queries: vec![query.to_owned()],
            filters: None,
            strategy: StrategyKind::PureVector,
        })
    }
}

fn _assert_object_safe(_: &dyn QueryStrategy) {}
