//! E-01 integration test: full ingest-to-search round-trip.
//!
//! # Purpose
//!
//! Exercises the complete pipeline: fixture files on disk → IngestPipeline →
//! SQLite index → search() → RagCitation results with verified path + line spans.
//!
//! # Test variants
//!
//! ## Fast tests (no #[ignore])
//!
//! - `round_trip_fts_finds_seeded_content`: 3 fixture files, ingest, FTS search
//!   returns correct citation paths and non-empty line spans.
//! - `ingest_idempotency`: second ingest of the same files returns skipped=true
//!   for all and chunk count in DB is unchanged.
//! - `changed_file_re_ingest`: modify one file, re-ingest → new content is returned.
//! - `fts_vs_hybrid_parity`: FTS-only vs hybrid (vec_enabled) return overlapping
//!   top results for the same query.
//! - `rerank_seam_exercised`: `rerank_enabled=true` with `NoopReranker` injected →
//!   results have `reranker_score` populated and ordering is stable.
//!
//! ## Slow test (#[ignore])
//!
//! - `slow_full_memory_50_files`: 50 synthetic files, 20 queries, MRR checks.
//!   Run with `cargo test -p cascade-rag -- --ignored`.
//!
//! # Ticket
//!
//! T-P4-E01-30
//!
//! SPORT: MASTER-CRATES.md → cascade-rag integration test

use std::fs;
use std::path::{Path, PathBuf};
use std::sync::Arc;

use rusqlite::Connection;
use tokio::sync::Mutex;

use cascade_rag::{
    db::run_migrations,
    embed::MockEmbedModel,
    ingest::{IngestConfig, IngestPipeline},
    search::{search, SearchConfig},
};
use cascade_types::reranker::Reranker;
use cascade_types::NoopReranker;

// ── Fixture helpers ───────────────────────────────────────────────────────────

/// Create a fresh temp directory for one test. Returns the path.
fn make_tempdir(test_name: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "cascade-e01-{}-{}",
        test_name,
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.subsec_nanos())
            .unwrap_or(0)
    ));
    fs::create_dir_all(&dir).expect("failed to create temp dir");
    dir
}

/// Seed a mini-project fixture with deterministic content.
///
/// Creates:
/// - `CLAUDE.md` — instruction cascade docs (queried with "instruction cascade tiers")
/// - `src/lib.rs` — Rust source with search/retrieval keywords
/// - `config.json` — JSON config with pipeline settings
///
/// Returns the list of seeded paths.
fn seed_fixture_files(base: &Path) -> Vec<PathBuf> {
    fs::create_dir_all(base.join("src")).expect("mkdir src");

    let claude_md = base.join("CLAUDE.md");
    fs::write(
        &claude_md,
        "# Cascade Instruction Cascade\n\
         \n\
         The instruction cascade resolves tiers: GCI → ASI → PPI → PRI → PAI.\n\
         Each tier is more specific and overrides the tier above it.\n\
         \n\
         ## GCI — Global Claude Instructions\n\
         \n\
         Applies to every project. Defines delegation, autonomy, and writing rules.\n\
         \n\
         ## ASI — All Sites Instructions\n\
         \n\
         Applies to all coding projects under ~/Sites/. Backend and frontend policies.\n\
         \n\
         ## PPI — Per-Project Instructions\n\
         \n\
         Scoped to one multi-repo project. References higher tiers, never duplicates.\n",
    )
    .expect("write CLAUDE.md");

    let lib_rs = base.join("src/lib.rs");
    fs::write(
        &lib_rs,
        "//! cascade-rag: Retrieval Augmented Generation pipeline.\n\
         //!\n\
         //! The search function orchestrates FTS5 keyword retrieval,\n\
         //! dense vector KNN search, RRF score fusion, and optional reranking.\n\
         \n\
         pub mod search;\n\
         pub mod ingest;\n\
         pub mod embed;\n\
         pub mod rerank;\n\
         \n\
         /// Execute the RAG pipeline for a query string.\n\
         /// Returns ranked citations with file paths and line spans.\n\
         pub async fn search_query(query: &str) -> Vec<String> {\n\
             vec![]\n\
         }\n",
    )
    .expect("write lib.rs");

    let config_json = base.join("config.json");
    fs::write(
        &config_json,
        r#"{
  "pipeline": {
    "fts5_enabled": true,
    "vec_enabled": false,
    "rerank_enabled": false,
    "k": 10,
    "rrf_k": 60.0
  },
  "ingest": {
    "embed_batch_size": 32,
    "max_chunk_chars": 800
  },
  "model": {
    "embed": "bge-m3",
    "reranker": "bge-reranker-v2-m3"
  }
}"#,
    )
    .expect("write config.json");

    vec![claude_md, lib_rs, config_json]
}

