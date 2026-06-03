//! RAG evaluation framework: MRR, NDCG, Recall@K, Precision@K.
//!
//! The evaluation framework measures retrieval quality against a ground-truth
//! dataset of (query, relevant_chunk_ids) pairs.
//!
//! ## Metrics
//!
//! | Metric | Description |
//! |--------|-------------|
//! | MRR@K  | Mean Reciprocal Rank — average of 1/(rank of first relevant hit) |
//! | NDCG@K | Normalised Discounted Cumulative Gain |
//! | Recall@K | Fraction of relevant chunks appearing in top-K |
//! | Precision@K | Fraction of top-K that are relevant |
//!
//! ## Ground-truth format (S20-01)
//!
//! JSONL file: one JSON object per line:
//! ```json
//! {"query": "solar declination formula", "relevant_chunk_ids": ["src/spa.rs-120", "docs/algo.md-44"]}
//! ```
//!
//! ## Acceptance criteria (S20)
//!
//! - MRR@10 ≥ 0.65 on the built-in synthetic test set for hybrid RRF on a
//!   markdown corpus.
//! - Regression detection fires on a 10% simulated degradation.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::eval

use serde::{Deserialize, Serialize};
use std::collections::HashSet;

// ── Ground truth ──────────────────────────────────────────────────────────────

/// A single annotated query-answer pair for evaluation.
///
/// SPORT: MASTER-LIBS.md → cascade-rag::eval::EvalQuery
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct EvalQuery {
    /// The user query string.
    pub query: String,
    /// Chunk IDs that are considered relevant for this query.
    pub relevant_chunk_ids: Vec<String>,
    /// Optional human notes about why these chunks are relevant.
    pub notes: Option<String>,
}

/// A versioned ground-truth dataset.
///
/// SPORT: MASTER-LIBS.md → cascade-rag::eval::GroundTruth
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct GroundTruth {
    /// Dataset name (used in reports).
    pub name: String,
    /// Semantic version of the dataset.
    pub version: String,
    /// Annotated queries.
    pub queries: Vec<EvalQuery>,
}

impl GroundTruth {
    /// Load a ground-truth dataset from a JSONL file.
    ///
    /// Each line is one [`EvalQuery`].  Empty lines are skipped.
    pub fn load_jsonl(path: &std::path::Path) -> cascade_types::error::Result<Self> {
        use cascade_types::error::CascadeError;
        let content =
            std::fs::read_to_string(path).map_err(|e| CascadeError::io(path, "read", e))?;
        let queries: Vec<EvalQuery> = content
            .lines()
            .filter(|l| !l.trim().is_empty())
            .map(|l| {
                serde_json::from_str(l).map_err(|e| CascadeError::ParseFailed {
                    path: path.to_path_buf(),
                    detail: format!("ground-truth JSONL: {e}"),
                })
            })
            .collect::<cascade_types::error::Result<Vec<_>>>()?;
        Ok(Self {
            name: path
                .file_stem()
                .and_then(|s| s.to_str())
                .unwrap_or("dataset")
                .to_string(),
            version: "1.0.0".into(),
            queries,
        })
    }
}

// ── Metrics ───────────────────────────────────────────────────────────────────

/// Aggregate evaluation metrics for a retrieval strategy.
///
/// SPORT: MASTER-LIBS.md → cascade-rag::eval::EvalMetrics
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct EvalMetrics {
    /// Name of the retrieval strategy evaluated.
    pub strategy: String,
    /// Number of queries evaluated.
    pub query_count: usize,
    /// The K value used for all @K metrics.
    pub k: usize,
    /// Mean Reciprocal Rank at K.
    pub mrr_at_k: f32,
    /// Mean Normalised Discounted Cumulative Gain at K.
    pub ndcg_at_k: f32,
    /// Mean Recall at K.
    pub recall_at_k: f32,
    /// Mean Precision at K.
    pub precision_at_k: f32,
}

impl EvalMetrics {
    /// Compute metrics from a list of (query, ranked_chunk_ids) pairs.
    ///
    /// `results[i]` is the ordered list of chunk IDs returned for
    /// `ground_truth.queries[i]`.  Lengths must match.
    pub fn compute(
        strategy: impl Into<String>,
        ground_truth: &GroundTruth,
        results: &[Vec<String>],
        k: usize,
    ) -> Self {
        assert_eq!(
            ground_truth.queries.len(),
            results.len(),
            "query and result counts must match"
        );
        let n = ground_truth.queries.len();
        if n == 0 {
            return Self {
                strategy: strategy.into(),
                query_count: 0,
                k,
                mrr_at_k: 0.0,
                ndcg_at_k: 0.0,
                recall_at_k: 0.0,
                precision_at_k: 0.0,
            };
        }

        let mut mrr_sum = 0.0f32;
        let mut ndcg_sum = 0.0f32;
        let mut recall_sum = 0.0f32;
        let mut prec_sum = 0.0f32;

        for (eq, ranked) in ground_truth.queries.iter().zip(results.iter()) {
            let relevant: HashSet<&str> =
                eq.relevant_chunk_ids.iter().map(|s| s.as_str()).collect();
            let top_k: Vec<&str> = ranked.iter().take(k).map(|s| s.as_str()).collect();

            mrr_sum += mrr_at_k(&top_k, &relevant, k);
            ndcg_sum += ndcg_at_k(&top_k, &relevant, k);
            recall_sum += recall_at_k(&top_k, &relevant);
            prec_sum += precision_at_k(&top_k, &relevant);
        }

        Self {
            strategy: strategy.into(),
            query_count: n,
            k,
            mrr_at_k: mrr_sum / n as f32,
            ndcg_at_k: ndcg_sum / n as f32,
            recall_at_k: recall_sum / n as f32,
            precision_at_k: prec_sum / n as f32,
        }
    }

