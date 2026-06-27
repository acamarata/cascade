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
use std::path::PathBuf;

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
    /// Mean Average Precision at K.
    ///
    /// MAP@K = mean over queries of AP@K, where
    /// AP@K = (1/|R|) * Σ_{i=1}^{K} P@i * rel_i
    pub map_at_k: f32,
    /// Mean Recall at K.
    pub recall_at_k: f32,
    /// Mean Precision at K.
    pub precision_at_k: f32,
    /// Mean query latency in milliseconds, if measured.
    ///
    /// `None` when the eval harness does not instrument latency.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub latency_ms: Option<f32>,
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
                map_at_k: 0.0,
                recall_at_k: 0.0,
                precision_at_k: 0.0,
                latency_ms: None,
            };
        }

        let mut mrr_sum = 0.0f32;
        let mut ndcg_sum = 0.0f32;
        let mut map_sum = 0.0f32;
        let mut recall_sum = 0.0f32;
        let mut prec_sum = 0.0f32;

        for (eq, ranked) in ground_truth.queries.iter().zip(results.iter()) {
            let relevant: HashSet<&str> =
                eq.relevant_chunk_ids.iter().map(|s| s.as_str()).collect();
            let top_k: Vec<&str> = ranked.iter().take(k).map(|s| s.as_str()).collect();

            mrr_sum += mrr_at_k(&top_k, &relevant, k);
            ndcg_sum += ndcg_at_k(&top_k, &relevant, k);
            map_sum += map_at_k(&top_k, &relevant, k);
            recall_sum += recall_at_k(&top_k, &relevant);
            prec_sum += precision_at_k(&top_k, &relevant);
        }

        Self {
            strategy: strategy.into(),
            query_count: n,
            k,
            mrr_at_k: mrr_sum / n as f32,
            ndcg_at_k: ndcg_sum / n as f32,
            map_at_k: map_sum / n as f32,
            recall_at_k: recall_sum / n as f32,
            precision_at_k: prec_sum / n as f32,
            latency_ms: None,
        }
    }

    /// Returns `true` if any metric has degraded by at least `threshold_pct`
    /// relative to `baseline` (boundary-inclusive: an exact `threshold_pct` drop
    /// IS a regression).
    ///
    /// Used for regression detection (S20-08). Regression detection is
    /// deliberately conservative — catching a boundary drop is safer than
    /// missing it. A small epsilon is added to the comparison so that an exact
    /// threshold drop fires deterministically despite f32 rounding of
    /// `baseline * factor` (e.g. `0.7 * 0.9` is not exactly `0.63` in f32).
    pub fn has_regression(&self, baseline: &EvalMetrics, threshold_pct: f32) -> bool {
        // Relative tolerance for boundary stability under f32 rounding.
        const EPS: f32 = 1e-5;
        let factor = 1.0 - threshold_pct / 100.0;
        self.mrr_at_k <= baseline.mrr_at_k * factor + EPS
            || self.ndcg_at_k <= baseline.ndcg_at_k * factor + EPS
            || self.map_at_k <= baseline.map_at_k * factor + EPS
            || self.recall_at_k <= baseline.recall_at_k * factor + EPS
    }
}

// ── Per-query metric functions ────────────────────────────────────────────────

/// Average Precision at K.
///
/// AP@K = (1/|R|) * Σ_{i=1}^{K} P@i * rel_i
///
/// where `|R|` is the number of relevant documents (clamped to 1 when empty to
/// avoid division by zero), `P@i` is precision at cut-off `i`, and `rel_i` is
/// 1 if the i-th result is relevant, else 0.
///
/// # Inputs
/// - `ranked`: slice of chunk IDs in ranked order (already trimmed to top-K).
/// - `relevant`: set of relevant chunk IDs.
/// - `_k`: unused (ranked is already clipped to K by callers).
///
/// # Outputs
/// AP@K in [0.0, 1.0].
fn map_at_k(ranked: &[&str], relevant: &HashSet<&str>, _k: usize) -> f32 {
    if relevant.is_empty() {
        // Vacuously perfect — no relevant docs means any ranking is correct.
        return 1.0;
    }
    let mut hits = 0u32;
    let mut ap_sum = 0.0f32;
    for (i, id) in ranked.iter().enumerate() {
        if relevant.contains(*id) {
            hits += 1;
            let precision_at_i = hits as f32 / (i + 1) as f32;
            ap_sum += precision_at_i;
        }
    }
    ap_sum / relevant.len() as f32
}

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

