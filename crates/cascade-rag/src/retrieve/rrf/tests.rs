//! Tests for RRF merge logic and exclusion integration.

use super::super::exclusion::{ExclusionConfig, ExclusionSet};
use super::merge::{rrf_merge, RankedList};
use cascade_types::retriever::RetrievalHit;
use std::path::PathBuf;

// Helper: approximate float equality.
fn approx_eq(a: f64, b: f64) -> bool {
    (a - b).abs() < 1e-10
}

/// Build a RetrievalHit with the given chunk_id and file_path.
fn hit(chunk_id: i64, file_path: Option<&str>, score: f32) -> RetrievalHit {
    RetrievalHit {
        chunk_id: chunk_id.to_string(),
        text: "content".to_string(),
        file_path: file_path.map(PathBuf::from),
        start_line: None,
        end_line: None,
        score,
        rank: 0,
        tier: None,
    }
}

/// Two lists with known overlap — verify RRF scores match manual calculation.
///
/// fts:   [(1, 0.9), (2, 0.6)]         rank 1 → id=1, rank 2 → id=2
/// dense: [(2, 0.85), (1, 0.7)]         rank 1 → id=2, rank 2 → id=1
///
/// k=60, weight=1.0:
///   id=1: 1/(60+1) + 1/(60+2) = 1/61 + 1/62
///   id=2: 1/(60+2) + 1/(60+1) = 1/62 + 1/61  (same! symmetric)
///   tie broken by chunk_id ascending → id=1 first.
#[test]
fn known_vector_fusion() {
    let fts: Vec<(i64, f64)> = vec![(1, 0.9), (2, 0.6)];
    let dense: Vec<(i64, f64)> = vec![(2, 0.85), (1, 0.7)];
    let lists = vec![
        RankedList {
            source: "fts5",
            weight: 1.0,
            hits: &fts,
        },
        RankedList {
            source: "dense",
            weight: 1.0,
            hits: &dense,
        },
    ];
    let fused = rrf_merge(&lists, 60.0, 10);

    assert_eq!(fused.len(), 2, "expect 2 unique chunks");

    let expected_id1 = 1.0 / 61.0 + 1.0 / 62.0;
    let expected_id2 = 1.0 / 62.0 + 1.0 / 61.0;
    assert!(approx_eq(fused[0].rrf_score, expected_id1), "id=1 score");
    assert!(approx_eq(fused[1].rrf_score, expected_id2), "id=2 score");

    // Tie-break: ascending chunk_id → id=1 before id=2.
    assert_eq!(fused[0].chunk_id, 1);
    assert_eq!(fused[1].chunk_id, 2);

    // Both lists contributed to both chunks.
    assert!(fused[0].sources_hit.contains(&"fts5".to_string()));
    assert!(fused[0].sources_hit.contains(&"dense".to_string()));
}

/// Source weights: doubling the weight of one list doubles its contribution.
#[test]
fn source_weights() {
    let fts: Vec<(i64, f64)> = vec![(10, 0.9)];
    let dense: Vec<(i64, f64)> = vec![(20, 0.85)];
    let lists = vec![
        RankedList {
            source: "fts5",
            weight: 2.0,
            hits: &fts,
        },
        RankedList {
            source: "dense",
            weight: 1.0,
            hits: &dense,
        },
    ];
    let fused = rrf_merge(&lists, 60.0, 10);

    // id=10 gets 2.0/(60+1), id=20 gets 1.0/(60+1).
    let score_10 = fused.iter().find(|h| h.chunk_id == 10).unwrap().rrf_score;
    let score_20 = fused.iter().find(|h| h.chunk_id == 20).unwrap().rrf_score;
    assert!(
        approx_eq(score_10, 2.0 / 61.0),
        "weighted fts score for id=10"
    );
    assert!(
        approx_eq(score_20, 1.0 / 61.0),
        "unweighted dense score for id=20"
    );
    // Higher weight → higher score → id=10 ranked first.
    assert_eq!(fused[0].chunk_id, 10);
}