    /// Returns `true` if any metric has degraded by more than `threshold_pct`
    /// compared to `baseline`.
    ///
    /// Used for regression detection (S20-08).
    pub fn has_regression(&self, baseline: &EvalMetrics, threshold_pct: f32) -> bool {
        let factor = 1.0 - threshold_pct / 100.0;
        self.mrr_at_k < baseline.mrr_at_k * factor
            || self.ndcg_at_k < baseline.ndcg_at_k * factor
            || self.recall_at_k < baseline.recall_at_k * factor
    }
}

// ── Per-query metric functions ────────────────────────────────────────────────

/// Reciprocal Rank: 1 / (rank of first relevant hit), or 0 if none in top-K.
fn mrr_at_k(ranked: &[&str], relevant: &HashSet<&str>, _k: usize) -> f32 {
    for (i, id) in ranked.iter().enumerate() {
        if relevant.contains(*id) {
            return 1.0 / (i + 1) as f32;
        }
    }
    0.0
}

/// Normalised DCG at K.
///
/// DCG = Σ relevance_i / log2(i + 2) for i in 0..K
/// IDCG = DCG of the ideal (all relevant first) ranking
fn ndcg_at_k(ranked: &[&str], relevant: &HashSet<&str>, k: usize) -> f32 {
    let dcg: f32 = ranked
        .iter()
        .take(k)
        .enumerate()
        .map(|(i, id)| {
            let rel = if relevant.contains(*id) { 1.0f32 } else { 0.0 };
            rel / (i as f32 + 2.0).log2()
        })
        .sum();

    // Ideal DCG: first `min(|relevant|, k)` positions are all relevant.
    let n_relevant = relevant.len().min(k);
    let idcg: f32 = (0..n_relevant).map(|i| 1.0 / (i as f32 + 2.0).log2()).sum();

    if idcg == 0.0 {
        0.0
    } else {
        dcg / idcg
    }
}

/// Recall at K: fraction of relevant chunks appearing in top-K.
fn recall_at_k(ranked: &[&str], relevant: &HashSet<&str>) -> f32 {
    if relevant.is_empty() {
        return 1.0;
    }
    let hits = ranked.iter().filter(|id| relevant.contains(**id)).count();
    hits as f32 / relevant.len() as f32
}

/// Precision at K: fraction of top-K that are relevant.
fn precision_at_k(ranked: &[&str], relevant: &HashSet<&str>) -> f32 {
    if ranked.is_empty() {
        return 0.0;
    }
    let hits = ranked.iter().filter(|id| relevant.contains(**id)).count();
    hits as f32 / ranked.len() as f32
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashSet;

    #[test]
    fn mrr_perfect() {
        let relevant: HashSet<&str> = ["a"].iter().cloned().collect();
        assert!((mrr_at_k(&["a", "b", "c"], &relevant, 10) - 1.0).abs() < 1e-6);
    }

    #[test]
    fn mrr_second_position() {
        let relevant: HashSet<&str> = ["b"].iter().cloned().collect();
        assert!((mrr_at_k(&["a", "b", "c"], &relevant, 10) - 0.5).abs() < 1e-6);
    }

    #[test]
    fn ndcg_perfect() {
        let relevant: HashSet<&str> = ["a", "b"].iter().cloned().collect();
        // Perfect ranking: both relevant items at top.
        let score = ndcg_at_k(&["a", "b", "c"], &relevant, 3);
        assert!(
            (score - 1.0).abs() < 1e-6,
            "NDCG should be 1.0 for perfect ranking, got {score}"
        );
    }

    #[test]
    fn recall_all_in_top_k() {
        let relevant: HashSet<&str> = ["a", "b"].iter().cloned().collect();
        let score = recall_at_k(&["a", "b", "c"], &relevant);
        assert!((score - 1.0).abs() < 1e-6);
    }

    #[test]
    fn regression_detection_fires_at_10pct() {
        let baseline = EvalMetrics {
            strategy: "a".into(),
            query_count: 10,
            k: 10,
            mrr_at_k: 0.7,
            ndcg_at_k: 0.65,
            recall_at_k: 0.8,
            precision_at_k: 0.5,
        };
        let degraded = EvalMetrics {
            mrr_at_k: 0.63, // < 0.7 * 0.9 = 0.63 — exactly on boundary
            ..baseline.clone()
        };
        assert!(
            degraded.has_regression(&baseline, 10.0),
            "should detect ≥10% MRR degradation"
        );
    }
}
