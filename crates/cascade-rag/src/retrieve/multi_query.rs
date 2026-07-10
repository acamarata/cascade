//! MultiQuery and StepBack query expansion strategies.
//!
//! # Purpose
//!
//! Both strategies use an injected LLM (via [`QueryExpansionLlm`]) to rewrite a
//! user query into alternative forms before retrieval:
//!
//! - **MultiQuery**: generates N paraphrased variants of the original query and
//!   union-deduplicates their result sets, increasing recall by reducing
//!   sensitivity to exact wording.
//!
//! - **StepBack**: generates a single, broader "step-back" question whose answer
//!   provides background knowledge before the specific query is answered.  This
//!   improves retrieval of foundational concepts that the specific answer depends on.
//!
//! Both strategies fall back to returning the original query unchanged when no
//! LLM is configured (offline/test mode without a mock).
//!
//! # Inputs
//!
//! - `llm`: `Arc<dyn QueryExpansionLlm>` — injectable LLM (mock-able in tests).
//! - `n_variants`: number of paraphrases for MultiQuery (default 3, clamped 1–10).
//!
//! # Outputs
//!
//! `ExpandedQuery::queries` contains the original query plus all generated
//! variants (MultiQuery), or a single broadened query (StepBack).
//!
//! # Constraints
//!
//! - LLM failures are absorbed: the original query is returned on any error.
//! - Both strategies are `Send + Sync` and safe to share via `Arc`.
//!
//! # Ticket
//!
//! rag-09
//!
//! SPORT: MASTER-CRATES.md → cascade-rag::retrieve::multi_query

use std::sync::Arc;

use async_trait::async_trait;
use tracing::{debug, instrument, warn};

use cascade_types::{
    error::Result,
    query_strategy::{ExpandedQuery, QueryStrategy, StrategyKind},
};

// ── QueryExpansionLlm ─────────────────────────────────────────────────────────

/// Injectable LLM used by [`MultiQueryStrategy`] and [`StepBackStrategy`].
///
/// Abstracts over the concrete LLM backend so tests can inject a deterministic
/// mock, while production code can inject a real model without coupling the
/// retrieval crate to any particular inference library.
///
/// # Object safety
///
/// Object-safe; share via `Arc<dyn QueryExpansionLlm>`.
#[async_trait]
pub trait QueryExpansionLlm: Send + Sync {
    /// Generate `n` paraphrased variants of `query`.
    ///
    /// The returned `Vec` should have at most `n` items; the original query is
    /// NOT included (callers prepend it).  Return `Ok(vec![])` on partial
    /// failure so the caller gracefully falls back to the original alone.
    async fn paraphrase(&self, query: &str, n: usize) -> Result<Vec<String>>;

    /// Generate a single broader "step-back" version of `query`.
    ///
    /// Returns the broadened question, or `Err` on LLM failure.
    async fn step_back(&self, query: &str) -> Result<String>;
}

// ── MultiQueryStrategy ────────────────────────────────────────────────────────

/// Generates N paraphrased variants and unions their retrieval result sets.
///
/// The original query is always the first element of `ExpandedQuery::queries`
/// so that the user's exact intent is preserved in the retrieval pass.
pub struct MultiQueryStrategy {
    llm: Arc<dyn QueryExpansionLlm>,
    /// Number of paraphrase variants to generate (excluding the original).
    pub n_variants: usize,
}

impl std::fmt::Debug for MultiQueryStrategy {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("MultiQueryStrategy")
            .field("n_variants", &self.n_variants)
            .finish_non_exhaustive()
    }
}

impl MultiQueryStrategy {
    /// Create a new `MultiQueryStrategy`.
    ///
    /// # Inputs
    ///
    /// - `llm`: injectable LLM.
    /// - `n_variants`: paraphrase count (clamped to 1..=10).
    pub fn new(llm: Arc<dyn QueryExpansionLlm>, n_variants: usize) -> Self {
        Self {
            llm,
            n_variants: n_variants.clamp(1, 10),
        }
    }
}

#[async_trait]
impl QueryStrategy for MultiQueryStrategy {
    fn kind(&self) -> StrategyKind {
        StrategyKind::MultiQuery
    }

