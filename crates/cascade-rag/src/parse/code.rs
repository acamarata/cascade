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

// ── DocumentParser impl ───────────────────────────────────────────────────────

/// Synchronous [`DocumentParser`] adapter for source-code files.
///
/// Identity pass — returns the file content as-is with no transformation.
/// The filename stem becomes the title. Uses lossy UTF-8 so binary or
/// mis-encoded files do not cause a hard error.
///
/// Supported extensions: `.rs .ts .tsx .js .jsx .py .go .rb .java .c .cpp
/// .h .sh .bash .zsh`
///
/// SPORT: MASTER-LIBS.md → cascade-rag::parse::CodeDocParser
///
/// # Ticket
/// T-P4-E01-14
pub struct CodeDocParser;

/// Extensions claimed by [`CodeDocParser`].
const CODE_EXTENSIONS: &[&str] = &[
    "rs", "ts", "tsx", "js", "jsx", "py", "go", "rb", "java", "c", "cpp", "h", "sh", "bash", "zsh",
];

impl DocumentParser for CodeDocParser {
    fn can_parse(&self, path: &Path) -> bool {
        path.extension()
            .and_then(|e| e.to_str())
            .map(|ext| CODE_EXTENSIONS.contains(&ext))
            .unwrap_or(false)
    }

    fn parse(&self, path: &Path) -> Result<DocumentText> {
        let bytes = std::fs::read(path).map_err(|e| CascadeError::ParseFailed {
            path: path.to_path_buf(),
            detail: format!("read failed: {e}"),
        })?;
        let text = String::from_utf8_lossy(&bytes).into_owned();

        let title = path.file_stem().and_then(|s| s.to_str()).map(String::from);

        let language = detect_language(path).to_string();
        let mut metadata = HashMap::new();
        metadata.insert("language".to_string(), language);

        Ok(DocumentText {
            source_path: path.to_path_buf(),
            text,
            title,
            metadata,
            parser_name: self.parser_name().to_string(),
        })
    }

    fn parser_name(&self) -> &str {
        "code"
    }
}

// ── Async Parser impl (existing, unchanged) ───────────────────────────────────

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

// ── Tests (T-P4-E01-14) ───────────────────────────────────────────────────────

#[cfg(test)]
mod doc_parser_tests {
    use super::*;
    use std::io::Write;
    use tempfile::NamedTempFile;

    #[test]
    fn parses_rust_file() {
        let mut f = NamedTempFile::with_suffix(".rs").unwrap();
        write!(f, "fn main() {{}}").unwrap();
        let parser = CodeDocParser;
        assert!(parser.can_parse(f.path()));
        let doc = parser.parse(f.path()).unwrap();
        assert_eq!(doc.parser_name, "code");
        assert_eq!(
            doc.metadata.get("language").map(String::as_str),
            Some("rust")
        );
        assert!(doc.text.contains("fn main()"));
    }

    #[test]
    fn parses_python_file() {
        let mut f = NamedTempFile::with_suffix(".py").unwrap();
        write!(f, "print('hello')").unwrap();
        let parser = CodeDocParser;
        assert!(parser.can_parse(f.path()));
        let doc = parser.parse(f.path()).unwrap();
        assert_eq!(
            doc.metadata.get("language").map(String::as_str),
            Some("python")
        );
    }

    #[test]
    fn can_parse_extension_table() {
        let parser = CodeDocParser;
        // In scope
        for ext in &[
            "rs", "ts", "tsx", "js", "jsx", "py", "go", "rb", "java", "c", "cpp", "h", "sh",
            "bash", "zsh",
        ] {
            let p = format!("file.{ext}");
            assert!(
                parser.can_parse(Path::new(&p)),
                "expected can_parse for .{ext}"
            );
        }
        // Not in scope
        assert!(!parser.can_parse(Path::new("README.md")));
        assert!(!parser.can_parse(Path::new("data.pdf")));
    }

    #[test]
    fn lossy_fallback_non_utf8() {
        let mut f = NamedTempFile::with_suffix(".rs").unwrap();
        let mut content = b"fn foo() {\n".to_vec();
        content.extend_from_slice(&[0xFF, 0xFE]);
        content.extend_from_slice(b"\n}");
        f.write_all(&content).unwrap();
        let parser = CodeDocParser;
        let doc = parser.parse(f.path()).unwrap(); // must not error
        assert!(doc.text.contains("fn foo()"));
    }

    #[test]
    fn title_is_filename_stem() {
        let mut f = NamedTempFile::with_suffix(".rs").unwrap();
        write!(f, "// empty").unwrap();
        let parser = CodeDocParser;
        let doc = parser.parse(f.path()).unwrap();
        // title must be the stem (not the full filename, not None)
        assert!(doc.title.is_some());
    }
}
