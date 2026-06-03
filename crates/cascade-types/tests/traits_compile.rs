//! Compile-time trait object safety tests.
//!
//! Each test in this file ensures that the corresponding trait is object-safe
//! (i.e. can be used as `dyn Trait`) and that the no-op implementations compile
//! correctly. These tests contain minimal runtime assertions; the primary
//! purpose is to catch object-safety regressions during `cargo test`.

use cascade_types::{
    agent::{Agent, Context, NoopAgent, Tier},
    chunker::{Chunk, ChunkOpts, Chunker, Document, NoopChunker},
    embedding_provider::{EmbedOpts, EmbeddingProvider, NoopEmbeddingProvider},
    key_storage::{InMemoryKeyStorage, KeyStorage, SecretBytes},
    parser::ParserKind,
    query_strategy::{PureVectorStrategy, QueryStrategy, StrategyKind},
    reranker::{NoopReranker, RerankOpts, Reranker},
    retriever::{NoopRetriever, RetrieveOpts, Retriever},
    CascadeTier, InboxTier,
};
use std::sync::Arc;

// ── Object-safety: each trait must be boxable ─────────────────────────────────

#[test]
fn chunker_is_object_safe() {
    let _: Box<dyn Chunker> = Box::new(NoopChunker);
}

#[test]
fn retriever_is_object_safe() {
    let _: Box<dyn Retriever> = Box::new(NoopRetriever);
}

#[test]
fn embedding_provider_is_object_safe() {
    let _: Box<dyn EmbeddingProvider> = Box::new(NoopEmbeddingProvider);
}

#[test]
fn reranker_is_object_safe() {
    let _: Box<dyn Reranker> = Box::new(NoopReranker);
}

#[test]
fn query_strategy_is_object_safe() {
    let _: Box<dyn QueryStrategy> = Box::new(PureVectorStrategy);
}

#[test]
fn agent_is_object_safe() {
    let _: Box<dyn Agent> = Box::new(NoopAgent);
}

#[test]
fn key_storage_is_object_safe() {
    let _: Box<dyn KeyStorage> = Box::new(InMemoryKeyStorage::new());
}

// Parser trait requires an associated method that accepts `&Path`, which is
// object-safe (no generics on the methods).
// PlainTextParser is the test impl.
#[test]
fn parser_kind_enum_exhaustive() {
    // Ensure all expected variants are present and match is exhaustive.
    let kinds = [
        ParserKind::Markdown,
        ParserKind::Code,
        ParserKind::Pdf,
        ParserKind::Html,
        ParserKind::Yaml,
        ParserKind::Json,
        ParserKind::Toml,
        ParserKind::Xml,
        ParserKind::Jupyter,
        ParserKind::PlainText,
    ];
    for kind in &kinds {
        assert!(
            !kind.extensions().is_empty(),
            "{kind:?} must have at least one extension"
        );
    }
}

// ── Arc<dyn Trait>: shareable across threads ──────────────────────────────────

#[test]
fn traits_are_send_sync() {
    let _: Arc<dyn Chunker> = Arc::new(NoopChunker);
    let _: Arc<dyn Retriever> = Arc::new(NoopRetriever);
    let _: Arc<dyn EmbeddingProvider> = Arc::new(NoopEmbeddingProvider);
    let _: Arc<dyn Reranker> = Arc::new(NoopReranker);
    let _: Arc<dyn QueryStrategy> = Arc::new(PureVectorStrategy);
    let _: Arc<dyn Agent> = Arc::new(NoopAgent);
    let _: Arc<dyn KeyStorage> = Arc::new(InMemoryKeyStorage::new());
}

// ── Noop runtime smoke tests ──────────────────────────────────────────────────

#[tokio::test]
async fn noop_chunker_single_chunk() {
    let chunker = NoopChunker;
    let doc = Document::from_text("hello world");
    let chunks = chunker.chunk(&doc, &ChunkOpts::default()).await.unwrap();
    assert_eq!(chunks.len(), 1);
    assert_eq!(chunks[0].text, "hello world");
    assert_eq!(chunks[0].metadata.chunk_index, 0);
}

#[tokio::test]
async fn noop_chunker_empty_document() {
    let chunker = NoopChunker;
    let doc = Document::from_text("");
    let chunks = chunker.chunk(&doc, &ChunkOpts::default()).await.unwrap();
    assert!(chunks.is_empty());
}

#[tokio::test]
async fn noop_retriever_returns_empty() {
    let retriever = NoopRetriever;
    let hits = retriever
        .retrieve("any query", &RetrieveOpts::default())
        .await
        .unwrap();
    assert!(hits.is_empty());
}