    /// Expand `query` into the original plus up to `n_variants` paraphrases.
    #[instrument(skip(self), fields(n_variants = self.n_variants))]
    async fn expand(&self, query: &str) -> Result<ExpandedQuery> {
        debug!(
            query,
            n = self.n_variants,
            "MultiQuery: generating paraphrases"
        );

        let mut queries = vec![query.to_string()];

        match self.llm.paraphrase(query, self.n_variants).await {
            Ok(variants) => {
                debug!(generated = variants.len(), "MultiQuery: variants ready");
                queries.extend(variants);
            }
            Err(e) => {
                warn!(error = %e, "MultiQuery: LLM failed; falling back to original query");
            }
        }

        Ok(ExpandedQuery {
            queries,
            filters: None,
            strategy: StrategyKind::MultiQuery,
        })
    }
}

// ── StepBackStrategy ──────────────────────────────────────────────────────────

/// Step-back prompting: broaden the query to retrieve background knowledge.
///
/// `ExpandedQuery::queries` contains a single string — the broadened question.
/// Callers may merge these results with a direct-query retrieval pass.
pub struct StepBackStrategy {
    llm: Arc<dyn QueryExpansionLlm>,
}

impl std::fmt::Debug for StepBackStrategy {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("StepBackStrategy").finish_non_exhaustive()
    }
}

impl StepBackStrategy {
    /// Create a new `StepBackStrategy`.
    pub fn new(llm: Arc<dyn QueryExpansionLlm>) -> Self {
        Self { llm }
    }
}

#[async_trait]
impl QueryStrategy for StepBackStrategy {
    fn kind(&self) -> StrategyKind {
        StrategyKind::StepBack
    }