/// Tie-break: two chunks with identical scores are sorted by ascending chunk_id.
#[test]
fn tie_break_determinism() {
    let list: Vec<(i64, f64)> = vec![(100, 1.0), (50, 1.0)];
    // Second list gives same rank to both ids (one each, mirrored).
    let list2: Vec<(i64, f64)> = vec![(50, 1.0), (100, 1.0)];
    let lists = vec![
        RankedList {
            source: "a",
            weight: 1.0,
            hits: &list,
        },
        RankedList {
            source: "b",
            weight: 1.0,
            hits: &list2,
        },
    ];
    let fused = rrf_merge(&lists, 60.0, 10);
    // id=50: rank 2 in a + rank 1 in b → 1/62 + 1/61
    // id=100: rank 1 in a + rank 2 in b → 1/61 + 1/62  (same scores, tie)
    // Tie broken by id ascending → 50 before 100.
    assert_eq!(fused[0].chunk_id, 50);
    assert_eq!(fused[1].chunk_id, 100);
}

/// Empty lists: all-empty input → empty output.
#[test]
fn empty_lists_return_empty() {
    let lists: Vec<RankedList<'_>> = vec![];
    let fused = rrf_merge(&lists, 60.0, 10);
    assert!(fused.is_empty());
}

/// Empty lists: one non-empty, one empty → non-empty list drives results.
#[test]
fn one_empty_one_non_empty() {
    let fts: Vec<(i64, f64)> = vec![(1, 0.9), (2, 0.6)];
    let empty: Vec<(i64, f64)> = vec![];
    let lists = vec![
        RankedList {
            source: "fts5",
            weight: 1.0,
            hits: &fts,
        },
        RankedList {
            source: "dense",
            weight: 1.0,
            hits: &empty,
        },
    ];
    let fused = rrf_merge(&lists, 60.0, 10);
    assert_eq!(fused.len(), 2);
    // id=1 is rank-1 → higher rrf than id=2 (rank-2).
    assert_eq!(fused[0].chunk_id, 1);
    assert!(approx_eq(fused[0].rrf_score, 1.0 / 61.0));
    assert!(approx_eq(fused[1].rrf_score, 1.0 / 62.0));
}

/// Single-list passthrough: results are ordered rank-preservingly.
#[test]
fn single_list_passthrough() {
    let fts: Vec<(i64, f64)> = vec![(5, 0.99), (3, 0.75), (7, 0.4)];
    let lists = vec![RankedList {
        source: "fts5",
        weight: 1.0,
        hits: &fts,
    }];
    let fused = rrf_merge(&lists, 60.0, 0); // top_n=0 → return all
    assert_eq!(fused.len(), 3);
    // rank 1 → id=5 → score 1/61; rank 2 → id=3 → 1/62; rank 3 → id=7 → 1/63
    assert_eq!(fused[0].chunk_id, 5);
    assert_eq!(fused[1].chunk_id, 3);
    assert_eq!(fused[2].chunk_id, 7);
}

/// Top-N cut: only `top_n` results returned.
#[test]
fn top_n_cut() {
    let fts: Vec<(i64, f64)> = vec![(1, 1.0), (2, 0.9), (3, 0.8), (4, 0.7)];
    let lists = vec![RankedList {
        source: "fts5",
        weight: 1.0,
        hits: &fts,
    }];
    let fused = rrf_merge(&lists, 60.0, 2);
    assert_eq!(fused.len(), 2);
    assert_eq!(fused[0].chunk_id, 1);
    assert_eq!(fused[1].chunk_id, 2);
}

/// Provenance: sources_hit lists only the sources that contributed.
#[test]
fn provenance_sources_hit() {
    // id=99 only in fts; id=77 only in dense; id=55 in both.
    let fts: Vec<(i64, f64)> = vec![(55, 0.9), (99, 0.5)];
    let dense: Vec<(i64, f64)> = vec![(55, 0.8), (77, 0.4)];
    let lists = vec![
        RankedList {
            source: "fts5",
            weight: 1.0,
            hits: &fts,
        },
        RankedList {
            source: "dense",
            weight: 1.0,
            hits: &dense,
        },
    ];
    let fused = rrf_merge(&lists, 60.0, 10);

    let hit_55 = fused.iter().find(|h| h.chunk_id == 55).unwrap();
    assert!(hit_55.sources_hit.contains(&"fts5".to_string()));
    assert!(hit_55.sources_hit.contains(&"dense".to_string()));

    let hit_99 = fused.iter().find(|h| h.chunk_id == 99).unwrap();
    assert_eq!(hit_99.sources_hit, vec!["fts5".to_string()]);

    let hit_77 = fused.iter().find(|h| h.chunk_id == 77).unwrap();
    assert_eq!(hit_77.sources_hit, vec!["dense".to_string()]);
}

