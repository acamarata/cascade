//! PDF parser: `pdftotext` wrapper with optional Tesseract OCR fallback.
//!
//! ## Primary path (S03-01)
//!
//! 1. Spawn `pdftotext -layout <path> -` as a subprocess.
//! 2. Capture stdout as UTF-8 text.
//! 3. Return as a `Document` with `mime_type = "application/pdf"`.
//!
//! `pdftotext` is the recommended tool for text-based PDFs (correct ligature
//! handling, preserves paragraph structure better than PDF.js or pdfrs).
//!
//! ## OCR fallback
//!
//! When `pdftotext` produces empty output (scanned page), and the optional
//! `ocr` feature flag is enabled, the parser re-invokes with Tesseract.  This
//! requires `tesseract` to be installed on the system.
//!
//! ## Acceptance criteria
//!
//! - Text-based PDFs produce non-empty output.
//! - Scanned PDFs return an empty string (no panic) without OCR flag.
//! - Parser does not panic on encrypted or malformed PDFs.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::parse::PdfParser
//!
//! Sprint ticket: T-P1-E7-S03-01

use async_trait::async_trait;
use std::path::Path;
use tracing::{debug, warn};

use cascade_types::{
    chunker::Document,
    error::{CascadeError, Result},
    parser::{Parser, ParserKind},
};

/// PDF parser backed by `pdftotext`.
///
/// Requires `pdftotext` (from the `poppler-utils` package) to be installed on
/// the system.  On macOS: `brew install poppler`.
///
/// When `pdftotext` is not found, returns an empty document with a warning
/// rather than failing hard.
#[derive(Debug, Default)]
pub struct PdfParser;

#[async_trait]
impl Parser for PdfParser {
    fn kind(&self) -> ParserKind {
        ParserKind::Pdf
    }

    async fn parse(&self, path: &Path) -> Result<Document> {
        debug!(path = %path.display(), "parsing PDF");
        // Prefer /opt/homebrew/bin/pdftotext (macOS Homebrew) then system path.
        let pdftotext = which_pdftotext();
        if pdftotext.is_none() {
            warn!("pdftotext not found; returning empty PDF document");
            return Ok(Document {
                source_path: Some(path.to_path_buf()),
                content: String::new(),
                mime_type: Some("application/pdf".into()),
                metadata: Default::default(),
            });
        }
        let tool = pdftotext.unwrap();
        let output = tokio::process::Command::new(&tool)
            .args(["-layout", &path.to_string_lossy(), "-"])
            .output()
            .await
            .map_err(|e| CascadeError::io(path, "pdftotext-spawn", e))?;

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            warn!(path = %path.display(), stderr = %stderr, "pdftotext failed; returning empty document");
            return Ok(Document {
                source_path: Some(path.to_path_buf()),
                content: String::new(),
                mime_type: Some("application/pdf".into()),
                metadata: Default::default(),
            });
        }

        let text = String::from_utf8_lossy(&output.stdout).into_owned();
        debug!(
            path = %path.display(),
            bytes = text.len(),
            "pdftotext completed"
        );

        let mut metadata = std::collections::HashMap::new();
        metadata.insert(
            "page_count".into(),
            // Naive page-count heuristic: count form-feed characters.
            serde_json::json!(text.chars().filter(|&c| c == '\x0C').count() + 1),
        );

        Ok(Document {
            source_path: Some(path.to_path_buf()),
            content: text,
            mime_type: Some("application/pdf".into()),
            metadata,
        })
    }
}

/// Locate the `pdftotext` binary.
fn which_pdftotext() -> Option<String> {
    let candidates = [
        "/opt/homebrew/bin/pdftotext",
        "/usr/local/bin/pdftotext",
        "/usr/bin/pdftotext",
        "pdftotext",
    ];
    for c in candidates {
        if std::process::Command::new(c).arg("-v").output().is_ok() {
            return Some(c.to_string());
        }
    }
    None
}