/// Register the sqlite-vec extension so vec0 virtual tables resolve in
/// migrations (no-op without the `vec` feature).
#[cfg(feature = "vec")]
fn load_vec_extension() {
    use rusqlite::ffi::sqlite3_auto_extension;
    unsafe {
        sqlite3_auto_extension(Some(std::mem::transmute(
            sqlite_vec::sqlite3_vec_init as *const (),
        )));
    }
}

/// Build a file-backed DB at `path` and run migrations.
fn open_file_db(path: &Path) -> Arc<Mutex<Connection>> {
    #[cfg(feature = "vec")]
    load_vec_extension();
    let conn = Connection::open(path).expect("open db");
    run_migrations(&conn).expect("migrations");
    Arc::new(Mutex::new(conn))
}

// ── Fast tests ────────────────────────────────────────────────────────────────

/// Full round-trip: seed 3 fixture files → ingest → FTS search → citation paths verified.
#[tokio::test]
async fn round_trip_fts_finds_seeded_content() {
    let base = make_tempdir("roundtrip");
    let files = seed_fixture_files(&base);
    let db_path = base.join("index.db");

    // Ingest
    let conn_ingest = Connection::open(&db_path).expect("ingest db");
    run_migrations(&conn_ingest).expect("migrations");
    let embed = Arc::new(MockEmbedModel::new(1024));
    let pipeline = IngestPipeline::new(
        conn_ingest,
        Arc::clone(&embed) as _,
        IngestConfig::default(),
    );
    let (results, errors) = pipeline.ingest_files(files.iter().map(|p| p.as_path()));
    assert!(errors.is_empty(), "ingest should not error: {:?}", errors);
    assert_eq!(results.len(), 3, "all 3 fixture files should be ingested");
    for r in &results {
        assert!(!r.skipped, "first ingest must not be skipped");
        assert!(
            r.chunks_created > 0,
            "each file must produce at least 1 chunk"
        );
    }

    let cfg = SearchConfig {
        k: 5,
        fts5_enabled: true,
        vec_enabled: false,
        rerank_enabled: false,
        ..Default::default()
    };

    // Use single keyword queries — the FTS5 sanitiser wraps multi-word queries in
    // phrase quotes which requires exact word order; single terms always match.
    // Search for "tiers" — a unique term in CLAUDE.md content.
    let results = {
        let conn_s = open_file_db(&db_path);
        let ea: Arc<dyn cascade_rag::embed::EmbedModel> = Arc::new(MockEmbedModel::new(1024));
        search("tiers", &cfg, conn_s, ea, None, None)
            .await
            .expect("search should not fail")
    };

    assert!(
        !results.is_empty(),
        "FTS search for 'tiers' should return at least one result"
    );

    // Verify citation path contains CLAUDE.md
    let found_claude = results.iter().any(|r| {
        r.source_path
            .to_str()
            .map(|s| s.contains("CLAUDE.md"))
            .unwrap_or(false)
    });
    assert!(
        found_claude,
        "CLAUDE.md must appear in search results for 'tiers'"
    );

    // Also verify lib.rs is findable via its unique term "retrieval".
    let results_lib = {
        let conn_s = open_file_db(&db_path);
        let ea: Arc<dyn cascade_rag::embed::EmbedModel> = Arc::new(MockEmbedModel::new(1024));
        search("retrieval", &cfg, conn_s, ea, None, None)
            .await
            .expect("search for retrieval should not fail")
    };
    let found_lib = results_lib.iter().any(|r| {
        r.source_path
            .to_str()
            .map(|s| s.contains("lib.rs"))
            .unwrap_or(false)
    });
    assert!(
        found_lib,
        "lib.rs must appear in search results for 'retrieval'"
    );

    // Verify line spans are non-zero
    for r in &results {
        assert!(r.line_end >= r.line_start, "line_end must be >= line_start");
        assert!(r.line_start > 0, "line_start must be 1-based (>0)");
    }
}