    /// Expand `query` into a single broadened step-back question.
    ///
    /// Falls back to the original query on LLM failure or empty response.
    #[instrument(skip(self))]
    async fn expand(&self, query: &str) -> Result<ExpandedQuery> {
        debug!(query, "StepBack: generating broader question");

        let broadened = match self.llm.step_back(query).await {
            Ok(q) if !q.trim().is_empty() => {
                debug!(broadened = %q, "StepBack: broadened query ready");
                q
            }
            Ok(_) => {
                warn!("StepBack: LLM returned empty; using original");
                query.to_string()
            }
            Err(e) => {
                warn!(error = %e, "StepBack: LLM failed; using original query");
                query.to_string()
            }
        };

        Ok(ExpandedQuery {
            queries: vec![broadened],
            filters: None,
            strategy: StrategyKind::StepBack,
        })
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::error::CascadeError;

    // ── Mock LLM ──────────────────────────────────────────────────────────────

    struct MockExpansionLlm {
        paraphrase_fail: bool,
        step_back_fail: bool,
        empty_step_back: bool,
    }

    impl MockExpansionLlm {
        fn ok() -> Arc<Self> {
            Arc::new(Self {
                paraphrase_fail: false,
                step_back_fail: false,
                empty_step_back: false,
            })
        }
        fn fail_paraphrase() -> Arc<Self> {
            Arc::new(Self {
                paraphrase_fail: true,
                step_back_fail: false,
                empty_step_back: false,
            })
        }
        fn fail_step_back() -> Arc<Self> {
            Arc::new(Self {
                paraphrase_fail: false,
                step_back_fail: true,
                empty_step_back: false,
            })
        }
        fn empty_step_back() -> Arc<Self> {
            Arc::new(Self {
                paraphrase_fail: false,
                step_back_fail: false,
                empty_step_back: true,
            })
        }
    }

    #[async_trait]
    impl QueryExpansionLlm for MockExpansionLlm {
        async fn paraphrase(&self, query: &str, n: usize) -> Result<Vec<String>> {
            if self.paraphrase_fail {
                return Err(CascadeError::Other("mock paraphrase failure".into()));
            }
            Ok((0..n).map(|i| format!("variant_{i}: {query}")).collect())
        }

        async fn step_back(&self, query: &str) -> Result<String> {
            if self.step_back_fail {
                return Err(CascadeError::Other("mock step_back failure".into()));
            }
            if self.empty_step_back {
                return Ok(String::new());
            }
            Ok(format!("What is the broader context of: {query}"))
        }
    }

    // ── MultiQuery tests ─────────────────────────────────────────────────────

    #[tokio::test]
    async fn multi_query_yields_multiple_sub_queries() {
        let strategy = MultiQueryStrategy::new(MockExpansionLlm::ok(), 3);
        let expanded = strategy.expand("how does RRF work").await.unwrap();

        assert_eq!(expanded.strategy, StrategyKind::MultiQuery);
        // Original + 3 variants = 4
        assert_eq!(expanded.queries.len(), 4);
        assert_eq!(expanded.queries[0], "how does RRF work");
        assert!(expanded.queries[1].starts_with("variant_0:"));
        assert!(expanded.queries[2].starts_with("variant_1:"));
        assert!(expanded.queries[3].starts_with("variant_2:"));
    }

    #[tokio::test]
    async fn multi_query_original_always_first() {
        let strategy = MultiQueryStrategy::new(MockExpansionLlm::ok(), 2);
        let expanded = strategy.expand("embedding distance").await.unwrap();
        assert_eq!(expanded.queries[0], "embedding distance");
    }

    #[tokio::test]
    async fn multi_query_clamps_n_variants_to_max_10() {
        let strategy = MultiQueryStrategy::new(MockExpansionLlm::ok(), 99);
        assert_eq!(strategy.n_variants, 10);
    }

    #[tokio::test]
    async fn multi_query_clamps_n_variants_to_min_1() {
        let strategy = MultiQueryStrategy::new(MockExpansionLlm::ok(), 0);
        assert_eq!(strategy.n_variants, 1);
    }

    #[tokio::test]
    async fn multi_query_falls_back_to_original_on_llm_failure() {
        let strategy = MultiQueryStrategy::new(MockExpansionLlm::fail_paraphrase(), 3);
        let expanded = strategy.expand("chunking strategy").await.unwrap();
        assert_eq!(expanded.queries.len(), 1);
        assert_eq!(expanded.queries[0], "chunking strategy");
    }

    #[tokio::test]
    async fn multi_query_kind_is_multi_query() {
        let strategy = MultiQueryStrategy::new(MockExpansionLlm::ok(), 2);
        assert_eq!(strategy.kind(), StrategyKind::MultiQuery);
    }

    #[tokio::test]
    async fn multi_query_filters_is_none() {
        let strategy = MultiQueryStrategy::new(MockExpansionLlm::ok(), 2);
        let expanded = strategy.expand("foo").await.unwrap();
        assert!(expanded.filters.is_none());
    }

    // ── StepBack tests ───────────────────────────────────────────────────────

    #[tokio::test]
    async fn step_back_yields_broadened_query() {
        let strategy = StepBackStrategy::new(MockExpansionLlm::ok());
        let expanded = strategy.expand("what is RRF k constant").await.unwrap();

        assert_eq!(expanded.strategy, StrategyKind::StepBack);
        assert_eq!(expanded.queries.len(), 1);
        assert!(expanded.queries[0].contains("broader context of"));
    }

    #[tokio::test]
    async fn step_back_broadens_not_identical_to_original() {
        let strategy = StepBackStrategy::new(MockExpansionLlm::ok());
        let expanded = strategy.expand("specific niche query").await.unwrap();
        assert_ne!(expanded.queries[0], "specific niche query");
    }

    #[tokio::test]
    async fn step_back_falls_back_on_llm_failure() {
        let strategy = StepBackStrategy::new(MockExpansionLlm::fail_step_back());
        let expanded = strategy.expand("sparse retrieval").await.unwrap();
        assert_eq!(expanded.queries[0], "sparse retrieval");
    }

    #[tokio::test]
    async fn step_back_falls_back_on_empty_response() {
        let strategy = StepBackStrategy::new(MockExpansionLlm::empty_step_back());
        let expanded = strategy.expand("dense vectors").await.unwrap();
        assert_eq!(expanded.queries[0], "dense vectors");
    }

    #[tokio::test]
    async fn step_back_kind_is_step_back() {
        let strategy = StepBackStrategy::new(MockExpansionLlm::ok());
        assert_eq!(strategy.kind(), StrategyKind::StepBack);
    }

    #[tokio::test]
    async fn step_back_filters_is_none() {
        let strategy = StepBackStrategy::new(MockExpansionLlm::ok());
        let expanded = strategy.expand("test").await.unwrap();
        assert!(expanded.filters.is_none());
    }
}
