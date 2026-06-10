//! # cascade_core::templates::section_parser
//!
//! Parses a Markdown document into a flat list of [`Section`]s.
//!
//! ## Purpose
//!
//! The section parser splits Markdown on heading lines (`# …` through `######
//! …`) and returns each section as a (`heading`, `body`, `level`) triple.
//! The body is the text **after** the heading line up to (but not including)
//! the next heading of the same or higher level.
//!
//! ## Inputs
//!
//! `parse_sections(content: &str) -> Vec<Section>` — raw Markdown text.
//!
//! ## Outputs
//!
//! An ordered `Vec<Section>` preserving document order.  Leading/trailing
//! whitespace in body strings is preserved exactly as written (the apply
//! engine needs it for idempotency checks).
//!
//! ## Constraints
//!
//! - Headings inside fenced code blocks (`` ``` `` or `~~~`) are **not**
//!   treated as section starts.
//! - A document with no headings returns a single `Section` with an empty
//!   heading and the whole document as the body (level = 0).  This lets the
//!   apply engine handle prose-only files without panicking.
//! - The heading text returned in `Section::heading` includes the leading `#`
//!   characters and the space, e.g. `"## Overview"`.  This is intentional:
//!   the apply engine uses it as an opaque identifier for conflict detection,
//!   and round-tripping it back to Markdown is trivial.
//!
//! ## SPORT
//!
//! Part of `cascade-core::templates` (T-P3-E05-09).

// ── Section ───────────────────────────────────────────────────────────────────

/// A single Markdown heading + the text that follows it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Section {
    /// The full heading line, e.g. `"## Overview"`.
    ///
    /// For the implicit preamble section (text before the first heading) this
    /// is an empty string.
    pub heading: String,

    /// The text that follows the heading line, **not** including the heading
    /// itself.  May be empty if the section has no body.
    pub body: String,

    /// The heading level: 0 for the implicit preamble, 1–6 for ATX headings.
    pub level: u8,
}

impl Section {
    /// Render the section back to Markdown (heading line + body).
    ///
    /// For level-0 preamble sections the heading is omitted.
    pub fn to_markdown(&self) -> String {
        if self.level == 0 {
            self.body.clone()
        } else {
            format!("{}\n{}", self.heading, self.body)
        }
    }
}

// ── parse_sections ────────────────────────────────────────────────────────────

/// Parse `content` into an ordered list of [`Section`]s.
///
/// Headings inside fenced code blocks are ignored.  A document with no
/// headings outside code fences returns one section: `{heading: "", body:
/// content, level: 0}`.
///
/// The order of sections in the returned vec matches document order.
pub fn parse_sections(content: &str) -> Vec<Section> {
    let mut sections: Vec<Section> = Vec::new();
    let mut current_heading = String::new();
    let mut current_level: u8 = 0;
    let mut current_body = String::new();
    let mut in_fence = false;
    let mut fence_char = ' ';

    for line in content.lines() {
        // ── Track fenced code blocks ──────────────────────────────────────
        let trimmed = line.trim_start();
        if !in_fence {
            if trimmed.starts_with("```") || trimmed.starts_with("~~~") {
                in_fence = true;
                fence_char = trimmed.chars().next().unwrap();
                current_body.push_str(line);
                current_body.push('\n');
                continue;
            }
        } else {
            // Inside a fence: look for the matching closing fence.
            if trimmed.starts_with(fence_char)
                && trimmed.chars().all(|c| c == fence_char || c == '\n')
            {
                in_fence = false;
            }
            current_body.push_str(line);
            current_body.push('\n');
            continue;
        }

        // ── Detect ATX heading ────────────────────────────────────────────
        if let Some(level) = atx_heading_level(line) {
            // Flush current section.
            sections.push(Section {
                heading: current_heading.clone(),
                body: current_body.clone(),
                level: current_level,
            });
            current_heading = line.to_string();
            current_level = level;
            current_body = String::new();
        } else {
            current_body.push_str(line);
            current_body.push('\n');
        }
    }

    // Flush final section.
    sections.push(Section {
        heading: current_heading,
        body: current_body,
        level: current_level,
    });

    // Drop an initial empty/preamble section only when the document starts
    // with a heading (i.e. preamble section has empty heading AND empty body).
    if sections.len() > 1 {
        if let Some(first) = sections.first() {
            if first.heading.is_empty() && first.body.trim().is_empty() {
                sections.remove(0);
            }
        }
    }

    // If we ended up with zero sections (impossible in practice but defensive):
    if sections.is_empty() {
        sections.push(Section {
            heading: String::new(),
            body: content.to_string(),
            level: 0,
        });
    }

    sections
}

