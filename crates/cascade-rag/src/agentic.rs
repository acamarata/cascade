//! Agentic retrieval loop: Self-RAG / FLARE-lite bounded retrieve→assess→reformulate.
//!
//! # Purpose
//!
//! Implements a bounded agentic retrieval loop that:
//! 1. Retrieves context using the current query via [`QueryStrategy::expand`].
//! 2. Asks an [`AdequacyAssessor`] whether the retrieved context is sufficient.
//! 3. If not, reformulates the query (via the assessor) and loops.
//! 4. Stops when context is adequate OR after `max_iterations` rounds.
//!
//! This is a FLARE-lite / Self-RAG pattern: no autoregressive generation in the
//! loop; the assessor is a lightweight inject-able oracle (real or mock).
//!
//! # Inputs
//!
//! - `question`: original user question.
//! - `strategy`: `Arc<dyn QueryStrategy>` — expands the current query.
//! - `assessor`: `Arc<dyn AdequacyAssessor>` — judges and optionally reformulates.
//! - `retrieve_fn`: `async Fn(Vec<String>) -> Vec<String>` — retrieves context
//!   for a list of expanded queries; returns text snippets.
//! - `config`: [`AgenticConfig`] — iteration bound and convergence controls.
//!
//! # Outputs
//!
//! [`AgenticResult`]: final retrieved context, number of iterations used, and
//! whether convergence was reached before the bound.
//!
//! # Constraints
//!
//! - Hard stop at `max_iterations`; never infinite-loops.
//! - All LLM calls are injected; no network calls in this module.
//! - `retrieve_fn` is sync-over-async for testability (accepts `Vec<String>`,
//!   returns `Vec<String>`).
//!
//! # Ticket
//!
//! rag-09
//!
//! SPORT: MASTER-CRATES.md → cascade-rag::agentic

use std::sync::Arc;

use async_trait::async_trait;
use tracing::{debug, instrument, warn};

use cascade_types::{error::Result, query_strategy::QueryStrategy};

// ── AdequacyAssessor ──────────────────────────────────────────────────────────

/// Judges whether retrieved context is sufficient to answer a question, and
/// optionally reformulates the query if not.
///
/// # Object safety
///
/// Object-safe; use `Arc<dyn AdequacyAssessor>`.
#[async_trait]
pub trait AdequacyAssessor: Send + Sync {
    /// Returns `true` if `context` is sufficient to answer `question`.
    ///
    /// A trivial implementation returns `true` unconditionally; a real
    /// implementation might call a lightweight classifier or LLM.
    async fn is_adequate(&self, question: &str, context: &[String]) -> bool;

    /// Reformulate `query` given that `context` was insufficient.
    ///
    /// Returns a new query string.  On failure, returns `query` unchanged so
    /// the loop still makes progress (hits the bound gracefully).
    async fn reformulate(&self, question: &str, query: &str, context: &[String]) -> String;
}

// ── AgenticConfig ─────────────────────────────────────────────────────────────

/// Configuration for the agentic retrieval loop.
#[derive(Debug, Clone)]
pub struct AgenticConfig {
    /// Hard upper bound on retrieve→assess→reformulate iterations.
    ///
    /// After `max_iterations` rounds the loop always stops, returning whatever
    /// context was last retrieved.  Default: 3.
    pub max_iterations: usize,
}

impl Default for AgenticConfig {
    fn default() -> Self {
        Self { max_iterations: 3 }
    }
}

// ── AgenticResult ─────────────────────────────────────────────────────────────

/// Output of [`agentic_retrieve`].
#[derive(Debug, Clone)]
pub struct AgenticResult {
    /// Final retrieved context snippets (from the last iteration).
    pub context: Vec<String>,
    /// Number of retrieve→assess iterations performed (1 ≤ n ≤ max_iterations).
    pub iterations: usize,
    /// `true` if the assessor declared context adequate before the bound.
    pub converged: bool,
    /// The final query used for the last retrieval pass.
    pub final_query: String,
}

// ── Main loop ─────────────────────────────────────────────────────────────────