/// Second ingest of the same files returns skipped=true for all; chunk count unchanged.
#[tokio::test]
async fn ingest_idempotency() {
    let base = make_tempdir("idempotency");
    let files = seed_fixture_files(&base);
    let db_path = base.join("index.db");

    let embed = Arc::new(MockEmbedModel::new(1024));

    // First ingest
    {
        let conn = Connection::open(&db_path).expect("db");
        run_migrations(&conn).expect("migrations");
        let pipeline = IngestPipeline::new(conn, Arc::clone(&embed) as _, IngestConfig::default());
        let (results, errors) = pipeline.ingest_files(files.iter().map(|p| p.as_path()));
        assert!(errors.is_empty());
        assert!(
            results.iter().all(|r| !r.skipped),
            "first ingest: none skipped"
        );
    }

    let count_after_first = {
        let c = Connection::open(&db_path).expect("db");
        c.query_row("SELECT COUNT(*) FROM rag_chunks", [], |r| {
            r.get::<_, i64>(0)
        })
        .expect("count")
    };

    // Second ingest (same file bytes, same hashes)
    {
        let conn = Connection::open(&db_path).expect("db");
        run_migrations(&conn).expect("migrations");
        let pipeline = IngestPipeline::new(conn, Arc::clone(&embed) as _, IngestConfig::default());
        let (results, errors) = pipeline.ingest_files(files.iter().map(|p| p.as_path()));
        assert!(errors.is_empty());
        for r in &results {
            assert!(
                r.skipped,
                "second ingest of unchanged file must be skipped: source_id={}",
                r.source_id
            );
            assert_eq!(
                r.chunks_created, 0,
                "skipped files must report 0 chunks_created"
            );
        }
    }

    let count_after_second = {
        let c = Connection::open(&db_path).expect("db");
        c.query_row("SELECT COUNT(*) FROM rag_chunks", [], |r| {
            r.get::<_, i64>(0)
        })
        .expect("count")
    };

    assert_eq!(
        count_after_first, count_after_second,
        "chunk count must be unchanged after idempotent re-ingest"
    );
}

/// Modify one file after first ingest, re-ingest → new content reflected in search.
#[tokio::test]
async fn changed_file_re_ingest_reflected_in_results() {
    let base = make_tempdir("changed");
    let files = seed_fixture_files(&base);
    let db_path = base.join("index.db");
    let embed = Arc::new(MockEmbedModel::new(1024));

    // First ingest
    {
        let conn = Connection::open(&db_path).expect("db");
        run_migrations(&conn).expect("migrations");
        let pipeline = IngestPipeline::new(conn, Arc::clone(&embed) as _, IngestConfig::default());
        pipeline.ingest_files(files.iter().map(|p| p.as_path()));
    }

    // Search before modification — should NOT find "qibla direction"
    {
        let conn_s = open_file_db(&db_path);
        let embed_arc: Arc<dyn cascade_rag::embed::EmbedModel> =
            Arc::new(MockEmbedModel::new(1024));
        let cfg = SearchConfig {
            k: 5,
            fts5_enabled: true,
            ..Default::default()
        };
        let before = search("qibla direction", &cfg, conn_s, embed_arc, None, None)
            .await
            .unwrap();
        let has_qibla = before
            .iter()
            .any(|r| r.snippet.to_lowercase().contains("qibla"));
        // Before modification, no content mentions qibla.
        assert!(!has_qibla, "should not find 'qibla' before we add it");
    }

    // Modify config.json to contain a new unique term.
    let config_path = &files[2]; // config.json
    let new_content = r#"{"pipeline":{"fts5_enabled":true},"qibla_direction":"great_circle_calculation","updated":true}"#;
    fs::write(config_path, new_content).expect("modify config.json");

    // Re-ingest (only the modified file will be re-indexed).
    {
        let conn = Connection::open(&db_path).expect("db");
        run_migrations(&conn).expect("migrations");
        let pipeline = IngestPipeline::new(conn, Arc::clone(&embed) as _, IngestConfig::default());
        let (results, errors) = pipeline.ingest_files(std::iter::once(config_path.as_path()));
        assert!(errors.is_empty());
        assert_eq!(results.len(), 1);
        assert!(!results[0].skipped, "modified file must not be skipped");
    }

    // Search after modification — should now find "qibla direction".
    {
        let conn_s = open_file_db(&db_path);
        let embed_arc: Arc<dyn cascade_rag::embed::EmbedModel> =
            Arc::new(MockEmbedModel::new(1024));
        let cfg = SearchConfig {
            k: 5,
            fts5_enabled: true,
            ..Default::default()
        };
        let after = search("qibla direction", &cfg, conn_s, embed_arc, None, None)
            .await
            .unwrap();
        assert!(
            !after.is_empty(),
            "after modification, 'qibla direction' should be findable in config.json"
        );
    }
}

