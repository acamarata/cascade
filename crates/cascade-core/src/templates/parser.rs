//! # cascade_core::templates::parser
//!
//! Parses a `.md` template file into a [`TemplateRecord`].
//!
//! ## Purpose
//!
//! A template file consists of:
//! 1. An opening `---` line.
//! 2. A TOML frontmatter block.
//! 3. A closing `---` line.
//! 4. The Markdown body (everything after the closing delimiter).
//!
//! ## Inputs
//!
//! `parse_template_file(path)` — path to a `.md` file on disk.
//!
//! ## Outputs
//!
//! `Result<TemplateRecord>` — parsed record, or a `CascadeError::Other` describing
//! the parse failure. Errors here are treated as non-fatal by the registry (files
//! are skipped with a `warn` log).
//!
//! ## Constraints
//!
//! - Files that have no opening `---` on the first non-empty line return `Err`.
//! - Files whose frontmatter does not deserialise as `TemplateManifest` return `Err`.
//! - Files with missing closing `---` return `Err`.
//!
//! ## SPORT
//!
//! Part of `cascade-core::templates` (T-P3-E05-02).

use cascade_types::{error::CascadeError, error::Result, TemplateManifest, TemplateRecord};
use std::path::Path;

/// Parse a single `.md` template file.
///
/// The file must begin with a `---` line, contain TOML frontmatter matching
/// [`TemplateManifest`], and have a closing `---` line. Everything after the
/// closing delimiter is the template body.
///
/// # Errors
///
/// Returns `CascadeError::Other` with a descriptive message if:
/// - The file cannot be read.
/// - No opening `---` is found.
/// - No closing `---` is found after the opening.
/// - The frontmatter fails TOML deserialization.
pub fn parse_template_file(path: &Path) -> Result<TemplateRecord> {
    let raw = std::fs::read_to_string(path)
        .map_err(|e| CascadeError::Other(format!("read template {}: {}", path.display(), e)))?;

    split_frontmatter(&raw)
        .map_err(|e| CascadeError::Other(format!("parse template {}: {}", path.display(), e)))
}

/// Split raw template text into manifest + body.
///
/// Exported for unit testing without filesystem I/O.
pub(crate) fn split_frontmatter(raw: &str) -> std::result::Result<TemplateRecord, String> {
    let mut lines = raw.lines();

    // Consume blank lines before the opening delimiter.
    let first_content = lines
        .by_ref()
        .find(|l| !l.trim().is_empty())
        .ok_or_else(|| "template file is empty".to_string())?;

    if first_content.trim() != "---" {
        return Err(format!(
            "expected opening '---' delimiter, got: {:?}",
            first_content
        ));
    }

    // Collect frontmatter lines until the closing `---`.
    let mut fm_lines: Vec<&str> = Vec::new();
    let mut found_close = false;
    for line in lines.by_ref() {
        if line.trim() == "---" {
            found_close = true;
            break;
        }
        fm_lines.push(line);
    }

    if !found_close {
        return Err("no closing '---' delimiter found in template".to_string());
    }

    let frontmatter = fm_lines.join("\n");
    let manifest: TemplateManifest = toml::from_str(&frontmatter)
        .map_err(|e| format!("TOML parse error in frontmatter: {}", e))?;

    // Reconstruct body: everything after the second `---`.
    // We re-scan the original string for the two delimiters.
    let body = extract_body_after_second_delimiter(raw);

    Ok(TemplateRecord { manifest, body })
}

/// Find the body text after the second `---` delimiter line.
fn extract_body_after_second_delimiter(raw: &str) -> String {
    let mut delimiter_count = 0;
    let mut body_start: Option<usize> = None;
    let mut cursor = 0usize;

    for line in raw.lines() {
        let line_len = line.len();
        if line.trim() == "---" {
            delimiter_count += 1;
            if delimiter_count == 2 {
                // Body starts after this line + its newline character(s).
                let after = cursor + line_len;
                // Skip one newline (\r\n or \n).
                let skip = if raw.as_bytes().get(after) == Some(&b'\r') {
                    2
                } else if raw.as_bytes().get(after) == Some(&b'\n') {
                    1
                } else {
                    0
                };
                body_start = Some(after + skip);
                break;
            }
        }
        // Advance cursor by line length + 1 for \n (or 2 for \r\n).
        cursor += line_len;
        let next = raw.as_bytes().get(cursor);
        if next == Some(&b'\r') {
            cursor += 2; // \r\n
        } else if next == Some(&b'\n') {
            cursor += 1; // \n
        }
    }

    match body_start {
        Some(pos) => raw[pos..].to_string(),
        None => String::new(),
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::TemplateTier;

    #[test]
    fn parse_minimal_template() {
        let raw = "---\n\
id = \"gci-default\"\n\
version = \"1.0.0\"\n\
tier = \"gci\"\n\
stacks = []\n\
project_shapes = []\n\
description = \"Minimal GCI starter template\"\n\
---\n\
# Body\n\
\n\
Content here.\n";

        let rec = split_frontmatter(raw).expect("should parse");
        assert_eq!(rec.manifest.id, "gci-default");
        assert_eq!(rec.manifest.tier, TemplateTier::Gci);
        assert!(rec.body.contains("# Body"));
    }

    #[test]
    fn parse_template_with_optional_fields() {
        let raw = "---\n\
id = \"rust-lib\"\n\
version = \"2.0.0\"\n\
tier = \"prc\"\n\
stacks = [\"rust\"]\n\
project_shapes = [\"lib\"]\n\
description = \"Rust lib template\"\n\
extends = \"gci-default\"\n\
min_cascade_version = \"0.3.0\"\n\
---\n\
body content\n";

        let rec = split_frontmatter(raw).expect("should parse");
        assert_eq!(rec.manifest.extends, Some("gci-default".to_string()));
        assert_eq!(rec.manifest.min_cascade_version, Some("0.3.0".to_string()));
        assert_eq!(rec.body, "body content\n");
    }

    #[test]
    fn parse_empty_body() {
        let raw = "---\n\
id = \"x\"\n\
version = \"1.0.0\"\n\
tier = \"any\"\n\
stacks = []\n\
project_shapes = []\n\
description = \"no body\"\n\
---\n";

        let rec = split_frontmatter(raw).expect("should parse");
        assert!(rec.body.is_empty() || rec.body.trim().is_empty());
    }

    #[test]
    fn error_on_no_opening_delimiter() {
        let raw = "id = \"x\"\nversion = \"1.0.0\"\n";
        assert!(split_frontmatter(raw).is_err());
    }

    #[test]
    fn error_on_no_closing_delimiter() {
        let raw = "---\nid = \"x\"\nversion = \"1.0.0\"\ntier = \"gci\"\n";
        assert!(split_frontmatter(raw).is_err());
    }

    #[test]
    fn error_on_bad_toml() {
        let raw = "---\nnot valid toml @@@ !!!\n---\nbody\n";
        assert!(split_frontmatter(raw).is_err());
    }

    #[test]
    fn error_on_empty_file() {
        assert!(split_frontmatter("").is_err());
        assert!(split_frontmatter("   \n  \n").is_err());
    }
}
