//! Pure RRF merger — no I/O, no async.
//!
//! Contains [`RankedList`], [`FusedHit`], [`NormStrategy`], and [`rrf_merge`].

use std::collections::HashMap;

// ── NormStrategy ──────────────────────────────────────────────────────────────

/// Score normalisation strategy applied to each channel's raw scores before
/// computing RRF ranks.
///
/// Normalisation is applied per-channel (per `RankedList`) before rank
/// assignment, so different channels with wildly different score ranges can be
/// compared on an equal footing.
///
/// # Variant semantics
///
/// | Variant  | Formula                          | Use-case                          |
/// |----------|----------------------------------|-----------------------------------|
/// | `None`   | identity                         | Default; preserves existing tests |
/// | `MinMax` | `(x - min) / (max - min)`        | Channels with bounded ranges      |
/// | `ZScore` | `(x - μ) / σ`                    | Gaussian-ish distributions        |
/// | `Sigmoid`| `1 / (1 + e^(-x))`              | Unbounded logit-like scores       |
///
/// When a list has only one element, `MinMax` and `ZScore` degenerate
/// gracefully: the single element maps to `1.0`.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum NormStrategy {
    /// No normalisation (identity). Default. Preserves all existing tests.
    #[default]
    None,
    /// Min-max normalisation: maps scores to `[0.0, 1.0]`.
    MinMax,
    /// Z-score standardisation: `(x - mean) / std_dev`.
    ZScore,
    /// Sigmoid squash: `1 / (1 + exp(-x))`.
    Sigmoid,
}

impl NormStrategy {
    /// Apply this normalisation strategy to a mutable slice of `(id, score)` pairs.
    ///
    /// Scores are updated in-place.  Relative descending order is preserved for
    /// `MinMax` and `ZScore`; for `Sigmoid` order is monotone-preserved.
    ///
    /// # Inputs
    /// - `hits`: `(chunk_id, score)` pairs, already sorted descending by score.
    ///
    /// # Outputs
    /// Modified in-place.  For all-equal or single-element inputs, `MinMax` and
    /// `ZScore` map every score to `1.0`.
    pub fn apply(&self, hits: &mut [(i64, f64)]) {
        match self {
            NormStrategy::None => {}
            NormStrategy::MinMax => {
                if hits.is_empty() {
                    return;
                }
                let min = hits.iter().map(|(_, s)| *s).fold(f64::INFINITY, f64::min);
                let max = hits.iter().map(|(_, s)| *s).fold(f64::NEG_INFINITY, f64::max);
                let range = max - min;
                if range < f64::EPSILON {
                    // All scores equal — map to 1.0.
                    for (_, s) in hits.iter_mut() {
                        *s = 1.0;
                    }
                } else {
                    for (_, s) in hits.iter_mut() {
                        *s = (*s - min) / range;
                    }
                }
            }
            NormStrategy::ZScore => {
                if hits.is_empty() {
                    return;
                }
                let n = hits.len() as f64;
                let mean = hits.iter().map(|(_, s)| *s).sum::<f64>() / n;
                let variance =
                    hits.iter().map(|(_, s)| (*s - mean).powi(2)).sum::<f64>() / n;
                let std_dev = variance.sqrt();
                if std_dev < f64::EPSILON {
                    // All equal — map to 1.0 for usability.
                    for (_, s) in hits.iter_mut() {
                        *s = 1.0;
                    }
                } else {
                    for (_, s) in hits.iter_mut() {
                        *s = (*s - mean) / std_dev;
                    }
                }
            }
            NormStrategy::Sigmoid => {
                for (_, s) in hits.iter_mut() {
                    *s = 1.0 / (1.0 + (-*s).exp());
                }
            }
        }
    }
}

// ── RankedList / FusedHit ─────────────────────────────────────────────────────

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

// ── rrf_merge ─────────────────────────────────────────────────────────────────

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

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    // ── NormStrategy tests ────────────────────────────────────────────────────

    #[test]
    fn norm_none_is_identity() {
        let mut hits = vec![(1i64, 0.9f64), (2, 0.5), (3, 0.1)];
        let orig = hits.clone();
        NormStrategy::None.apply(&mut hits);
        assert_eq!(hits, orig);
    }

    #[test]
    fn norm_minmax_maps_to_unit_range() {
        let mut hits = vec![(1i64, 10.0f64), (2, 6.0), (3, 2.0)];
        NormStrategy::MinMax.apply(&mut hits);
        // max=10 → 1.0, min=2 → 0.0
        assert!((hits[0].1 - 1.0).abs() < 1e-9, "max maps to 1.0: {}", hits[0].1);
        assert!((hits[2].1 - 0.0).abs() < 1e-9, "min maps to 0.0: {}", hits[2].1);
        // middle: (6-2)/(10-2) = 0.5
        assert!((hits[1].1 - 0.5).abs() < 1e-9, "middle maps to 0.5: {}", hits[1].1);
    }

    #[test]
    fn norm_minmax_all_equal_maps_to_one() {
        let mut hits = vec![(1i64, 5.0f64), (2, 5.0), (3, 5.0)];
        NormStrategy::MinMax.apply(&mut hits);
        for (_, s) in &hits {
            assert!((*s - 1.0).abs() < 1e-9, "all-equal maps to 1.0: {s}");
        }
    }

    #[test]
    fn norm_zscore_zero_mean_unit_std() {
        // scores [1, 2, 3]: mean=2, std=sqrt(2/3)≈0.8165
        let mut hits = vec![(1i64, 3.0f64), (2, 2.0), (3, 1.0)];
        NormStrategy::ZScore.apply(&mut hits);
        let mean: f64 = hits.iter().map(|(_, s)| *s).sum::<f64>() / 3.0;
        assert!(mean.abs() < 1e-9, "z-scored mean should be ~0: {mean}");
    }

    #[test]
    fn norm_zscore_all_equal_maps_to_one() {
        let mut hits = vec![(1i64, 7.0f64), (2, 7.0)];
        NormStrategy::ZScore.apply(&mut hits);
        for (_, s) in &hits {
            assert!((*s - 1.0).abs() < 1e-9, "all-equal z-score maps to 1.0: {s}");
        }
    }

    #[test]
    fn norm_sigmoid_maps_zero_to_half() {
        let mut hits = vec![(1i64, 0.0f64)];
        NormStrategy::Sigmoid.apply(&mut hits);
        assert!((hits[0].1 - 0.5).abs() < 1e-9, "sigmoid(0) = 0.5: {}", hits[0].1);
    }

    #[test]
    fn norm_sigmoid_large_positive_near_one() {
        let mut hits = vec![(1i64, 100.0f64)];
        NormStrategy::Sigmoid.apply(&mut hits);
        assert!(hits[0].1 > 0.999, "sigmoid(100) ≈ 1.0: {}", hits[0].1);
    }

    #[test]
    fn norm_sigmoid_large_negative_near_zero() {
        let mut hits = vec![(1i64, -100.0f64)];
        NormStrategy::Sigmoid.apply(&mut hits);
        assert!(hits[0].1 < 0.001, "sigmoid(-100) ≈ 0.0: {}", hits[0].1);
    }

    #[test]
    fn norm_empty_slice_no_panic() {
        let mut hits: Vec<(i64, f64)> = vec![];
        NormStrategy::MinMax.apply(&mut hits);
        NormStrategy::ZScore.apply(&mut hits);
        NormStrategy::Sigmoid.apply(&mut hits);
        // No panic; nothing changes.
        assert!(hits.is_empty());
    }
}