// ── EvalConfig / EvalReport / EvalHarness ────────────────────────────────────

/// Configuration for a single eval run.
///
/// SPORT: MASTER-LIBS.md → cascade-rag::eval::EvalConfig
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct EvalConfig {
    /// Top-K cutoff for all @K metrics.
    pub k: usize,
    /// Name tag for the strategy under test (e.g. "fts5", "rrf", "semantic").
    pub strategy: String,
}

impl Default for EvalConfig {
    fn default() -> Self {
        Self {
            k: 10,
            strategy: "default".into(),
        }
    }
}

/// Per-query result row inside an [`EvalReport`].
///
/// SPORT: MASTER-LIBS.md → cascade-rag::eval::QueryResult
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct QueryResult {
    /// The query string.
    pub query: String,
    /// Reciprocal Rank for this query (0.0 if no relevant hit in top-K).
    pub mrr: f32,
    /// NDCG@K for this query.
    pub ndcg: f32,
    /// Precision@K for this query.
    pub p_at_k: f32,
    /// Recall@K for this query.
    pub r_at_k: f32,
    /// Up to 5 of the returned chunk IDs (for debugging).
    pub top_5_ids: Vec<String>,
}

/// Full evaluation report produced by [`EvalHarness::run_eval`].
///
/// Serialises to a JSON report matching the spec:
/// ```json
/// { "query_count": 10, "mrr": 0.8, "ndcg_at_10": 0.75,
///   "precision_at_10": 0.6, "recall_at_10": 0.9,
///   "per_query": [...] }
/// ```
///
/// SPORT: MASTER-LIBS.md → cascade-rag::eval::EvalReport
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct EvalReport {
    /// Strategy name as supplied in [`EvalConfig`].
    pub strategy: String,
    /// Number of queries evaluated.
    pub query_count: usize,
    /// The K cutoff used for all @K metrics.
    pub k: usize,
    /// Aggregate mean MRR.
    pub mrr: f32,
    /// Aggregate mean NDCG@K.
    pub ndcg_at_10: f32,
    /// Aggregate mean Precision@K.
    pub precision_at_10: f32,
    /// Aggregate mean Recall@K.
    pub recall_at_10: f32,
    /// Per-query breakdown.
    pub per_query: Vec<QueryResult>,
}

impl EvalReport {
    /// Serialise the report to a JSON string.
    pub fn to_json(&self) -> cascade_types::error::Result<String> {
        serde_json::to_string_pretty(self).map_err(|e| {
            cascade_types::error::CascadeError::ParseFailed {
                path: PathBuf::from("eval_report.json"),
                detail: format!("serialise EvalReport: {e}"),
            }
        })
    }
}

/// Type alias for the injectable search function passed to [`EvalHarness`].
///
/// Takes a query string and returns a ranked list of chunk IDs (most relevant
/// first). This mirrors the production `search()` API so the same config
/// used in production is testable via eval.
pub type SearchFn = Box<dyn Fn(&str, usize) -> Vec<String> + Send + Sync>;

/// Runs a retrieval strategy over a [`GroundTruth`] dataset and produces an
/// [`EvalReport`].
///
/// The harness is pure: it has no database dependency. The caller injects a
/// `search_fn` closure that wraps whatever retrieval backend is under test.
/// This makes the harness usable in unit tests with a synthetic closure and in
/// integration tests with a live [`RagIndex`].
///
/// # Example
///
/// ```ignore
/// let harness = EvalHarness::new(
///     ground_truth,
///     EvalConfig { k: 10, strategy: "fts5".into() },
///     Box::new(|query, k| index.search(query, k)),
/// );
/// let report = harness.run_eval();
/// ```
///
/// SPORT: MASTER-LIBS.md → cascade-rag::eval::EvalHarness
pub struct EvalHarness {
    ground_truth: GroundTruth,
    config: EvalConfig,
    search_fn: SearchFn,
}

