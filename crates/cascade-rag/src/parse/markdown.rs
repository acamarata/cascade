//! Markdown parser.
//!
//! Reads a `.md` / `.mdx` file, strips YAML/TOML frontmatter, and produces a
//! [`Document`] with:
//!
//! - `mime_type = "text/markdown"`
//! - `metadata["title"]` — first H1 heading, if present
//! - `metadata["frontmatter"]` — raw frontmatter key-value pairs (as JSON)
//! - `metadata["code_languages"]` — list of languages in fenced code blocks
//!
//! Code fences are preserved in the document body so the chunker can extract
//! them as atomic units.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::parse::MarkdownParser
//!
//! Sprint ticket: T-P1-E7-S02-02

use async_trait::async_trait;
use serde_json::json;
use std::collections::HashMap;
use std::path::Path;

use cascade_types::{
    chunker::Document,
    error::{CascadeError, Result},
    parser::{Parser, ParserKind},
};

use super::{DocumentParser, DocumentText};

/// Markdown parser.
#[derive(Debug, Default)]
pub struct MarkdownParser;

#[async_trait]
impl Parser for MarkdownParser {
    fn kind(&self) -> ParserKind {
        ParserKind::Markdown
    }

    async fn parse(&self, path: &Path) -> Result<Document> {
        let raw = tokio::fs::read_to_string(path)
            .await
            .map_err(|e| CascadeError::io(path, "read", e))?;

        let (frontmatter, body) = strip_frontmatter(&raw);
        let title = extract_title(body);
        let code_langs = extract_code_languages(body);

        let mut metadata = std::collections::HashMap::new();
        if let Some(fm) = frontmatter {
            metadata.insert("frontmatter".into(), json!(fm));
        }
        if let Some(t) = title {
            metadata.insert("title".into(), json!(t));
        }
        if !code_langs.is_empty() {
            metadata.insert("code_languages".into(), json!(code_langs));
        }

        Ok(Document {
            source_path: Some(path.to_path_buf()),
            content: body.to_string(),
            mime_type: Some("text/markdown".into()),
            metadata,
        })
    }
}

// ── DocumentParser impl ───────────────────────────────────────────────────────

/// Synchronous [`DocumentParser`] adapter for Markdown files.
///
/// Strips YAML front-matter, extracts the first H1 as the title, and returns
/// the body as plain text. Uses lossy UTF-8 conversion so binary or
/// mis-encoded files do not cause a hard error.
///
/// SPORT: MASTER-LIBS.md → cascade-rag::parse::MarkdownDocParser
///
/// # Ticket
/// T-P4-E01-14
pub struct MarkdownDocParser;

impl DocumentParser for MarkdownDocParser {
    fn can_parse(&self, path: &Path) -> bool {
        matches!(
            path.extension().and_then(|e| e.to_str()),
            Some("md") | Some("mdx") | Some("markdown")
        )
    }

    fn parse(&self, path: &Path) -> Result<DocumentText> {
        // Lossy UTF-8: never fails on binary content.
        let bytes = std::fs::read(path).map_err(|e| CascadeError::ParseFailed {
            path: path.to_path_buf(),
            detail: format!("read failed: {e}"),
        })?;
        let raw = String::from_utf8_lossy(&bytes).into_owned();

        let (_frontmatter, body) = strip_frontmatter(&raw);
        let title = extract_title(body);

        let mut metadata = HashMap::new();
        if let Some(ref fm) = _frontmatter {
            metadata.insert("frontmatter".to_string(), fm.clone());
        }

        Ok(DocumentText {
            source_path: path.to_path_buf(),
            text: body.to_string(),
            title,
            metadata,
            parser_name: self.parser_name().to_string(),
        })
    }

    fn parser_name(&self) -> &str {
        "markdown"
    }
}

// ── Async Parser impl (existing, unchanged) ───────────────────────────────────

fn strip_frontmatter(text: &str) -> (Option<String>, &str) {
    let t = text.trim_start();
    if !t.starts_with("---") {
        return (None, text);
    }
    let after = &t[3..];
    if let Some(close) = after.find("\n---") {
        let fm = after[..close].trim().to_string();
        (Some(fm), &after[close + 4..])
    } else {
        (None, text)
    }
}

fn extract_title(text: &str) -> Option<String> {
    text.lines()
        .find(|l| l.starts_with("# "))
        .map(|l| l[2..].trim().to_string())
}

fn extract_code_languages(text: &str) -> Vec<String> {
    let mut langs = Vec::new();
    for line in text.lines() {
        let trimmed = line.trim_start();
        if let Some(rest) = trimmed.strip_prefix("```") {
            let lang = rest.split_whitespace().next().unwrap_or("").to_string();
            if !lang.is_empty() {
                langs.push(lang);
            }
        }
    }
    langs.sort();
    langs.dedup();
    langs
}

// ── Tests (T-P4-E01-14) ───────────────────────────────────────────────────────

#[cfg(test)]
mod doc_parser_tests {
    use super::*;
    use std::io::Write;
    use tempfile::NamedTempFile;

    #[test]
    fn strips_frontmatter_and_extracts_title() {
        let mut f = NamedTempFile::with_suffix(".md").unwrap();
        write!(f, "---\nauthor: test\n---\n# My Title\n\nHello world.").unwrap();
        let parser = MarkdownDocParser;
        assert!(parser.can_parse(f.path()));
        let doc = parser.parse(f.path()).unwrap();
        assert_eq!(doc.parser_name, "markdown");
        assert_eq!(doc.title.as_deref(), Some("My Title"));
        assert!(
            !doc.text.contains("author: test"),
            "front-matter must be stripped"
        );
        assert!(doc.text.contains("Hello world."));
    }

    #[test]
    fn no_frontmatter_still_works() {
        let mut f = NamedTempFile::with_suffix(".md").unwrap();
        write!(f, "# Just a heading\nsome content").unwrap();
        let parser = MarkdownDocParser;
        let doc = parser.parse(f.path()).unwrap();
        assert_eq!(doc.title.as_deref(), Some("Just a heading"));
        assert!(doc.text.contains("some content"));
    }

    #[test]
    fn can_parse_extension_table() {
        let parser = MarkdownDocParser;
        assert!(parser.can_parse(Path::new("README.md")));
        assert!(parser.can_parse(Path::new("page.mdx")));
        assert!(parser.can_parse(Path::new("notes.markdown")));
        assert!(!parser.can_parse(Path::new("main.rs")));
        assert!(!parser.can_parse(Path::new("data.txt")));
    }

    #[test]
    fn lossy_fallback_non_utf8() {
        let mut f = NamedTempFile::with_suffix(".md").unwrap();
        // Write some invalid UTF-8 bytes surrounded by valid text.
        let mut content = b"# Title\n".to_vec();
        content.extend_from_slice(&[0xFF, 0xFE]);
        content.extend_from_slice(b"\nvalid after");
        f.write_all(&content).unwrap();
        let parser = MarkdownDocParser;
        let doc = parser.parse(f.path()).unwrap(); // must not error
        assert_eq!(doc.title.as_deref(), Some("Title"));
        assert!(doc.text.contains("valid after"));
    }
}