/// Three-list fusion: FTS5 + dense + sparse — verifies N-list path.
#[test]
fn three_list_fusion() {
    let fts: Vec<(i64, f64)> = vec![(1, 0.9), (2, 0.5)];
    let dense: Vec<(i64, f64)> = vec![(1, 0.8), (3, 0.7)];
    let sparse: Vec<(i64, f64)> = vec![(2, 0.6), (1, 0.3)];
    let lists = vec![
        RankedList {
            source: "fts5",
            weight: 1.0,
            hits: &fts,
        },
        RankedList {
            source: "dense",
            weight: 1.0,
            hits: &dense,
        },
        RankedList {
            source: "sparse",
            weight: 1.0,
            hits: &sparse,
        },
    ];
    let fused = rrf_merge(&lists, 60.0, 10);
    // id=1 appears in all 3 lists → highest cumulative RRF.
    assert_eq!(fused[0].chunk_id, 1);
    assert_eq!(fused[0].sources_hit.len(), 3);
}

// ── 5-channel fusion tests ────────────────────────────────────────────────

/// Curated-description channel boosts a sparse-body doc when its description
/// matches.  Without the curated channel the sparse doc would lose to a rich
/// body doc; with it, the two compete on equal footing (or the curated doc wins).
///
/// Simulated with pure `rrf_merge` by giving the sparse doc a strong curated
/// rank and a weak FTS rank, and the body doc a strong FTS rank only.
#[test]
fn curated_channel_boosts_sparse_body_doc() {
    // doc=1: strong body (fts rank 1), no curated hit.
    // doc=2: weak body (fts rank 5), but curated rank 1.
    let fts: Vec<(i64, f64)> = vec![(1, 0.95), (3, 0.5), (4, 0.4), (5, 0.3), (2, 0.1)];
    let curated: Vec<(i64, f64)> = vec![(2, 0.99)]; // doc=2 matches description

    let lists_without = vec![RankedList {
        source: "fts5",
        weight: 1.0,
        hits: &fts,
    }];
    let fused_without = rrf_merge(&lists_without, 60.0, 5);

    let lists_with = vec![
        RankedList {
            source: "fts5",
            weight: 1.0,
            hits: &fts,
        },
        RankedList {
            source: "curated",
            weight: 0.8,
            hits: &curated,
        },
    ];
    let fused_with = rrf_merge(&lists_with, 60.0, 5);

    // Without curated: doc=2 must rank last (it was rank-5 in FTS).
    let rank_without = fused_without
        .iter()
        .position(|h| h.chunk_id == 2)
        .expect("doc=2 must appear");
    assert_eq!(
        rank_without, 4,
        "doc=2 must be last without curated channel"
    );

    // With curated: doc=2 rank must improve (move closer to the front).
    let rank_with = fused_with
        .iter()
        .position(|h| h.chunk_id == 2)
        .expect("doc=2 must appear");
    assert!(
        rank_with < rank_without,
        "curated channel must improve doc=2 rank ({rank_with} < {rank_without})"
    );
}

/// Recency breaks ties toward the newer document.
///
/// Two docs with identical FTS and curated scores.  The recency channel
/// gives a higher score to doc=10 (newer).  After fusion, doc=10 must rank
/// first.
#[test]
fn recency_breaks_ties_toward_newer_doc() {
    // doc=10 and doc=20 are symmetric in FTS (rank 1 each in their own list
    // → simulate tied scores by putting them at the same rank in both).
    let fts: Vec<(i64, f64)> = vec![(10, 0.8), (20, 0.8)]; // tied FTS scores
                                                           // Recency: doc=10 is newer → rank 1; doc=20 is older → rank 2.
    let recency: Vec<(i64, f64)> = vec![(10, 1.0), (20, 0.5)];

    let lists = vec![
        RankedList {
            source: "fts5",
            weight: 1.0,
            hits: &fts,
        },
        RankedList {
            source: "recency",
            weight: 0.5,
            hits: &recency,
        },
    ];
    let fused = rrf_merge(&lists, 60.0, 10);

    // doc=10 should be first because it wins the recency channel.
    assert_eq!(
        fused[0].chunk_id, 10,
        "newer doc must rank first after recency tie-break"
    );
    assert!(fused[0].sources_hit.contains(&"recency".to_string()));
}