impl EvalHarness {
    /// Create a new harness.
    ///
    /// # Parameters
    /// - `ground_truth`: dataset of (query, relevant_chunk_ids) pairs.
    /// - `config`: K cutoff and strategy name.
    /// - `search_fn`: closure `(query: &str, k: usize) -> Vec<String>` that
    ///   returns chunk IDs ranked by relevance.
    pub fn new(ground_truth: GroundTruth, config: EvalConfig, search_fn: SearchFn) -> Self {
        Self {
            ground_truth,
            config,
            search_fn,
        }
    }

    /// Run the evaluation and return a full [`EvalReport`].
    ///
    /// Iterates over every query in the ground-truth dataset, calls
    /// `search_fn`, computes per-query metrics, and aggregates.
    pub fn run_eval(&self) -> EvalReport {
        let k = self.config.k;
        let queries = &self.ground_truth.queries;

        if queries.is_empty() {
            return EvalReport {
                strategy: self.config.strategy.clone(),
                query_count: 0,
                k,
                mrr: 0.0,
                ndcg_at_10: 0.0,
                precision_at_10: 0.0,
                recall_at_10: 0.0,
                per_query: vec![],
            };
        }

        let mut per_query = Vec::with_capacity(queries.len());
        let mut mrr_sum = 0.0f32;
        let mut ndcg_sum = 0.0f32;
        let mut prec_sum = 0.0f32;
        let mut rec_sum = 0.0f32;

        for eq in queries {
            let ranked = (self.search_fn)(&eq.query, k);
            let relevant: HashSet<&str> =
                eq.relevant_chunk_ids.iter().map(|s| s.as_str()).collect();
            let top_k: Vec<&str> = ranked.iter().take(k).map(|s| s.as_str()).collect();

            let mrr = mrr_at_k(&top_k, &relevant, k);
            let ndcg = ndcg_at_k(&top_k, &relevant, k);
            let p = precision_at_k(&top_k, &relevant);
            let r = recall_at_k(&top_k, &relevant);

            mrr_sum += mrr;
            ndcg_sum += ndcg;
            prec_sum += p;
            rec_sum += r;

            per_query.push(QueryResult {
                query: eq.query.clone(),
                mrr,
                ndcg,
                p_at_k: p,
                r_at_k: r,
                top_5_ids: ranked.into_iter().take(5).collect(),
            });
        }

        let n = queries.len() as f32;
        EvalReport {
            strategy: self.config.strategy.clone(),
            query_count: queries.len(),
            k,
            mrr: mrr_sum / n,
            ndcg_at_10: ndcg_sum / n,
            precision_at_10: prec_sum / n,
            recall_at_10: rec_sum / n,
            per_query,
        }
    }
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
            map_at_k: 0.6,
            recall_at_k: 0.8,
            precision_at_k: 0.5,
            latency_ms: None,
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

    // ── EvalHarness / EvalReport tests ───────────────────────────────────────

    /// MRR = 1.0 when the first result is always relevant (acceptance criterion).
    #[test]
    fn eval_harness_mrr_perfect_first_hit() {
        let gt = GroundTruth {
            name: "test".into(),
            version: "1.0.0".into(),
            queries: vec![
                EvalQuery {
                    query: "q1".into(),
                    relevant_chunk_ids: vec!["a".into()],
                    notes: None,
                },
                EvalQuery {
                    query: "q2".into(),
                    relevant_chunk_ids: vec!["x".into()],
                    notes: None,
                },
            ],
        };
        // Search fn always returns the relevant id at position 0.
        let harness = EvalHarness::new(
            gt,
            EvalConfig {
                k: 10,
                strategy: "mock".into(),
            },
            Box::new(|query, _k| {
                if query == "q1" {
                    vec!["a".into(), "b".into()]
                } else {
                    vec!["x".into(), "y".into()]
                }
            }),
        );
        let report = harness.run_eval();
        assert_eq!(report.query_count, 2);
        assert!(
            (report.mrr - 1.0).abs() < 1e-5,
            "MRR should be 1.0, got {}",
            report.mrr
        );
        assert!(
            (report.ndcg_at_10 - 1.0).abs() < 1e-5,
            "NDCG should be 1.0, got {}",
            report.ndcg_at_10
        );
    }

