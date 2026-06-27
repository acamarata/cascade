//! EmbeddingProvider trait and associated types.
//!
//! An `EmbeddingProvider` converts text strings into fixed-dimensional
//! floating-point vectors. Implementations wrap specific model APIs or
//! local inference runtimes; the trait is provider-agnostic.

use crate::error::Result;
use async_trait::async_trait;
use serde::{Deserialize, Serialize};

// ── ProviderKind ──────────────────────────────────────────────────────────────

/// The set of embedding models that Cascade supports out of the box.
///
/// Adding a new provider is a minor version bump; match arms should include
/// a wildcard (`_`) or use `#[non_exhaustive]` handling.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
#[non_exhaustive]
pub enum ProviderKind {
    /// BGE-M3 (BAAI) — multilingual, supports dense + sparse + ColBERT.
    BgeM3,

    /// Nomic Embed Text — open-source, 768-dim.
    Nomic,

    /// Jina Embeddings v3 — long-context, 1024-dim.
    Jina,

    /// E5-Mistral-7B-Instruct — instruction-tuned, 4096-dim.
    E5Mistral,

    /// OpenAI text-embedding-3-small / text-embedding-3-large.
    OpenAi,

    /// Cohere embed-v3.
    Cohere,

    /// Voyage AI embed-3.
    Voyage,

    /// Google Gemini embedding API.
    Gemini,

    /// A locally-hosted ONNX model via the Cascade plugin system.
    LocalOnnx,
}

impl ProviderKind {
    /// Returns the canonical string identifier used in config files.
    pub fn config_key(&self) -> &'static str {
        match self {
            Self::BgeM3 => "bge-m3",
            Self::Nomic => "nomic",
            Self::Jina => "jina",
            Self::E5Mistral => "e5-mistral",
            Self::OpenAi => "openai",
            Self::Cohere => "cohere",
            Self::Voyage => "voyage",
            Self::Gemini => "gemini",
            Self::LocalOnnx => "local-onnx",
        }
    }

    /// Default embedding dimension for this provider's primary model.
    ///
    /// Returns `None` for providers that vary by model variant.
    pub fn default_dimension(&self) -> Option<usize> {
        match self {
            Self::BgeM3 => Some(1024),
            Self::Nomic => Some(768),
            Self::Jina => Some(1024),
            Self::E5Mistral => Some(4096),
            Self::OpenAi => None, // 1536 (small) or 3072 (large) depending on config
            Self::Cohere => Some(1024),
            Self::Voyage => Some(1024),
            Self::Gemini => Some(768),
            Self::LocalOnnx => None, // depends on the model file
        }
    }
}

impl std::fmt::Display for ProviderKind {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.config_key())
    }
}

// ── Embedding ─────────────────────────────────────────────────────────────────

/// A single embedding vector.
///
/// The length always equals the provider's declared dimension.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Embedding {
    /// The dense vector values.
    pub values: Vec<f32>,

    /// Token count of the input that produced this embedding.
    /// `None` if the provider does not report token usage.
    pub token_count: Option<usize>,
}

impl Embedding {
    /// Returns the dimensionality of this embedding.
    pub fn dim(&self) -> usize {
        self.values.len()
    }

    /// Compute the cosine similarity with another embedding of the same dimension.
    ///
    /// Returns a value in `[-1.0, 1.0]`. Returns `0.0` if either vector is zero.
    pub fn cosine_similarity(&self, other: &Embedding) -> f32 {
        debug_assert_eq!(
            self.dim(),
            other.dim(),
            "dimension mismatch in cosine_similarity"
        );
        let dot: f32 = self
            .values
            .iter()
            .zip(&other.values)
            .map(|(a, b)| a * b)
            .sum();
        let norm_a: f32 = self.values.iter().map(|x| x * x).sum::<f32>().sqrt();
        let norm_b: f32 = other.values.iter().map(|x| x * x).sum::<f32>().sqrt();
        if norm_a == 0.0 || norm_b == 0.0 {
            0.0
        } else {
            dot / (norm_a * norm_b)
        }
    }
}

// ── EmbedOpts ────────────────────────────────────────────────────────────────

/// Options for an embedding request.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct EmbedOpts {
    /// Hint about how the embeddings will be used. Some models return
    /// different vectors for queries vs. documents.
    pub usage: EmbedUsage,

    /// Optional model override (e.g. `"text-embedding-3-large"`).
    /// When `None`, the provider uses its default model.
    pub model_override: Option<String>,

    /// Matryoshka truncation: truncate the output vector to this many dimensions
    /// and L2-renormalise after inference.  `None` = use the model's full dimension.
    ///
    /// Only supported by providers that produce Matryoshka-compatible embeddings
    /// (e.g. MultilingualE5Large, OpenAI text-embedding-3-*).  Other providers
    /// ignore this field.  Always produces a unit-norm vector.
    #[serde(default)]
    pub truncate_dim: Option<usize>,
}

/// Whether the text being embedded is a search query or a document passage.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum EmbedUsage {
    /// The text will be used as a query vector.
    Query,
    /// The text will be stored as a document vector.
    Document,
}

impl Default for EmbedOpts {
    fn default() -> Self {
        Self {
            usage: EmbedUsage::Document,
            model_override: None,
            truncate_dim: None,
        }
    }
}

// ── Trait ─────────────────────────────────────────────────────────────────────

/// Converts a batch of text strings into embedding vectors.
///
/// The trait is intentionally batch-oriented: providers with HTTP APIs are
/// much more efficient when batching, and callers should always prefer batching
/// over calling `embed` once per string.
///
/// # Example
///
/// ```rust,no_run
/// # use cascade_types::embedding_provider::{EmbeddingProvider, EmbedOpts, Embedding};
/// # use cascade_types::error::Result;
/// # use async_trait::async_trait;
/// struct LocalEmbedder;
///
/// #[async_trait]
/// impl EmbeddingProvider for LocalEmbedder {
///     async fn embed(&self, texts: &[&str], _opts: &EmbedOpts) -> Result<Vec<Embedding>> {
///         // call local inference runtime …
///         Ok(vec![])
///     }
///     fn dimension(&self) -> usize { 768 }
/// }
/// ```
#[async_trait]
pub trait EmbeddingProvider: Send + Sync {
    /// Embed a batch of texts.
    ///
    /// Returns one `Embedding` per input text, in the same order.
    ///
    /// # Errors
    ///
    /// Returns [`CascadeError::EmbeddingFailed`] if the provider returns an
    /// error, or [`CascadeError::EmbeddingDimensionMismatch`] if the returned
    /// vectors have the wrong dimension.
    async fn embed(&self, texts: &[&str], opts: &EmbedOpts) -> Result<Vec<Embedding>>;

    /// The output vector dimension for this provider + model combination.
    fn dimension(&self) -> usize;

    /// The provider kind (used for config and logging).
    fn kind(&self) -> ProviderKind {
        ProviderKind::LocalOnnx
    }
}

// ── No-op implementation (for tests) ─────────────────────────────────────────

/// Returns zero vectors of dimension 4. For unit tests only.
#[derive(Debug, Default)]
pub struct NoopEmbeddingProvider;

#[async_trait]
impl EmbeddingProvider for NoopEmbeddingProvider {
    async fn embed(&self, texts: &[&str], _opts: &EmbedOpts) -> Result<Vec<Embedding>> {
        Ok(texts
            .iter()
            .map(|_| Embedding {
                values: vec![0.0; 4],
                token_count: None,
            })
            .collect())
    }

    fn dimension(&self) -> usize {
        4
    }
}

fn _assert_object_safe(_: &dyn EmbeddingProvider) {}
