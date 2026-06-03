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
use std::path::Path;

use cascade_types::{
    chunker::Document,
    error::{CascadeError, Result},
    parser::{Parser, ParserKind},
};

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
            let lang = rest
                .trim()
                .split_whitespace()
                .next()
                .unwrap_or("")
                .to_string();
            if !lang.is_empty() {
                langs.push(lang);
            }
        }
    }
    langs.sort();
    langs.dedup();
    langs
}