    /// NDCG@K manually verified for a 3-query synthetic case.
    ///
    /// Query has 2 relevant docs. Results: [relevant, irrelevant, relevant].
    /// DCG  = 1/log2(2) + 0 + 1/log2(4) = 1.0 + 0.5 = 1.5
    /// IDCG = 1/log2(2) + 1/log2(3) = 1.0 + 0.6309… = 1.6309…
    /// NDCG = 1.5 / 1.6309… ≈ 0.9197
    #[test]
    fn eval_ndcg_manual_calculation() {
        // ranked[0]=r1 (relevant), [1]=ir (not), [2]=r2 (relevant), k=3
        // Relevant: {r1, r2}
        // DCG  = 1/log2(2) + 0/log2(3) + 1/log2(4) = 1.0 + 0.0 + 0.5 = 1.5
        // IDCG = 1/log2(2) + 1/log2(3) = 1.0 + 0.6309... = 1.6309...
        // NDCG = 1.5 / 1.6309... ≈ 0.9197
        let relevant: HashSet<&str> = ["r1", "r2"].iter().cloned().collect();
        let score = ndcg_at_k(&["r1", "ir", "r2"], &relevant, 3);
        let idcg = 1.0_f32 / (2.0_f32.log2()) + 1.0_f32 / (3.0_f32.log2());
        let dcg = 1.0_f32 / (2.0_f32.log2()) + 1.0_f32 / (4.0_f32.log2());
        let expected = dcg / idcg;
        assert!(
            (score - expected).abs() < 1e-5,
            "NDCG manual: expected {expected}, got {score}"
        );
    }

    /// Aggregate math: 3 queries with known per-query MRR → mean is correct.
    #[test]
    fn eval_aggregate_mean_mrr() {
        // q1 → hit at pos 0 → MRR 1.0
        // q2 → hit at pos 1 → MRR 0.5
        // q3 → no hit       → MRR 0.0
        let gt = GroundTruth {
            name: "agg".into(),
            version: "1.0.0".into(),
            queries: vec![
                EvalQuery {
                    query: "q1".into(),
                    relevant_chunk_ids: vec!["a".into()],
                    notes: None,
                },
                EvalQuery {
                    query: "q2".into(),
                    relevant_chunk_ids: vec!["b".into()],
                    notes: None,
                },
                EvalQuery {
                    query: "q3".into(),
                    relevant_chunk_ids: vec!["c".into()],
                    notes: None,
                },
            ],
        };
        let harness = EvalHarness::new(
            gt,
            EvalConfig {
                k: 10,
                strategy: "agg-test".into(),
            },
            Box::new(|query: &str, _k| {
                if query == "q1" {
                    vec!["a".into(), "x".into()]
                } else if query == "q2" {
                    vec!["x".into(), "b".into()]
                } else {
                    vec!["x".into(), "y".into()]
                }
            }),
        );
        let report = harness.run_eval();
        let expected_mrr = (1.0_f32 + 0.5 + 0.0) / 3.0;
        assert!(
            (report.mrr - expected_mrr).abs() < 1e-5,
            "aggregate MRR: expected {expected_mrr}, got {}",
            report.mrr
        );
        assert_eq!(report.per_query.len(), 3);
        assert_eq!(report.query_count, 3);
    }

    /// Empty ground truth produces zeroed report without panic.
    #[test]
    fn eval_empty_ground_truth() {
        let gt = GroundTruth {
            name: "empty".into(),
            version: "1.0.0".into(),
            queries: vec![],
        };
        let harness = EvalHarness::new(
            gt,
            EvalConfig::default(),
            Box::new(|_: &str, _: usize| vec![]),
        );
        let report = harness.run_eval();
        assert_eq!(report.query_count, 0);
        assert_eq!(report.mrr, 0.0);
        assert_eq!(report.per_query.len(), 0);
    }

    /// MRR = 0.0 when no relevant result appears in top-K (no NaN).
    #[test]
    fn eval_mrr_no_relevant_hit_is_zero_not_nan() {
        let relevant: HashSet<&str> = ["missing"].iter().cloned().collect();
        let score = mrr_at_k(&["a", "b", "c"], &relevant, 10);
        assert_eq!(score, 0.0);
        assert!(!score.is_nan());
    }