#[tokio::test]
async fn noop_embedding_provider_returns_zero_vectors() {
    let provider = NoopEmbeddingProvider;
    let embeddings = provider
        .embed(&["hello", "world"], &EmbedOpts::default())
        .await
        .unwrap();
    assert_eq!(embeddings.len(), 2);
    assert!(embeddings[0].values.iter().all(|&v| v == 0.0));
}

#[tokio::test]
async fn noop_reranker_preserves_count() {
    use cascade_types::chunker::ChunkMetadata;
    use std::collections::HashMap;

    let make_chunk = |i: usize| Chunk {
        id: format!("chunk-{i}"),
        text: format!("text {i}"),
        metadata: ChunkMetadata {
            start_byte: i * 10,
            end_byte: i * 10 + 10,
            source_path: None,
            start_line: None,
            end_line: None,
            chunk_index: i,
            total_chunks: 3,
            extra: HashMap::new(),
        },
    };

    let candidates: Vec<Chunk> = (0..3).map(make_chunk).collect();
    let reranker = NoopReranker;
    let results = reranker
        .rerank("query", &candidates, &RerankOpts::default())
        .await
        .unwrap();
    // Default top_k=5 but only 3 candidates, so expect 3.
    assert_eq!(results.len(), 3);
    // Scores should be descending.
    let scores: Vec<f32> = results.iter().map(|r| r.score).collect();
    let mut sorted = scores.clone();
    sorted.sort_by(|a, b| b.partial_cmp(a).unwrap());
    assert_eq!(scores, sorted);
}

#[tokio::test]
async fn pure_vector_strategy_passthrough() {
    let strategy = PureVectorStrategy;
    let expanded = strategy.expand("find me something").await.unwrap();
    assert_eq!(expanded.queries, vec!["find me something"]);
    assert_eq!(expanded.strategy, StrategyKind::PureVector);
}

#[tokio::test]
async fn noop_agent_echoes_prompt() {
    use std::collections::HashMap;

    let agent = NoopAgent;
    let ctx = Context {
        cascade_text: String::new(),
        retrieval_hits: vec![],
        tier: Tier::T3,
        invocation_id: "test-001".into(),
        session: HashMap::new(),
        cwd: std::env::current_dir().unwrap(),
    };
    let response = agent.handle("hello agent", &ctx).await.unwrap();
    assert!(response.text.contains("hello agent"));
    assert!(!response.needs_continuation);
}

#[tokio::test]
async fn in_memory_key_storage_round_trip() {
    let store = InMemoryKeyStorage::new();
    store
        .put("test-key", SecretBytes::from_utf8_str("secret-value"))
        .await
        .unwrap();
    let retrieved = store.get("test-key").await.unwrap();
    assert_eq!(retrieved.as_str().unwrap(), "secret-value");

    store.delete("test-key").await.unwrap();
    let result = store.get("test-key").await;
    assert!(result.is_err(), "key should be gone after delete");
}

// ── CascadeTier and InboxTier ─────────────────────────────────────────────────

#[test]
fn cascade_tier_roundtrip() {
    use std::str::FromStr;
    for tier in CascadeTier::all_in_precedence_order() {
        let s = tier.to_string();
        let parsed = CascadeTier::from_str(&s).unwrap();
        assert_eq!(&parsed, tier);
    }
}

#[test]
fn cascade_tier_precedence_is_stable() {
    // GCI must be the "smallest" (highest precedence) in Ord.
    assert!(CascadeTier::Gci < CascadeTier::Pac);
}

#[test]
fn inbox_tier_display() {
    assert_eq!(InboxTier::Ppi.to_string(), "ppi");
    assert_eq!(InboxTier::Pri.to_string(), "pri");
    assert_eq!(InboxTier::Pai.to_string(), "pai");
}

// ── Paths module ──────────────────────────────────────────────────────────────

#[test]
fn global_cascade_dir_ends_with_dot_cascade() {
    let dir = cascade_types::global_cascade_dir();
    assert_eq!(dir.file_name().unwrap(), ".cascade");
}

#[test]
fn parser_from_path_markdown() {
    use cascade_types::parser::ParserKind;
    let kind = ParserKind::from_path(std::path::Path::new("readme.md")).unwrap();
    assert_eq!(kind, ParserKind::Markdown);
}

#[test]
fn parser_from_path_rust() {
    use cascade_types::parser::ParserKind;
    let kind = ParserKind::from_path(std::path::Path::new("main.rs")).unwrap();
    assert_eq!(kind, ParserKind::Code);
}

#[test]
fn parser_from_path_unknown_returns_none() {
    use cascade_types::parser::ParserKind;
    assert!(ParserKind::from_path(std::path::Path::new("file.xyz")).is_none());
}