/// Five-channel fusion: when all 5 channels contribute to doc=1, it wins.
#[test]
fn five_channel_fusion_all_sources_contribute() {
    let fts: Vec<(i64, f64)> = vec![(1, 0.9), (2, 0.5)];
    let dense: Vec<(i64, f64)> = vec![(1, 0.85), (3, 0.7)];
    let curated: Vec<(i64, f64)> = vec![(1, 0.99), (4, 0.6)];
    let recency: Vec<(i64, f64)> = vec![(1, 1.0), (5, 0.4)];
    let sparse: Vec<(i64, f64)> = vec![(1, 0.75), (6, 0.3)];

    let lists = vec![
        RankedList {
            source: "fts5",
            weight: 1.0,
            hits: &fts,
        },
        RankedList {
            source: "dense",
            weight: 1.0,
            hits: &dense,
        },
        RankedList {
            source: "curated",
            weight: 0.8,
            hits: &curated,
        },
        RankedList {
            source: "recency",
            weight: 0.5,
            hits: &recency,
        },
        RankedList {
            source: "sparse",
            weight: 1.0,
            hits: &sparse,
        },
    ];
    let fused = rrf_merge(&lists, 60.0, 10);

    assert_eq!(
        fused[0].chunk_id, 1,
        "doc=1 (all 5 channels) must rank first"
    );
    assert_eq!(
        fused[0].sources_hit.len(),
        5,
        "all 5 sources must be in provenance"
    );
}

/// Missing channels (all empty except one) must not crash — single-channel fallback.
#[test]
fn missing_channels_degrade_gracefully() {
    let fts: Vec<(i64, f64)> = vec![(42, 0.9)];
    let empty: Vec<(i64, f64)> = vec![];

    // Only the FTS list is non-empty; curated and recency are empty.
    let lists = vec![
        RankedList {
            source: "fts5",
            weight: 1.0,
            hits: &fts,
        },
        RankedList {
            source: "curated",
            weight: 0.8,
            hits: &empty,
        },
        RankedList {
            source: "recency",
            weight: 0.5,
            hits: &empty,
        },
    ];
    let fused = rrf_merge(&lists, 60.0, 10);

    assert_eq!(fused.len(), 1, "must return the single FTS hit");
    assert_eq!(fused[0].chunk_id, 42);
    assert_eq!(fused[0].sources_hit, vec!["fts5".to_string()]);
}

/// Weight=0.0 for a channel effectively disables its influence.
#[test]
fn zero_weight_channel_has_no_influence() {
    let fts: Vec<(i64, f64)> = vec![(1, 0.9)];
    let recency: Vec<(i64, f64)> = vec![(2, 1.0), (1, 0.5)]; // recency prefers doc=2

    // With weight=0.0 on recency, doc=2 gets 0 contribution from recency.
    let lists_zero = vec![
        RankedList {
            source: "fts5",
            weight: 1.0,
            hits: &fts,
        },
        RankedList {
            source: "recency",
            weight: 0.0,
            hits: &recency,
        },
    ];
    let fused_zero = rrf_merge(&lists_zero, 60.0, 10);

    // doc=1 wins because recency has zero weight.
    assert_eq!(
        fused_zero[0].chunk_id, 1,
        "zero-weight recency must not override FTS"
    );
}

// ── Adversarial exclusion tests ───────────────────────────────────────────
//
// These tests prove that a locked doc CANNOT surface via any channel, even
// when its content/recency/description would otherwise rank it #1.
//
// Strategy: use ExclusionSet directly on pair lists (simulating layer 1 of
// the retriever) and on RetrievalHit lists (layer 2).  The tests are
// channel-by-channel plus a full 5-channel fusion scenario.