    /// EvalReport serialises cleanly via serde_json (acceptance criterion).
    #[test]
    fn eval_report_json_serialises() {
        let report = EvalReport {
            strategy: "fts5".into(),
            query_count: 2,
            k: 10,
            mrr: 0.75,
            ndcg_at_10: 0.68,
            precision_at_10: 0.5,
            recall_at_10: 0.9,
            per_query: vec![QueryResult {
                query: "test query".into(),
                mrr: 1.0,
                ndcg: 1.0,
                p_at_k: 1.0,
                r_at_k: 1.0,
                top_5_ids: vec!["chunk-1".into()],
            }],
        };
        let json = report.to_json().expect("serialise should not fail");
        assert!(json.contains("\"mrr\""));
        assert!(json.contains("\"ndcg_at_10\""));
        assert!(json.contains("\"per_query\""));
        assert!(json.contains("\"precision_at_10\""));
        assert!(json.contains("\"recall_at_10\""));
        // Round-trip
        let _: EvalReport = serde_json::from_str(&json).expect("deserialise round-trip");
    }

    /// Precision@K = 0 for empty result list (no divide-by-zero).
    #[test]
    fn eval_precision_empty_results_no_panic() {
        let relevant: HashSet<&str> = ["a"].iter().cloned().collect();
        let score = precision_at_k(&[], &relevant);
        assert_eq!(score, 0.0);
    }

    /// Recall@K = 1.0 when relevant set is empty (vacuously all found).
    #[test]
    fn eval_recall_empty_relevant_set_is_one() {
        let relevant: HashSet<&str> = HashSet::new();
        let score = recall_at_k(&["a", "b"], &relevant);
        assert_eq!(score, 1.0);
    }

    /// NDCG uses log base 2 (not natural log or log10) — CR-A check.
    ///
    /// For 1 relevant doc at rank 0:
    ///   DCG = IDCG = 1/log2(2) = 1/1.0 = 1.0  → NDCG = 1.0
    /// With log10(2) ≈ 0.301, DCG/IDCG would still be 1.0, but for rank 1:
    ///   DCG(log2) = 1/log2(3) ≈ 0.6309; IDCG = 1.0 → NDCG ≈ 0.6309
    ///   DCG(log10) = 1/log10(3) ≈ 0.2096 — distinct value, detects wrong base.
    #[test]
    fn eval_ndcg_uses_log_base_2() {
        let relevant: HashSet<&str> = ["a"].iter().cloned().collect();
        // a is at rank 1 (0-indexed), so i=1, denominator = log2(3)
        let score = ndcg_at_k(&["x", "a"], &relevant, 2);
        // DCG  = 0/log2(2) + 1/log2(3) = 1/log2(3)
        // IDCG = 1/log2(2) = 1.0
        // NDCG = (1/log2(3)) / 1.0 = 1/log2(3) ≈ 0.6309
        let expected = (1.0_f32 / 3.0_f32.log2()) / (1.0_f32 / 2.0_f32.log2());
        assert!(
            (score - expected).abs() < 1e-5,
            "NDCG log2 check: expected {expected}, got {score}"
        );
    }

    // ── MAP@K tests ───────────────────────────────────────────────────────────

    /// MAP@K = 1.0 when all relevant docs appear at the top in order.
    #[test]
    fn map_perfect_ranking() {
        let relevant: HashSet<&str> = ["a", "b"].iter().cloned().collect();
        // Results: a(rel), b(rel), c(irr)  → P@1=1.0, P@2=1.0, P@3 not counted
        // AP = (1.0 + 1.0) / 2 = 1.0
        let score = map_at_k(&["a", "b", "c"], &relevant, 3);
        assert!((score - 1.0).abs() < 1e-6, "MAP perfect: {score}");
    }

    /// MAP@K for interleaved results.
    ///
    /// Ranked: [a(rel), x(irr), b(rel)]   |R| = 2
    /// P@1 = 1/1 = 1.0  (hit at i=1)
    /// P@3 = 2/3        (hit at i=3)
    /// AP  = (1.0 + 2/3) / 2 = (1.0 + 0.6667) / 2 ≈ 0.8333
    #[test]
    fn map_interleaved_manual() {
        let relevant: HashSet<&str> = ["a", "b"].iter().cloned().collect();
        let score = map_at_k(&["a", "x", "b"], &relevant, 3);
        let expected = (1.0_f32 + 2.0 / 3.0) / 2.0;
        assert!(
            (score - expected).abs() < 1e-5,
            "MAP interleaved: expected {expected}, got {score}"
        );
    }

