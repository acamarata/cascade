//! HTML parser.
//!
//! Reads an `.html` / `.htm` / `.xhtml` file and produces a [`DocumentText`] with:
//!
//! - All `<script>`, `<style>`, `<nav>`, `<footer>`, `<header>`, `<aside>` elements removed.
//! - HTML comments stripped (handled by the html5ever tree automatically).
//! - Remaining element text extracted; block-level elements separated by newlines.
//! - HTML entities decoded (handled by scraper / html5ever automatically).
//! - `<title>` tag content mapped to [`DocumentText::title`].
//! - `<meta name="description">` mapped to `metadata["description"]`.
//! - Multiple whitespace sequences (spaces, repeated newlines) collapsed.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::parse::HtmlParser
//!
//! # Ticket
//! T-P4-E01-15

use std::collections::HashMap;
use std::path::Path;

use scraper::{ElementRef, Html, Selector};

use cascade_types::error::{CascadeError, Result};

use super::{DocumentParser, DocumentText};

// Tags whose content is excluded from text extraction entirely.
const SKIP_TAGS: &[&str] = &[
    "script", "style", "nav", "footer", "header", "aside", "noscript",
];

// Block-level tags that get a newline inserted after their contribution.
const BLOCK_TAGS: &[&str] = &[
    "p",
    "div",
    "section",
    "article",
    "main",
    "h1",
    "h2",
    "h3",
    "h4",
    "h5",
    "h6",
    "ul",
    "ol",
    "li",
    "table",
    "thead",
    "tbody",
    "tr",
    "th",
    "td",
    "blockquote",
    "pre",
    "figure",
    "figcaption",
    "form",
    "fieldset",
    "br",
    "hr",
];

/// HTML document parser.
///
/// Parses HTML files using the `scraper` crate (backed by html5ever), strips
/// non-content elements, and extracts plain text suitable for RAG indexing.
///
/// SPORT: MASTER-LIBS.md → cascade-rag::parse::HtmlParser
///
/// # Ticket
/// T-P4-E01-15
#[derive(Debug, Default)]
pub struct HtmlParser;

impl HtmlParser {
    /// Recursively walk an [`ElementRef`] subtree, appending text to `buf`.
    ///
    /// Skips subtrees whose tag name appears in [`SKIP_TAGS`].
    /// Appends `\n` after block-level elements.
    fn visit_element(el: ElementRef<'_>, buf: &mut String) {
        let tag = el.value().name();

        if SKIP_TAGS.contains(&tag) {
            return;
        }

        for child in el.children() {
            match child.value() {
                scraper::node::Node::Text(text) => {
                    buf.push_str(text);
                }
                scraper::node::Node::Element(_) => {
                    if let Some(child_el) = ElementRef::wrap(child) {
                        Self::visit_element(child_el, buf);
                    }
                }
                // Comments, ProcessingInstructions, Doctypes — skip.
                _ => {}
            }
        }

        // Structural newline after block-level element.
        if BLOCK_TAGS.contains(&tag) {
            buf.push('\n');
        }
    }

    /// Extract plain text from a parsed [`Html`] document.
    fn extract_text(document: &Html) -> String {
        // Start from <body> when present, else fall back to the root.
        let body_sel = Selector::parse("body").expect("static selector");
        let mut buf = String::with_capacity(4096);

        if let Some(body) = document.select(&body_sel).next() {
            Self::visit_element(body, &mut buf);
        } else {
            // No <body>: walk from the document root element.
            Self::visit_element(document.root_element(), &mut buf);
        }

        Self::collapse_whitespace(&buf)
    }

    /// Collapse runs of horizontal whitespace to a single space, and allow at
    /// most two consecutive newlines (paragraph separation).
    fn collapse_whitespace(s: &str) -> String {
        let mut result = String::with_capacity(s.len());
        let mut prev_space = false;
        let mut prev_newline = false;
        let mut newline_run: usize = 0;

        for ch in s.chars() {
            match ch {
                '\n' => {
                    newline_run += 1;
                    if newline_run <= 2 {
                        result.push('\n');
                    }
                    prev_space = false;
                    prev_newline = true;
                }
                '\r' | '\t' | ' ' => {
                    if !prev_space && !prev_newline {
                        result.push(' ');
                    }
                    prev_space = true;
                }
                _ => {
                    newline_run = 0;
                    prev_space = false;
                    prev_newline = false;
                    result.push(ch);
                }
            }
        }

        result.trim().to_owned()
    }