/// The locked doc would rank #1 on FTS alone.  After layer-1 exclusion it
/// must not appear in the pair list.
#[test]
fn adversarial_fts_channel_locked_doc_absent() {
    let ex = ExclusionSet::compile(&ExclusionConfig::from_patterns(["/locked"]));

    // Locked doc (id=999) would be rank-1 on FTS; public doc (id=1) is rank-2.
    let raw_fts: Vec<(i64, f64)> = vec![(999, 0.99), (1, 0.5)];

    let filtered = ex.filter_pairs(&raw_fts, |id| match id {
        999 => Some("/locked/secret.md".to_string()),
        1 => Some("/public/safe.md".to_string()),
        _ => None,
    });

    assert!(
        !filtered.iter().any(|(id, _)| *id == 999),
        "locked doc must not appear in FTS pair list after layer-1 exclusion"
    );
    assert!(
        filtered.iter().any(|(id, _)| *id == 1),
        "public doc must still appear"
    );
}

/// The locked doc would rank #1 on vector alone.
#[test]
fn adversarial_vector_channel_locked_doc_absent() {
    let ex = ExclusionSet::compile(&ExclusionConfig::from_patterns(["/locked"]));
    let raw_vec: Vec<(i64, f64)> = vec![(999, 0.98), (2, 0.6)];

    let filtered = ex.filter_pairs(&raw_vec, |id| match id {
        999 => Some("/locked/secret.md".to_string()),
        _ => None,
    });

    assert!(!filtered.iter().any(|(id, _)| *id == 999));
}

/// The locked doc would rank #1 on the curated-description channel.
#[test]
fn adversarial_curated_channel_locked_doc_absent() {
    let ex = ExclusionSet::compile(&ExclusionConfig::from_patterns(["/locked"]));
    let raw_curated: Vec<(i64, f64)> = vec![(999, 0.97), (3, 0.7)];

    let filtered = ex.filter_pairs(&raw_curated, |id| match id {
        999 => Some("/locked/meta.md".to_string()),
        _ => None,
    });

    assert!(!filtered.iter().any(|(id, _)| *id == 999));
}

/// The locked doc would rank #1 on the recency channel (newest file).
#[test]
fn adversarial_recency_channel_locked_doc_absent() {
    let ex = ExclusionSet::compile(&ExclusionConfig::from_patterns(["/locked"]));
    let raw_recency: Vec<(i64, f64)> = vec![(999, 1.0), (4, 0.8)];

    let filtered = ex.filter_pairs(&raw_recency, |id| match id {
        999 => Some("/locked/diary.md".to_string()),
        _ => None,
    });

    assert!(!filtered.iter().any(|(id, _)| *id == 999));
}

/// Full 5-channel fusion: even when the locked doc ranks #1 in ALL 5
/// channels, it must be absent from the fused result after layer-1 filtering.
#[test]
fn adversarial_five_channel_fusion_locked_doc_absent() {
    let ex = ExclusionSet::compile(&ExclusionConfig::from_patterns(["/locked"]));

    // Locked doc is rank-1 everywhere; public docs are rank-2.
    let raw_fts: Vec<(i64, f64)> = vec![(999, 0.99), (1, 0.5)];
    let raw_vec: Vec<(i64, f64)> = vec![(999, 0.98), (2, 0.6)];
    let raw_curated: Vec<(i64, f64)> = vec![(999, 0.97), (3, 0.7)];
    let raw_recency: Vec<(i64, f64)> = vec![(999, 1.0), (4, 0.8)];
    let raw_sparse: Vec<(i64, f64)> = vec![(999, 0.95), (5, 0.4)];

    let path_of = |id: i64| match id {
        999 => Some("/locked/top-secret.md".to_string()),
        _ => None,
    };

    let fts_pairs = ex.filter_pairs(&raw_fts, path_of);
    let vec_pairs = ex.filter_pairs(&raw_vec, path_of);
    let curated_pairs = ex.filter_pairs(&raw_curated, path_of);
    let recency_pairs = ex.filter_pairs(&raw_recency, path_of);
    let sparse_pairs = ex.filter_pairs(&raw_sparse, path_of);

    // None of the filtered lists must contain the locked id.
    for (label, pairs) in [
        ("fts", &fts_pairs),
        ("vec", &vec_pairs),
        ("curated", &curated_pairs),
        ("recency", &recency_pairs),
        ("sparse", &sparse_pairs),
    ] {
        assert!(
            !pairs.iter().any(|(id, _)| *id == 999),
            "locked doc must not appear in {label} channel after layer-1 exclusion"
        );
    }

    // Fuse the clean lists — locked id must not appear in fusion output.
    let lists = vec![
        RankedList {
            source: "fts5",
            weight: 1.0,
            hits: &fts_pairs,
        },
        RankedList {
            source: "dense",
            weight: 1.0,
            hits: &vec_pairs,
        },
        RankedList {
            source: "curated",
            weight: 0.8,
            hits: &curated_pairs,
        },
        RankedList {
            source: "recency",
            weight: 0.5,
            hits: &recency_pairs,
        },
        RankedList {
            source: "sparse",
            weight: 1.0,
            hits: &sparse_pairs,
        },
    ];
    let fused = rrf_merge(&lists, 60.0, 20);
    assert!(
        !fused.iter().any(|h| h.chunk_id == 999),
        "locked doc must not appear in 5-channel RRF fusion output"
    );
}