/// FTS-only vs hybrid (vec_enabled=true) both return results for the same query;
/// results overlap (at least 1 shared citation path).
#[tokio::test]
async fn fts_vs_hybrid_parity() {
    let base = make_tempdir("parity");
    let files = seed_fixture_files(&base);
    let db_path = base.join("index.db");
    let embed = Arc::new(MockEmbedModel::new(1024));

    // Ingest
    {
        let conn = Connection::open(&db_path).expect("db");
        run_migrations(&conn).expect("migrations");
        let pipeline = IngestPipeline::new(conn, Arc::clone(&embed) as _, IngestConfig::default());
        pipeline.ingest_files(files.iter().map(|p| p.as_path()));
    }

    // Single term — FTS5 sanitiser wraps in phrase quotes, single word always matches.
    let query = "retrieval";

    // FTS-only
    let fts_results = {
        let conn_s = open_file_db(&db_path);
        let embed_arc: Arc<dyn cascade_rag::embed::EmbedModel> =
            Arc::new(MockEmbedModel::new(1024));
        let cfg = SearchConfig {
            k: 5,
            fts5_enabled: true,
            vec_enabled: false,
            rerank_enabled: false,
            ..Default::default()
        };
        search(query, &cfg, conn_s, embed_arc, None, None).await.unwrap()
    };

    // Hybrid (FTS5 + dense vectors via MockEmbedModel cosine path)
    let hybrid_results = {
        let conn_s = open_file_db(&db_path);
        let embed_arc: Arc<dyn cascade_rag::embed::EmbedModel> =
            Arc::new(MockEmbedModel::new(1024));
        let cfg = SearchConfig {
            k: 5,
            fts5_enabled: true,
            vec_enabled: true, // triggers brute-force cosine with MockEmbedModel
            rerank_enabled: false,
            ..Default::default()
        };
        search(query, &cfg, conn_s, embed_arc, None, None).await.unwrap()
    };

    // Both should produce results for the seeded content.
    assert!(
        !fts_results.is_empty(),
        "FTS-only must return results for '{query}'"
    );
    assert!(
        !hybrid_results.is_empty(),
        "hybrid must return results for '{query}'"
    );

    // At least 1 path appears in both result sets.
    let fts_paths: std::collections::HashSet<&PathBuf> =
        fts_results.iter().map(|r| &r.source_path).collect();
    let hybrid_paths: std::collections::HashSet<&PathBuf> =
        hybrid_results.iter().map(|r| &r.source_path).collect();
    let overlap: usize = fts_paths.intersection(&hybrid_paths).count();
    assert!(
        overlap >= 1,
        "FTS and hybrid should share at least 1 result path; fts={:?} hybrid={:?}",
        fts_paths,
        hybrid_paths
    );
}