    /// Extract `<title>` text, trimmed.
    fn extract_title(document: &Html) -> Option<String> {
        let sel = Selector::parse("title").expect("static selector");
        document
            .select(&sel)
            .next()
            .map(|el| el.text().collect::<Vec<_>>().join("").trim().to_owned())
            .filter(|s| !s.is_empty())
    }

    /// Extract `<meta name="description" content="...">`.
    fn extract_description(document: &Html) -> Option<String> {
        let sel = Selector::parse(r#"meta[name="description"]"#).expect("static selector");
        document
            .select(&sel)
            .next()
            .and_then(|el| el.value().attr("content").map(|s| s.trim().to_owned()))
            .filter(|s| !s.is_empty())
    }
}

impl DocumentParser for HtmlParser {
    fn can_parse(&self, path: &Path) -> bool {
        matches!(
            path.extension()
                .and_then(|e| e.to_str())
                .map(|e| e.to_ascii_lowercase())
                .as_deref(),
            Some("html" | "htm" | "xhtml")
        )
    }

    fn parse(&self, path: &Path) -> Result<DocumentText> {
        let raw = std::fs::read(path).map_err(|e| CascadeError::ParseFailed {
            path: path.to_path_buf(),
            detail: format!("read failed: {e}"),
        })?;
        let html_str = String::from_utf8_lossy(&raw);
        let document = Html::parse_document(&html_str);

        let title = Self::extract_title(&document);
        let text = Self::extract_text(&document);
        let stem = path.file_stem().and_then(|s| s.to_str()).map(String::from);

        let mut metadata = HashMap::new();
        if let Some(desc) = Self::extract_description(&document) {
            metadata.insert("description".into(), desc);
        }

        Ok(DocumentText {
            source_path: path.to_path_buf(),
            text,
            title: title.or(stem),
            metadata,
            parser_name: self.parser_name().into(),
        })
    }