/// Prefix exclusion: a directory-locked prefix excludes ALL children.
#[test]
fn adversarial_prefix_locks_all_children() {
    let ex = ExclusionSet::compile(&ExclusionConfig::from_patterns(["/private"]));

    let paths_to_check = [
        "/private/a.md",
        "/private/sub/b.md",
        "/private/sub/deep/c.pdf",
        "/private/",
    ];
    for p in paths_to_check {
        assert!(
            ex.is_excluded(p),
            "'{p}' must be excluded under /private prefix"
        );
    }
    // Sibling path must not be excluded.
    assert!(!ex.is_excluded("/private-other/file.md"));
    assert!(!ex.is_excluded("/public/file.md"));
}

/// Layer-2 post-filter: a locked doc that somehow slips past layer 1 (e.g.
/// it had no file_path at the channel level) is caught by filter_hits.
#[test]
fn adversarial_layer2_catches_leaked_hit() {
    let ex = ExclusionSet::compile(&ExclusionConfig::from_patterns(["/locked"]));

    // Simulate a hit that came through layer 1 without a path (kept, fail-open)
    // but now has a file_path set (e.g. from the hit-map join in RrfRetriever).
    let hits = vec![
        hit(999, Some("/locked/leaked.md"), 0.99), // would-be winner, locked
        hit(1, Some("/public/safe.md"), 0.85),
        hit(2, None, 0.70), // no path → kept
    ];

    let filtered = ex.filter_hits(hits);

    assert!(
        !filtered.iter().any(|h| h.chunk_id == "999"),
        "layer-2 must catch the locked hit that leaked past layer 1"
    );
    assert!(filtered.iter().any(|h| h.chunk_id == "1"));
    assert!(filtered.iter().any(|h| h.chunk_id == "2"));
}

/// Removing the exclusion makes the locked doc appear (proves the test is real).
#[test]
fn removing_exclusion_unlocks_doc() {
    // With exclusion — doc 999 must not appear.
    let ex_on = ExclusionSet::compile(&ExclusionConfig::from_patterns(["/locked"]));
    let hits_on = vec![
        hit(999, Some("/locked/secret.md"), 0.99),
        hit(1, Some("/public/safe.md"), 0.85),
    ];
    let filtered_on = ex_on.filter_hits(hits_on);
    assert!(!filtered_on.iter().any(|h| h.chunk_id == "999"));

    // Without exclusion — doc 999 must appear.
    let ex_off = ExclusionSet::compile(&ExclusionConfig::default());
    let hits_off = vec![
        hit(999, Some("/locked/secret.md"), 0.99),
        hit(1, Some("/public/safe.md"), 0.85),
    ];
    let filtered_off = ex_off.filter_hits(hits_off);
    assert!(
        filtered_off.iter().any(|h| h.chunk_id == "999"),
        "with no exclusion, the doc must appear (proves test is real)"
    );
}

/// Empty exclusion config = no filtering; existing tests remain unaffected.
#[test]
fn empty_exclusion_no_filtering_regression() {
    let ex = ExclusionSet::compile(&ExclusionConfig::default());
    let pairs: Vec<(i64, f64)> = vec![(1, 0.9), (2, 0.8), (3, 0.7)];
    let filtered = ex.filter_pairs(&pairs, |_| Some("/any/path.md".to_string()));
    assert_eq!(
        filtered.len(),
        3,
        "empty exclusion must not filter anything"
    );
}
