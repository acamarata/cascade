//! Pure RRF merger — no I/O, no async.
//!
//! Contains [`RankedList`], [`FusedHit`], and [`rrf_merge`].

use std::collections::HashMap;

/// A single entry in an input ranked list for [`rrf_merge`].
///
/// The list must already be **sorted highest-score-first** — `rrf_merge`
/// converts position to 1-based rank internally.
#[derive(Debug, Clone)]
pub struct RankedList<'a> {
    /// Human-readable label used for per-source provenance (e.g. `"fts5"`,
    /// `"dense"`, `"sparse"`).
    pub source: &'a str,
    /// Optional weight multiplier applied to every RRF contribution from this
    /// list.  `1.0` is neutral (standard RRF).  Higher values boost the list's
    /// influence; `0.0` disables it entirely.
    pub weight: f64,
    /// Pre-ranked `(chunk_id, score)` pairs, descending by score.
    pub hits: &'a [(i64, f64)],
}

/// Output of [`rrf_merge`] — one fused entry per unique `chunk_id`.
#[derive(Debug, Clone, PartialEq)]
pub struct FusedHit {
    /// The chunk identifier.
    pub chunk_id: i64,
    /// Weighted RRF score: Σ_i  weight_i / (k + rank_i(d)).
    pub rrf_score: f64,
    /// Names of the source lists that contributed to this score (for citation).
    pub sources_hit: Vec<String>,
}

/// Pure RRF merger.
///
/// # Parameters
///
/// * `lists` — up to N pre-ranked input lists, each with an optional weight.
/// * `k`     — RRF smoothing constant (default 60.0).
/// * `top_n` — maximum results to return; `0` means return all.
///
/// # Algorithm
///
/// For each list `i` and each document `d` at 1-based rank `r`:
///
/// ```text
/// rrf_score(d) += weight_i / (k + r)
/// ```
///
/// Documents not present in a list contribute 0.
///
/// ## Tie-breaking
///
/// Ties are broken deterministically by `chunk_id` ascending, giving a stable
/// ordering across calls with identical inputs.
///
/// ## Empty / single-list behaviour
///
/// * Empty list entries are silently skipped.
/// * A single non-empty list degenerates to a pure rank-preserving sort scaled
///   by `weight / (k + rank)`.
///
/// # Example
///
/// ```
/// use cascade_rag::retrieve::rrf::{RankedList, rrf_merge};
///
/// let fts: Vec<(i64, f64)> = vec![(1, 0.9), (2, 0.6)];
/// let dense: Vec<(i64, f64)> = vec![(2, 0.85), (1, 0.7)];
/// let lists = vec![
///     RankedList { source: "fts5",  weight: 1.0, hits: &fts },
///     RankedList { source: "dense", weight: 1.0, hits: &dense },
/// ];
/// let fused = rrf_merge(&lists, 60.0, 10);
/// // chunk_id 1 appeared as rank-1 in fts and rank-2 in dense → higher score than
/// // chunk_id 2 (rank-2 fts, rank-1 dense) because scores are symmetric here,
/// // but fts rank-1 beats dense rank-1 for chunk_id 2 is offset by fts rank-2.
/// assert!(!fused.is_empty());
/// ```
pub fn rrf_merge(lists: &[RankedList<'_>], k: f64, top_n: usize) -> Vec<FusedHit> {
    // Accumulate: chunk_id -> (rrf_score, sources)
    let mut scores: HashMap<i64, (f64, Vec<String>)> = HashMap::new();

    for list in lists {
        if list.hits.is_empty() {
            continue;
        }
        for (zero_based, (chunk_id, _score)) in list.hits.iter().enumerate() {
            let rank = (zero_based + 1) as f64; // 1-based
            let contribution = list.weight / (k + rank);
            let entry = scores.entry(*chunk_id).or_insert_with(|| (0.0, Vec::new()));
            entry.0 += contribution;
            if !entry.1.iter().any(|s| s == list.source) {
                entry.1.push(list.source.to_owned());
            }
        }
    }

    let mut fused: Vec<FusedHit> = scores
        .into_iter()
        .map(|(chunk_id, (rrf_score, sources_hit))| FusedHit {
            chunk_id,
            rrf_score,
            sources_hit,
        })
        .collect();

    // Primary sort: descending RRF score.
    // Tie-break: ascending chunk_id (deterministic).
    fused.sort_by(|a, b| {
        b.rrf_score
            .partial_cmp(&a.rrf_score)
            .unwrap_or(std::cmp::Ordering::Equal)
            .then_with(|| a.chunk_id.cmp(&b.chunk_id))
    });

    if top_n > 0 {
        fused.truncate(top_n);
    }
    fused
}