    fn parser_name(&self) -> &str {
        "html"
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;
    use tempfile::NamedTempFile;

    fn write_html(content: &str) -> NamedTempFile {
        let mut f = NamedTempFile::with_suffix(".html").unwrap();
        write!(f, "{content}").unwrap();
        f
    }

    // ── T-P4-E01-15-01: can_parse extensions ──────────────────────────────────

    /// can_parse returns true for .html, .htm, .xhtml; false for others.
    #[test]
    fn can_parse_extensions() {
        let p = HtmlParser;
        assert!(p.can_parse(Path::new("page.html")));
        assert!(p.can_parse(Path::new("page.htm")));
        assert!(p.can_parse(Path::new("page.xhtml")));
        assert!(p.can_parse(Path::new("PAGE.HTML"))); // case-insensitive
        assert!(!p.can_parse(Path::new("doc.md")));
        assert!(!p.can_parse(Path::new("code.rs")));
        assert!(!p.can_parse(Path::new("data.json")));
    }

    // ── T-P4-E01-15-02: title extraction ─────────────────────────────────────

    /// <title> tag content is set as DocumentText.title.
    #[test]
    fn title_extracted() {
        let f = write_html(
            "<html><head><title>My Page Title</title></head><body><p>Hello</p></body></html>",
        );
        let p = HtmlParser;
        let doc = p.parse(f.path()).unwrap();
        assert_eq!(doc.title.as_deref(), Some("My Page Title"));
        assert_eq!(doc.parser_name, "html");
    }

    // ── T-P4-E01-15-03: script/style removal ─────────────────────────────────

    /// <script> and <style> content is excluded from extracted text.
    #[test]
    fn script_style_removed() {
        let f = write_html(
            r#"<html><head>
                <style>body { color: red; }</style>
                <script>alert("hello");</script>
            </head><body>
                <p>Visible text here.</p>
                <script>console.log("also removed");</script>
            </body></html>"#,
        );
        let p = HtmlParser;
        let doc = p.parse(f.path()).unwrap();
        assert!(
            !doc.text.contains("color: red"),
            "CSS must be stripped: {:?}",
            doc.text
        );
        assert!(
            !doc.text.contains("alert"),
            "JS must be stripped: {:?}",
            doc.text
        );
        assert!(
            !doc.text.contains("console.log"),
            "inline script must be stripped: {:?}",
            doc.text
        );
        assert!(
            doc.text.contains("Visible text here"),
            "body text must be present: {:?}",
            doc.text
        );
    }

    // ── T-P4-E01-15-04: nav/footer/header/aside removal ──────────────────────

    /// <nav>, <footer>, <header>, <aside> content is excluded.
    #[test]
    fn structural_elements_removed() {
        let f = write_html(
            r#"<html><body>
                <header>Site Header</header>
                <nav>Navigation links</nav>
                <main><p>Main content</p></main>
                <aside>Sidebar</aside>
                <footer>Footer text</footer>
            </body></html>"#,
        );
        let p = HtmlParser;
        let doc = p.parse(f.path()).unwrap();
        assert!(!doc.text.contains("Site Header"), "header must be stripped");
        assert!(
            !doc.text.contains("Navigation links"),
            "nav must be stripped"
        );
        assert!(!doc.text.contains("Sidebar"), "aside must be stripped");
        assert!(!doc.text.contains("Footer text"), "footer must be stripped");
        assert!(
            doc.text.contains("Main content"),
            "main content must remain"
        );
    }

    // ── T-P4-E01-15-05: entity decoding ──────────────────────────────────────

    /// HTML entities are decoded to their Unicode equivalents.
    #[test]
    fn entities_decoded() {
        let f = write_html("<html><body><p>Price: &pound;10 &amp; &lt;free&gt;</p></body></html>");
        let p = HtmlParser;
        let doc = p.parse(f.path()).unwrap();
        assert!(
            doc.text.contains('£'),
            "£ entity must be decoded: {:?}",
            doc.text
        );
        assert!(
            doc.text.contains('&'),
            "&amp; must be decoded: {:?}",
            doc.text
        );
        assert!(
            doc.text.contains('<'),
            "&lt; must be decoded: {:?}",
            doc.text
        );
    }

    // ── T-P4-E01-15-06: whitespace collapse ───────────────────────────────────

    /// Multiple whitespace sequences are collapsed to a single space.
    #[test]
    fn whitespace_collapsed() {
        let f = write_html("<html><body><p>Too   many    spaces   here</p></body></html>");
        let p = HtmlParser;
        let doc = p.parse(f.path()).unwrap();
        assert!(
            !doc.text.contains("   "),
            "triple spaces must be collapsed: {:?}",
            doc.text
        );
    }

    // ── T-P4-E01-15-07: malformed HTML tolerance ──────────────────────────────

    /// Parser handles malformed / unclosed tags gracefully without panicking.
    #[test]
    fn malformed_html_tolerant() {
        let f = write_html("<html><body><p>Unclosed paragraph<div>nested<b>bold</div></body>");
        let p = HtmlParser;
        // Must not panic; should return some non-empty text.
        let doc = p.parse(f.path()).unwrap();
        assert!(
            !doc.text.is_empty(),
            "malformed HTML should still yield text"
        );
    }

    // ── T-P4-E01-15-08: fixture realistic page ───────────────────────────────

    /// Fixture page: a realistic HTML document yields meaningful text.
    #[test]
    fn fixture_realistic_page() {
        let f = write_html(
            r#"<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="description" content="A cascade context tool">
    <title>Cascade — Context Cascade Tool</title>
    <style>
        .hidden { display: none; }
    </style>
</head>
<body>
    <header><nav><a href="/">Home</a></nav></header>
    <main>
        <h1>Welcome to Cascade</h1>
        <p>Cascade is a multi-agent context management tool.</p>
        <ul>
            <li>RAG support</li>
            <li>BGE-M3 embeddings</li>
        </ul>
        <script>trackPageView();</script>
    </main>
    <footer>Copyright 2026</footer>
</body>
</html>"#,
        );
        let p = HtmlParser;
        let doc = p.parse(f.path()).unwrap();

        assert_eq!(doc.title.as_deref(), Some("Cascade — Context Cascade Tool"));
        assert!(
            doc.text.contains("Welcome to Cascade"),
            "h1 must be present"
        );
        assert!(
            doc.text.contains("multi-agent"),
            "paragraph text must be present"
        );
        assert!(
            doc.text.contains("RAG support"),
            "list items must be present"
        );
        assert!(
            !doc.text.contains("trackPageView"),
            "script must be removed"
        );
        assert!(!doc.text.contains("display: none"), "style must be removed");
        assert!(
            !doc.text.contains("Copyright 2026"),
            "footer must be removed"
        );
        assert_eq!(
            doc.metadata.get("description").map(String::as_str),
            Some("A cascade context tool")
        );
    }
}