/// rerank_enabled=true with NoopReranker injected → citations have reranker_score set
/// and ordering is stable (reranker_score descending).
#[tokio::test]
async fn rerank_seam_exercised() {
    let base = make_tempdir("rerank");
    let files = seed_fixture_files(&base);
    let db_path = base.join("index.db");
    let embed = Arc::new(MockEmbedModel::new(1024));

    // Ingest
    {
        let conn = Connection::open(&db_path).expect("db");
        run_migrations(&conn).expect("migrations");
        let pipeline = IngestPipeline::new(conn, Arc::clone(&embed) as _, IngestConfig::default());
        pipeline.ingest_files(files.iter().map(|p| p.as_path()));
    }

    let conn_s = open_file_db(&db_path);
    let embed_arc: Arc<dyn cascade_rag::embed::EmbedModel> = Arc::new(MockEmbedModel::new(1024));
    let rr: Arc<dyn Reranker> = Arc::new(NoopReranker);

    let cfg = SearchConfig {
        k: 5,
        fts5_enabled: true,
        vec_enabled: false,
        rerank_enabled: true,
        ..Default::default()
    };

    let results = search("retrieval", &cfg, conn_s, embed_arc, Some(rr), None)
        .await
        .expect("rerank path must not error");

    assert!(
        !results.is_empty(),
        "rerank path must return results for seeded content"
    );

    // All citations must have reranker_score populated when reranker was injected.
    for r in &results {
        assert!(
            r.reranker_score.is_some(),
            "reranker_score must be Some when NoopReranker is injected; chunk_id={}",
            r.chunk_id
        );
    }

    // Results must be ordered by descending reranker_score.
    let scores: Vec<f64> = results.iter().filter_map(|r| r.reranker_score).collect();
    for pair in scores.windows(2) {
        assert!(
            pair[0] >= pair[1],
            "reranker scores must be non-increasing: {} < {}",
            pair[0],
            pair[1]
        );
    }
}

/// 5 synthetic FTS5 queries against 100 manually-inserted chunks.
/// Verifies each query finds its target chunk in top-3.
/// This is the canonical "fast path" exercised in CI.
#[tokio::test]
async fn synthetic_100_chunks_five_queries() {
    // ── Build in-memory DB with 100 synthetic chunks ──────────────────────────
    #[cfg(feature = "vec")]
    load_vec_extension();
    let conn = Connection::open_in_memory().expect("in-memory db");
    run_migrations(&conn).expect("migrations");

    conn.execute(
        "INSERT INTO rag_sources (file_path, content_hash, schema_version) VALUES (?1, ?2, 1)",
        rusqlite::params!["/synthetic/file.md", "synthetic_hash_v1"],
    )
    .expect("insert source");
    let source_id = conn.last_insert_rowid();

    // 100 synthetic chunks. Each chunk_index maps to a unique keyword set.
    // 5 "golden" chunks are seeded at specific indices with easily-retrievable terms.
    struct GoldenQuery {
        term: &'static str,
        chunk_index: usize,
    }
    let golden: Vec<GoldenQuery> = vec![
        GoldenQuery {
            term: "hijri calendar month calculation",
            chunk_index: 3,
        },
        GoldenQuery {
            term: "prayer times solar declination angle",
            chunk_index: 17,
        },
        GoldenQuery {
            term: "moon sighting ephemeris JPL",
            chunk_index: 31,
        },
        GoldenQuery {
            term: "qibla direction great circle bearing",
            chunk_index: 55,
        },
        GoldenQuery {
            term: "RRF score fusion FTS5 dense vectors",
            chunk_index: 82,
        },
    ];

    for i in 0..100usize {
        // For golden chunks, embed their term directly in the text.
        let text = if let Some(g) = golden.iter().find(|g| g.chunk_index == i) {
            format!(
                "Chunk {i}: This chunk covers {}. \
                 It is indexed for semantic retrieval tests.",
                g.term
            )
        } else {
            format!(
                "Chunk {i}: General content about software engineering, \
                 code quality, and testing practices. Index: {i}."
            )
        };

        conn.execute(
            "INSERT INTO rag_chunks \
             (source_id, chunk_index, chunk_text, line_start, line_end, schema_version) \
             VALUES (?1, ?2, ?3, ?4, ?5, 1)",
            rusqlite::params![
                source_id,
                i as i64,
                text,
                (i * 5 + 1) as i64,
                (i * 5 + 5) as i64,
            ],
        )
        .expect("insert chunk");
    }

    let conn_arc = Arc::new(Mutex::new(conn));
    let embed_arc: Arc<dyn cascade_rag::embed::EmbedModel> = Arc::new(MockEmbedModel::new(1024));

    // ── Run 5 golden queries, each must appear in top-3 ──────────────────────
    for g in &golden {
        let cfg = SearchConfig {
            k: 10,
            fts5_enabled: true,
            vec_enabled: false,
            rerank_enabled: false,
            ..Default::default()
        };

        // Extract the first word as FTS query (guaranteed to be in the chunk text).
        let fts_term = g.term.split_whitespace().next().expect("non-empty term");

        let results = search(
            fts_term,
            &cfg,
            Arc::clone(&conn_arc),
            Arc::clone(&embed_arc),
            None,
            None,
        )
        .await
        .expect("search should not fail");

        assert!(
            !results.is_empty(),
            "FTS query for '{}' must return at least one result",
            fts_term
        );

        // The golden chunk's line_start is (chunk_index * 5 + 1).
        let golden_line_start = g.chunk_index * 5 + 1;
        let top3_has_golden = results
            .iter()
            .take(3)
            .any(|r| r.line_start == golden_line_start);

        assert!(
            top3_has_golden,
            "golden chunk for '{}' (line_start={}) must appear in top-3; got: {:?}",
            fts_term,
            golden_line_start,
            results
                .iter()
                .take(3)
                .map(|r| r.line_start)
                .collect::<Vec<_>>()
        );
    }
}

