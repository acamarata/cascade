//! Source code parser.
//!
//! Reads any supported source file and produces a [`Document`] tagged with:
//!
//! - `mime_type = "text/x-{language}"` (e.g. `"text/x-rust"`)
//! - `metadata["language"]` — detected language string
//! - `metadata["line_count"]` — total line count
//!
//! The content is the raw source text; structural analysis (function extraction,
//! call-graph) is deferred to the code chunker [`crate::chunk::code::CodeChunker`].
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::parse::CodeParser

use std::path::Path;
use async_trait::async_trait;
use serde_json::json;

use cascade_types::{
    chunker::Document,
    error::{CascadeError, Result},
    parser::{Parser, ParserKind},
};

/// Source code parser (language-agnostic).
#[derive(Debug, Default)]
pub struct CodeParser;

#[async_trait]
impl Parser for CodeParser {
    fn kind(&self) -> ParserKind {
        ParserKind::Code
    }

    async fn parse(&self, path: &Path) -> Result<Document> {
        let raw = tokio::fs::read_to_string(path)
            .await
            .map_err(|e| CascadeError::io(path, "read", e))?;

        let language = detect_language(path);
        let line_count = raw.lines().count();

        let mut metadata = std::collections::HashMap::new();
        metadata.insert("language".into(), json!(language));
        metadata.insert("line_count".into(), json!(line_count));

        Ok(Document {
            source_path: Some(path.to_path_buf()),
            content: raw,
            mime_type: Some(format!("text/x-{language}")),
            metadata,
        })
    }
}

/// Map file extension to a canonical language name.
fn detect_language(path: &Path) -> &'static str {
    match path.extension().and_then(|e| e.to_str()) {
        Some("rs") => "rust",
        Some("py") => "python",
        Some("ts") | Some("tsx") => "typescript",
        Some("js") | Some("jsx") | Some("mjs") | Some("cjs") => "javascript",
        Some("go") => "go",
        Some("java") => "java",
        Some("c") => "c",
        Some("cpp") | Some("cc") | Some("cxx") => "cpp",
        Some("cs") => "csharp",
        Some("rb") => "ruby",
        Some("php") => "php",
        Some("swift") => "swift",
        Some("kt") | Some("kts") => "kotlin",
        Some("dart") => "dart",
        Some("lua") => "lua",
        Some("scala") => "scala",
        Some("r") | Some("R") => "r",
        Some("jl") => "julia",
        Some("ex") | Some("exs") => "elixir",
        Some("ml") | Some("mli") => "ocaml",
        Some("hs") => "haskell",
        Some("zig") => "zig",
        Some("sh") | Some("bash") => "bash",
        Some("sql") => "sql",
        _ => "unknown",
    }
}