/// Bounded agentic retrieval loop.
///
/// # Algorithm
///
/// ```text
/// query = question
/// for i in 1..=max_iterations:
///     expanded = strategy.expand(query)
///     context  = retrieve_fn(expanded.queries)
///     if assessor.is_adequate(question, context):
///         return AgenticResult { converged: true, iterations: i, … }
///     if i < max_iterations:
///         query = assessor.reformulate(question, query, context)
/// return AgenticResult { converged: false, iterations: max_iterations, … }
/// ```
///
/// # Inputs
///
/// - `question`: original user question (never changes across iterations).
/// - `strategy`: query expansion strategy.
/// - `assessor`: adequacy oracle.
/// - `retrieve_fn`: closure that takes expanded query strings and returns
///   retrieved text snippets.
/// - `config`: loop bounds.
///
/// # Outputs
///
/// [`AgenticResult`] — always `Ok`; never returns `Err` (errors in expansion
/// or retrieval degrade to empty context, loop continues).
pub async fn agentic_retrieve<F, Fut>(
    question: &str,
    strategy: Arc<dyn QueryStrategy>,
    assessor: Arc<dyn AdequacyAssessor>,
    retrieve_fn: F,
    config: &AgenticConfig,
) -> Result<AgenticResult>
where
    F: Fn(Vec<String>) -> Fut + Send + Sync,
    Fut: std::future::Future<Output = Vec<String>> + Send,
{
    let max = config.max_iterations.max(1);
    let mut query = question.to_string();
    let mut context: Vec<String> = Vec::new();

    for i in 1..=max {
        debug!(iteration = i, query = %query, "agentic_retrieve: expanding query");

        // 1. Expand the current query.
        let expanded = match strategy.expand(&query).await {
            Ok(e) => e,
            Err(err) => {
                warn!(error = %err, "agentic_retrieve: expand failed; using raw query");
                // Synthesize a pass-through result.
                cascade_types::query_strategy::ExpandedQuery {
                    queries: vec![query.clone()],
                    filters: None,
                    strategy: strategy.kind(),
                }
            }
        };

        // 2. Retrieve context.
        context = retrieve_fn(expanded.queries).await;
        debug!(iteration = i, snippets = context.len(), "agentic_retrieve: context retrieved");

        // 3. Assess adequacy.
        if assessor.is_adequate(question, &context).await {
            debug!(iteration = i, "agentic_retrieve: context adequate; converged");
            return Ok(AgenticResult {
                context,
                iterations: i,
                converged: true,
                final_query: query,
            });
        }

        // 4. Reformulate for the next round (skip on last iteration).
        if i < max {
            let new_query = assessor.reformulate(question, &query, &context).await;
            debug!(
                iteration = i,
                old_query = %query,
                new_query = %new_query,
                "agentic_retrieve: reformulated query"
            );
            query = new_query;
        }
    }

    debug!(max_iterations = max, "agentic_retrieve: bound reached without convergence");
    Ok(AgenticResult {
        context,
        iterations: max,
        converged: false,
        final_query: query,
    })
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::query_strategy::{ExpandedQuery, StrategyKind};
    use std::sync::atomic::{AtomicUsize, Ordering};

    // ── Mock QueryStrategy ────────────────────────────────────────────────────

    /// Pass-through strategy; records call count.
    struct CountingStrategy(Arc<AtomicUsize>);

    #[async_trait]
    impl QueryStrategy for CountingStrategy {
        fn kind(&self) -> StrategyKind {
            StrategyKind::PureVector
        }
        async fn expand(&self, query: &str) -> cascade_types::error::Result<ExpandedQuery> {
            self.0.fetch_add(1, Ordering::SeqCst);
            Ok(ExpandedQuery {
                queries: vec![query.to_owned()],
                filters: None,
                strategy: StrategyKind::PureVector,
            })
        }
    }

    // ── Mock AdequacyAssessor ─────────────────────────────────────────────────

    /// Returns adequate after `adequate_after` calls; reformulates by appending " [retry N]".
    struct ThresholdAssessor {
        adequate_after: usize,
        call_count: Arc<AtomicUsize>,
    }

    impl ThresholdAssessor {
        fn new(adequate_after: usize) -> Arc<Self> {
            Arc::new(Self {
                adequate_after,
                call_count: Arc::new(AtomicUsize::new(0)),
            })
        }
    }

    #[async_trait]
    impl AdequacyAssessor for ThresholdAssessor {
        async fn is_adequate(&self, _question: &str, _context: &[String]) -> bool {
            let n = self.call_count.fetch_add(1, Ordering::SeqCst) + 1;
            n >= self.adequate_after
        }
        async fn reformulate(&self, _question: &str, query: &str, _context: &[String]) -> String {
            let n = self.call_count.load(Ordering::SeqCst);
            format!("{query} [retry {n}]")
        }
    }

    /// Always-adequate assessor.
    struct AlwaysAdequate;
    #[async_trait]
    impl AdequacyAssessor for AlwaysAdequate {
        async fn is_adequate(&self, _: &str, _: &[String]) -> bool {
            true
        }
        async fn reformulate(&self, _: &str, q: &str, _: &[String]) -> String {
            q.to_owned()
        }
    }

    /// Never-adequate assessor (forces loop to reach bound).
    struct NeverAdequate;
    #[async_trait]
    impl AdequacyAssessor for NeverAdequate {
        async fn is_adequate(&self, _: &str, _: &[String]) -> bool {
            false
        }
        async fn reformulate(&self, _: &str, q: &str, _: &[String]) -> String {
            format!("{q}+")
        }
    }

    // ── Simple retrieve_fn ────────────────────────────────────────────────────

    /// Returns one snippet per query, prefixed with "doc:".
    async fn mock_retrieve(queries: Vec<String>) -> Vec<String> {
        queries.into_iter().map(|q| format!("doc:{q}")).collect()
    }

    // ── Tests ─────────────────────────────────────────────────────────────────

    #[tokio::test]
    async fn converges_on_first_iteration_when_always_adequate() {
        let strategy = Arc::new(CountingStrategy(Arc::new(AtomicUsize::new(0))));
        let assessor: Arc<dyn AdequacyAssessor> = Arc::new(AlwaysAdequate);
        let config = AgenticConfig { max_iterations: 5 };

        let result = agentic_retrieve(
            "explain RRF",
            strategy,
            assessor,
            mock_retrieve,
            &config,
        )
        .await
        .unwrap();

        assert!(result.converged);
        assert_eq!(result.iterations, 1);
        assert_eq!(result.context.len(), 1);
        assert!(result.context[0].starts_with("doc:"));
    }

    #[tokio::test]
    async fn stops_at_max_iterations_bound() {
        let strategy = Arc::new(CountingStrategy(Arc::new(AtomicUsize::new(0))));
        let assessor: Arc<dyn AdequacyAssessor> = Arc::new(NeverAdequate);
        let config = AgenticConfig { max_iterations: 3 };

        let result = agentic_retrieve(
            "what is chunking",
            strategy,
            assessor,
            mock_retrieve,
            &config,
        )
        .await
        .unwrap();

        assert!(!result.converged);
        assert_eq!(result.iterations, 3);
    }

    #[tokio::test]
    async fn converges_after_n_iterations() {
        let strategy = Arc::new(CountingStrategy(Arc::new(AtomicUsize::new(0))));
        let assessor = ThresholdAssessor::new(2); // adequate on 2nd call
        let config = AgenticConfig { max_iterations: 5 };

        let result = agentic_retrieve(
            "how does HyDE work",
            strategy,
            Arc::clone(&assessor) as Arc<dyn AdequacyAssessor>,
            mock_retrieve,
            &config,
        )
        .await
        .unwrap();

        assert!(result.converged);
        assert_eq!(result.iterations, 2);
    }

    #[tokio::test]
    async fn query_is_reformulated_between_iterations() {
        let strategy = Arc::new(CountingStrategy(Arc::new(AtomicUsize::new(0))));
        // Never converges so we can check final_query
        let assessor: Arc<dyn AdequacyAssessor> = Arc::new(NeverAdequate);
        let config = AgenticConfig { max_iterations: 3 };

        let result = agentic_retrieve(
            "sparse vectors",
            strategy,
            assessor,
            mock_retrieve,
            &config,
        )
        .await
        .unwrap();

        // NeverAdequate appends "+" each reformulation
        assert!(result.final_query.contains("sparse vectors+"));
    }

    #[tokio::test]
    async fn max_iterations_zero_is_clamped_to_one() {
        let strategy = Arc::new(CountingStrategy(Arc::new(AtomicUsize::new(0))));
        let assessor: Arc<dyn AdequacyAssessor> = Arc::new(NeverAdequate);
        let config = AgenticConfig { max_iterations: 0 };

        let result = agentic_retrieve(
            "test",
            strategy,
            assessor,
            mock_retrieve,
            &config,
        )
        .await
        .unwrap();

        // clamped to 1
        assert_eq!(result.iterations, 1);
        assert!(!result.converged);
    }

    #[tokio::test]
    async fn context_from_last_iteration_is_returned_on_non_convergence() {
        let strategy = Arc::new(CountingStrategy(Arc::new(AtomicUsize::new(0))));
        let assessor: Arc<dyn AdequacyAssessor> = Arc::new(NeverAdequate);
        let config = AgenticConfig { max_iterations: 2 };

        let result = agentic_retrieve(
            "final context test",
            strategy,
            assessor,
            |queries| async move {
                // Return different context per round based on query
                queries.into_iter().map(|q| format!("result:{q}")).collect()
            },
            &config,
        )
        .await
        .unwrap();

        assert!(!result.converged);
        assert!(!result.context.is_empty());
        // Context comes from the last retrieval
        assert!(result.context[0].starts_with("result:"));
    }
}