// ── helpers ───────────────────────────────────────────────────────────────────

/// Return the ATX heading level (1–6) if `line` is an ATX heading, else None.
///
/// An ATX heading starts with 1–6 `#` characters followed by a space or tab.
/// A line like `#hashtag` (no space) is **not** a heading.
fn atx_heading_level(line: &str) -> Option<u8> {
    let mut chars = line.chars().peekable();
    let mut count: u8 = 0;
    while chars.peek() == Some(&'#') {
        count += 1;
        chars.next();
        if count > 6 {
            return None;
        }
    }
    if count == 0 {
        return None;
    }
    // Must be followed by a space or tab.
    match chars.next() {
        Some(' ') | Some('\t') => Some(count),
        _ => None,
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn empty_document_returns_single_empty_section() {
        let sections = parse_sections("");
        assert_eq!(sections.len(), 1);
        assert_eq!(sections[0].heading, "");
        assert_eq!(sections[0].level, 0);
    }

    #[test]
    fn no_headings_returns_whole_document_as_body() {
        let doc = "Just some prose.\nNo headings here.\n";
        let sections = parse_sections(doc);
        assert_eq!(sections.len(), 1);
        assert_eq!(sections[0].level, 0);
        assert_eq!(sections[0].body, doc);
    }

    #[test]
    fn single_h2_section() {
        let doc = "## Overview\nSome content.\n";
        let sections = parse_sections(doc);
        assert_eq!(sections.len(), 1);
        assert_eq!(sections[0].heading, "## Overview");
        assert_eq!(sections[0].level, 2);
        assert!(sections[0].body.contains("Some content."));
    }

    #[test]
    fn multiple_sections_in_order() {
        let doc = "## Alpha\nalpha body\n## Beta\nbeta body\n## Gamma\ngamma body\n";
        let sections = parse_sections(doc);
        assert_eq!(sections.len(), 3);
        assert_eq!(sections[0].heading, "## Alpha");
        assert_eq!(sections[1].heading, "## Beta");
        assert_eq!(sections[2].heading, "## Gamma");
    }

    #[test]
    fn preamble_plus_sections() {
        let doc = "Preamble text.\n## Section One\nbody\n";
        let sections = parse_sections(doc);
        // Should have preamble + 1 section
        assert_eq!(sections.len(), 2);
        assert_eq!(sections[0].level, 0);
        assert!(sections[0].body.contains("Preamble text."));
        assert_eq!(sections[1].heading, "## Section One");
    }

    #[test]
    fn headings_inside_code_fence_ignored() {
        let doc = "## Real Heading\n```\n## Not a Heading\n```\n## Also Real\n";
        let sections = parse_sections(doc);
        assert_eq!(sections.len(), 2);
        assert_eq!(sections[0].heading, "## Real Heading");
        assert_eq!(sections[1].heading, "## Also Real");
        // The fenced block should be inside the body of "Real Heading".
        assert!(sections[0].body.contains("## Not a Heading"));
    }

    #[test]
    fn hash_without_space_is_not_heading() {
        let doc = "#hashtag\n#nospace\n## Real\nbody\n";
        let sections = parse_sections(doc);
        // The non-headings become a preamble section (level 0) + the real h2 section.
        assert_eq!(sections.len(), 2, "expect preamble + 1 real section");
        // First section is the preamble (level 0).
        assert_eq!(sections[0].level, 0);
        assert!(sections[0].body.contains("#hashtag"));
        // Second section is the real heading.
        assert_eq!(sections[1].heading, "## Real");
        assert!(sections[1].body.contains("body"));
    }

    #[test]
    fn mixed_heading_levels() {
        let doc = "# H1\ncontent\n## H2\nmore\n### H3\ndeep\n";
        let sections = parse_sections(doc);
        assert_eq!(sections.len(), 3);
        assert_eq!(sections[0].level, 1);
        assert_eq!(sections[1].level, 2);
        assert_eq!(sections[2].level, 3);
    }

    #[test]
    fn to_markdown_roundtrip_h2() {
        let s = Section {
            heading: "## Overview".to_string(),
            body: "Content here.\n".to_string(),
            level: 2,
        };
        let md = s.to_markdown();
        let re_parsed = parse_sections(&md);
        assert_eq!(re_parsed.len(), 1);
        assert_eq!(re_parsed[0].heading, "## Overview");
    }
}