    /// MAP@K = 0.0 when no relevant doc appears in results.
    #[test]
    fn map_zero_when_no_hits() {
        let relevant: HashSet<&str> = ["z"].iter().cloned().collect();
        let score = map_at_k(&["a", "b", "c"], &relevant, 3);
        assert_eq!(score, 0.0, "MAP no-hit: {score}");
    }

    /// MAP@K = 1.0 when relevant set is empty (vacuously perfect).
    #[test]
    fn map_empty_relevant_is_one() {
        let relevant: HashSet<&str> = HashSet::new();
        let score = map_at_k(&["a", "b"], &relevant, 2);
        assert_eq!(score, 1.0);
    }

    /// EvalMetrics::compute populates map_at_k correctly.
    #[test]
    fn eval_metrics_compute_includes_map() {
        let gt = GroundTruth {
            name: "map-test".into(),
            version: "1.0.0".into(),
            queries: vec![EvalQuery {
                query: "q1".into(),
                relevant_chunk_ids: vec!["a".into(), "b".into()],
                notes: None,
            }],
        };
        // Perfect ranking: a then b.
        let results = vec![vec!["a".into(), "b".into(), "c".into()]];
        let metrics = EvalMetrics::compute("test", &gt, &results, 3);
        assert!(
            (metrics.map_at_k - 1.0).abs() < 1e-5,
            "map_at_k should be 1.0 for perfect ranking: {}",
            metrics.map_at_k
        );
        assert!(metrics.latency_ms.is_none(), "latency_ms defaults to None");
    }

    // ── Golden fixture regression test ────────────────────────────────────────
    //
    // Synthetic corpus of 5 chunks, mock search function, ground truth of 3
    // queries.  Tests that the harness produces numerically stable results and
    // detects a simulated 15% MRR degradation.
    //
    // No network, no DB, no embedder — fully in-process.

    /// Synthetic golden fixture: MRR@5 ≥ 0.65 on a 5-chunk mock corpus.
    #[test]
    fn golden_fixture_mrr_gte_065() {
        // Corpus: 5 chunk IDs.
        // Ground truth: 3 queries, each with 1 relevant chunk.
        // Mock retriever: always returns the relevant chunk first.
        let gt = GroundTruth {
            name: "golden".into(),
            version: "1.0.0".into(),
            queries: vec![
                EvalQuery {
                    query: "solar declination formula".into(),
                    relevant_chunk_ids: vec!["chunk-1".into()],
                    notes: None,
                },
                EvalQuery {
                    query: "prayer time calculation method".into(),
                    relevant_chunk_ids: vec!["chunk-3".into()],
                    notes: None,
                },
                EvalQuery {
                    query: "qibla direction great circle".into(),
                    relevant_chunk_ids: vec!["chunk-5".into()],
                    notes: None,
                },
            ],
        };

        // Simulate a strong retriever: relevant always at rank 0.
        let results: Vec<Vec<String>> = gt
            .queries
            .iter()
            .map(|eq| {
                let mut ranked = eq.relevant_chunk_ids.clone();
                ranked.extend(["chunk-2".into(), "chunk-4".into()]);
                ranked
            })
            .collect();

        let metrics = EvalMetrics::compute("mock-strong", &gt, &results, 5);
        assert!(
            metrics.mrr_at_k >= 0.65,
            "golden MRR@5 should be ≥ 0.65, got {}",
            metrics.mrr_at_k
        );
        assert!(
            metrics.map_at_k >= 0.65,
            "golden MAP@5 should be ≥ 0.65, got {}",
            metrics.map_at_k
        );

        // Simulated degraded retriever: relevant at rank 2 (MRR = 0.333).
        let degraded_results: Vec<Vec<String>> = gt
            .queries
            .iter()
            .map(|eq| {
                let mut ranked = vec!["chunk-2".into(), "chunk-4".into()];
                ranked.extend(eq.relevant_chunk_ids.clone());
                ranked
            })
            .collect();

        let degraded = EvalMetrics::compute("mock-degraded", &gt, &degraded_results, 5);
        assert!(
            degraded.has_regression(&metrics, 15.0),
            "15% degradation should fire regression detector"
        );
    }
}
