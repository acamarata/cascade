//! Language detection from file extension or MIME type.

use std::path::Path;

/// Detected source language.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SourceLanguage {
    Rust,
    TypeScript,
    JavaScript,
    Python,
}

impl SourceLanguage {
    /// Human-readable name used in metadata.
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Rust => "rust",
            Self::TypeScript => "typescript",
            Self::JavaScript => "javascript",
            Self::Python => "python",
        }
    }

    /// Detect from file extension.
    pub fn from_extension(ext: &str) -> Option<Self> {
        match ext.to_lowercase().as_str() {
            "rs" => Some(Self::Rust),
            "ts" | "tsx" => Some(Self::TypeScript),
            "js" | "jsx" => Some(Self::JavaScript),
            "py" => Some(Self::Python),
            _ => None,
        }
    }

    /// Detect from MIME type prefix like `text/x-rust`.
    pub fn from_mime(mime: &str) -> Option<Self> {
        let lang = mime.strip_prefix("text/x-")?;
        match lang {
            "rust" => Some(Self::Rust),
            "typescript" | "tsx" => Some(Self::TypeScript),
            "javascript" | "jsx" => Some(Self::JavaScript),
            "python" => Some(Self::Python),
            _ => None,
        }
    }
}

/// Detect language from a document's source_path extension, falling back to MIME.
pub fn detect_language_for(path: Option<&Path>, mime: Option<&str>) -> Option<SourceLanguage> {
    if let Some(p) = path {
        if let Some(ext) = p.extension().and_then(|e| e.to_str()) {
            if let Some(lang) = SourceLanguage::from_extension(ext) {
                return Some(lang);
            }
        }
    }
    mime.and_then(SourceLanguage::from_mime)
}
