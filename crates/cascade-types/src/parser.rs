//! Parser trait and associated types.
//!
//! A `Parser` converts a raw file on disk (or in memory) into a [`crate::chunker::Document`]
//! ready for chunking. Each `ParserKind` corresponds to a file format; the
//! `Parser` trait is the common interface regardless of format.

use std::path::Path;
use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use crate::chunker::Document;
use crate::error::{CascadeError, Result};

// ── ParserKind ────────────────────────────────────────────────────────────────

/// The set of file formats that Cascade can parse.
///
/// Additional variants may be added in minor versions; match arms should
/// include a wildcard to avoid compile errors on upgrade.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
#[non_exhaustive]
pub enum ParserKind {
    /// Markdown (`.md`, `.mdx`).
    Markdown,

    /// Source code in any supported language.
    /// The language is inferred from the file extension or an explicit hint.
    Code,

    /// PDF documents (requires an optional `pdf` feature).
    Pdf,

    /// HTML/XHTML.
    Html,

    /// YAML configuration files (`.yaml`, `.yml`).
    Yaml,

    /// JSON data files.
    Json,

    /// TOML configuration files.
    Toml,

    /// XML documents.
    Xml,

    /// Jupyter notebooks (`.ipynb`).
    Jupyter,

    /// Plain text with no special structure.
    PlainText,
}

impl ParserKind {
    /// Returns the canonical file extensions associated with this format.
    pub fn extensions(&self) -> &'static [&'static str] {
        match self {
            Self::Markdown => &["md", "mdx"],
            Self::Code => &[
                "rs", "py", "ts", "tsx", "js", "jsx", "go", "java", "c", "cpp", "h",
                "hpp", "cs", "rb", "swift", "kt", "dart", "sh", "bash",
            ],
            Self::Pdf => &["pdf"],
            Self::Html => &["html", "htm", "xhtml"],
            Self::Yaml => &["yaml", "yml"],
            Self::Json => &["json", "jsonc"],
            Self::Toml => &["toml"],
            Self::Xml => &["xml"],
            Self::Jupyter => &["ipynb"],
            Self::PlainText => &["txt", "text"],
        }
    }

    /// Infer the parser kind from a file path's extension.
    ///
    /// Returns `None` if the extension is not recognised.
    pub fn from_path(path: &Path) -> Option<Self> {
        let ext = path.extension()?.to_str()?.to_lowercase();
        let ext = ext.as_str();
        [
            Self::Markdown,
            Self::Code,
            Self::Pdf,
            Self::Html,
            Self::Yaml,
            Self::Json,
            Self::Toml,
            Self::Xml,
            Self::Jupyter,
            Self::PlainText,
        ]
        .into_iter()
        .find(|kind| kind.extensions().contains(&ext))
    }
}

impl std::fmt::Display for ParserKind {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let s = match self {
            Self::Markdown => "markdown",
            Self::Code => "code",
            Self::Pdf => "pdf",
            Self::Html => "html",
            Self::Yaml => "yaml",
            Self::Json => "json",
            Self::Toml => "toml",
            Self::Xml => "xml",
            Self::Jupyter => "jupyter",
            Self::PlainText => "plain-text",
        };
        f.write_str(s)
    }
}

// ── Trait ─────────────────────────────────────────────────────────────────────

/// Converts a file (or in-memory bytes) into a [`Document`].
///
/// Parsers are typically stateless. The I/O is async so large files (PDFs,
/// notebooks) can be read without blocking the executor.
///
/// # Example
///
/// ```rust,no_run
/// # use cascade_types::parser::{Parser, ParserKind};
/// # use cascade_types::chunker::Document;
/// # use cascade_types::error::Result;
/// # use async_trait::async_trait;
/// # use std::path::Path;
/// struct MarkdownParser;
///
/// #[async_trait]
/// impl Parser for MarkdownParser {
///     fn kind(&self) -> ParserKind { ParserKind::Markdown }
///
///     async fn parse(&self, path: &Path) -> Result<Document> {
///         let content = tokio::fs::read_to_string(path).await
///             .map_err(|e| cascade_types::error::CascadeError::io(path, "read", e))?;
///         Ok(Document::from_text(content))
///     }
/// }
/// ```
#[async_trait]
pub trait Parser: Send + Sync {
    /// The file format this parser handles.
    fn kind(&self) -> ParserKind;

    /// Parse the file at `path` into a `Document`.
    ///
    /// The parser is responsible for opening and reading the file.
    async fn parse(&self, path: &Path) -> Result<Document>;

    /// Parse from an in-memory byte slice instead of a file path.
    ///
    /// The default implementation writes the bytes to a temporary file and
    /// calls [`Self::parse`]. Override for formats where in-memory parsing
    /// is significantly more efficient.
    async fn parse_bytes(&self, bytes: &[u8], hint_path: Option<&Path>) -> Result<Document> {
        // Write to a temp file so the default impl can reuse `parse`.
        let tmp = tempfile_path(hint_path);
        std::fs::write(&tmp, bytes).map_err(|e| CascadeError::io(&tmp, "write-tmp", e))?;
        let doc = self.parse(&tmp).await;
        let _ = std::fs::remove_file(&tmp);
        doc
    }
}

/// Returns a temporary file path, optionally preserving the original extension
/// for format-aware parsers.
fn tempfile_path(hint: Option<&Path>) -> std::path::PathBuf {
    let ext = hint
        .and_then(|p| p.extension())
        .and_then(|e| e.to_str())
        .unwrap_or("tmp");
    std::env::temp_dir().join(format!("cascade-parse-{}.{}", uuid_short(), ext))
}

fn uuid_short() -> String {
    use std::time::{SystemTime, UNIX_EPOCH};
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.subsec_nanos())
        .unwrap_or(0);
    format!("{:08x}", nanos)
}

// ── No-op implementation (for tests) ─────────────────────────────────────────

/// A parser that reads any file as plain UTF-8 text, ignoring format.
///
/// Useful as a fallback or in tests.
#[derive(Debug, Default)]
pub struct PlainTextParser;

#[async_trait]
impl Parser for PlainTextParser {
    fn kind(&self) -> ParserKind {
        ParserKind::PlainText
    }

    async fn parse(&self, path: &Path) -> Result<Document> {
        let content = tokio::fs::read_to_string(path)
            .await
            .map_err(|e| CascadeError::io(path, "read", e))?;
        Ok(Document {
            source_path: Some(path.to_path_buf()),
            content,
            mime_type: Some("text/plain".into()),
            metadata: Default::default(),
        })
    }
}

// ── Object-safety check ───────────────────────────────────────────────────────

fn _assert_object_safe(_: &dyn Parser) {}