// ── Slow test (model download required — excluded from CI) ────────────────────

/// Full pipeline against 50 synthetic files. Checks MRR ≥ 0.60 (FTS5-only)
/// and MRR ≥ 0.65 (hybrid with MockEmbedModel standing in for dense vectors).
///
/// Expected thresholds:
/// - FTS5-only MRR@10: ≥ 0.60
/// - Hybrid (FTS5 + dense cosine) MRR@10: ≥ 0.65
///
/// Run with: `cargo test -p cascade-rag -- slow_full_50_files --include-ignored`
#[tokio::test]
#[ignore = "slow: 50 files + 20 query eval; run manually with --include-ignored"]
async fn slow_full_50_files() {
    let base = make_tempdir("slow50");
    let db_path = base.join("index.db");
    let embed = Arc::new(MockEmbedModel::new(1024));

    // Seed 50 files with domain-varied content.
    let mut file_paths = Vec::new();
    let topics = [
        (
            "hijri_calendar",
            "Hijri calendar month calculation umm al-qura",
        ),
        (
            "prayer_times",
            "Prayer times solar declination angle fajr dhuhr asr",
        ),
        (
            "moon_sighting",
            "Moon sighting ephemeris JPL DE442S new crescent",
        ),
        (
            "qibla",
            "Qibla direction great circle bearing true north kaaba",
        ),
        (
            "rrf_pipeline",
            "RRF score fusion FTS5 dense vectors retrieval",
        ),
        (
            "cascade_tiers",
            "Instruction cascade GCI ASI PPI PRI PAI tiers",
        ),
        (
            "delegation",
            "Multi-agent delegation subagent spawn parallel",
        ),
        ("phase_build", "Phase based development PBD PEWS plan build"),
        (
            "typescript_strict",
            "TypeScript strict mode typed boundaries DRY",
        ),
        ("pnpm_packages", "pnpm workspace package manager dependency"),
    ];

    for i in 0..50usize {
        let (slug, content_seed) = &topics[i % topics.len()];
        let fname = base.join(format!("{slug}_{i:03}.md"));
        fs::write(
            &fname,
            format!(
                "# {slug} Document {i}\n\n\
                 {content_seed}.\n\n\
                 This is document number {i} in the synthetic test corpus.\n\
                 It covers the topic of {slug} with variations.\n\
                 Content ID: unique_marker_{i}\n"
            ),
        )
        .expect("write file");
        file_paths.push(fname);
    }

    // Ingest all 50 files.
    {
        let conn = Connection::open(&db_path).expect("db");
        run_migrations(&conn).expect("migrations");
        let pipeline = IngestPipeline::new(conn, Arc::clone(&embed) as _, IngestConfig::default());
        let (results, errors) = pipeline.ingest_files(file_paths.iter().map(|p| p.as_path()));
        assert!(errors.is_empty(), "50-file ingest should have no errors");
        assert_eq!(results.len(), 50);
    }

    // 20 golden queries: each targets a known topic.
    let golden_queries: Vec<(&str, &str)> = vec![
        ("hijri calendar month", "hijri_calendar"),
        ("prayer times solar", "prayer_times"),
        ("moon sighting ephemeris", "moon_sighting"),
        ("qibla direction bearing", "qibla"),
        ("rrf score fusion", "rrf_pipeline"),
        ("instruction cascade tiers", "cascade_tiers"),
        ("multi agent delegation", "delegation"),
        ("phase build pews", "phase_build"),
        ("typescript strict typed", "typescript_strict"),
        ("pnpm workspace dependency", "pnpm_packages"),
        ("unique_marker_0", "hijri_calendar"),
        ("unique_marker_10", "rrf_pipeline"),
        ("unique_marker_20", "typescript_strict"),
        ("unique_marker_30", "moon_sighting"),
        ("unique_marker_40", "typescript_strict"),
        ("hijri umm al qura", "hijri_calendar"),
        ("fajr dhuhr asr salah", "prayer_times"),
        ("crescent new moon", "moon_sighting"),
        ("kaaba true north", "qibla"),
        ("delegation subagent spawn", "delegation"),
    ];

    // Compute MRR@10 for FTS5-only.
    let mut fts_reciprocal_ranks: Vec<f64> = Vec::new();
    for (query, expected_slug) in &golden_queries {
        let conn_s = open_file_db(&db_path);
        let ea: Arc<dyn cascade_rag::embed::EmbedModel> = Arc::new(MockEmbedModel::new(1024));
        let cfg = SearchConfig {
            k: 10,
            fts5_enabled: true,
            vec_enabled: false,
            rerank_enabled: false,
            ..Default::default()
        };
        let results = search(query, &cfg, conn_s, ea, None, None)
            .await
            .unwrap_or_default();
        let rr = results
            .iter()
            .enumerate()
            .find(|(_, r)| {
                r.source_path
                    .to_str()
                    .map(|s| s.contains(expected_slug))
                    .unwrap_or(false)
            })
            .map(|(rank, _)| 1.0 / (rank as f64 + 1.0))
            .unwrap_or(0.0);
        fts_reciprocal_ranks.push(rr);
    }

    let fts_mrr: f64 = fts_reciprocal_ranks.iter().sum::<f64>() / fts_reciprocal_ranks.len() as f64;
    assert!(
        fts_mrr >= 0.60,
        "FTS5-only MRR@10 must be >= 0.60; got {:.3}",
        fts_mrr
    );

    // Compute MRR@10 for hybrid (FTS5 + MockEmbedModel cosine).
    let mut hybrid_reciprocal_ranks: Vec<f64> = Vec::new();
    for (query, expected_slug) in &golden_queries {
        let conn_s = open_file_db(&db_path);
        let ea: Arc<dyn cascade_rag::embed::EmbedModel> = Arc::new(MockEmbedModel::new(1024));
        let cfg = SearchConfig {
            k: 10,
            fts5_enabled: true,
            vec_enabled: true,
            rerank_enabled: false,
            ..Default::default()
        };
        let results = search(query, &cfg, conn_s, ea, None, None)
            .await
            .unwrap_or_default();
        let rr = results
            .iter()
            .enumerate()
            .find(|(_, r)| {
                r.source_path
                    .to_str()
                    .map(|s| s.contains(expected_slug))
                    .unwrap_or(false)
            })
            .map(|(rank, _)| 1.0 / (rank as f64 + 1.0))
            .unwrap_or(0.0);
        hybrid_reciprocal_ranks.push(rr);
    }

    let hybrid_mrr: f64 =
        hybrid_reciprocal_ranks.iter().sum::<f64>() / hybrid_reciprocal_ranks.len() as f64;
    assert!(
        hybrid_mrr >= 0.65,
        "hybrid MRR@10 must be >= 0.65; got {:.3}",
        hybrid_mrr
    );
}
