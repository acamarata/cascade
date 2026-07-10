//! File-merge helpers — build merged Markdown content and perform atomic writes.

use super::super::section_parser::Section;
use super::stamp::make_stamp;
use cascade_types::error::{CascadeError, Result};
use std::path::Path;

/// Build the final merged Markdown content.
///
/// Strategy:
/// 1. Start with the existing target content (may be empty).
/// 2. Apply forced replacements in-line (replace existing section body).
/// 3. Append new sections at the end.
/// 4. Append the stamp comment.
pub(super) fn build_merged_content(
    target: &str,
    to_append: &[Section],
    forced_replacements: &[(String, Section)],
    id: &str,
    version: &str,
) -> Result<String> {
    let mut result = target.to_string();

    // ── Apply forced replacements ────────────────────────────────────────
    for (heading, new_sec) in forced_replacements {
        result = replace_section_in_content(&result, heading, new_sec);
    }

    // Ensure the content ends with a newline before appending.
    if !result.is_empty() && !result.ends_with('\n') {
        result.push('\n');
    }

    // ── Append new sections ──────────────────────────────────────────────
    for sec in to_append {
        // Add a blank line separator if there's existing content.
        if !result.trim().is_empty() {
            result.push('\n');
        }
        result.push_str(&format!("{}\n", sec.heading));
        result.push_str(&sec.body);
    }

    // Ensure trailing newline before stamp.
    if !result.ends_with('\n') {
        result.push('\n');
    }

    // ── Append stamp ─────────────────────────────────────────────────────
    let now = chrono::Utc::now().format("%Y-%m-%dT%H:%M:%SZ").to_string();
    result.push_str(&format!("\n{}\n", make_stamp(id, version, &now)));

    Ok(result)
}

/// Replace a section's body in `content` identified by `heading`.
///
/// Finds the heading line, then replaces everything up to (but not including)
/// the next heading of the same or higher level with `new_sec.body`.
pub(super) fn replace_section_in_content(
    content: &str,
    heading: &str,
    new_sec: &Section,
) -> String {
    let lines: Vec<&str> = content.lines().collect();
    let mut result = String::new();
    let mut i = 0;
    let mut replaced = false;

    while i < lines.len() {
        if !replaced && lines[i] == heading {
            // Found the section heading.
            result.push_str(lines[i]);
            result.push('\n');
            i += 1;

            // Skip the old body lines.
            let sec_level = new_sec.level as usize;
            while i < lines.len() {
                let next_level = heading_level_of(lines[i]);
                if next_level > 0 && next_level <= sec_level {
                    break; // next heading at same or higher level
                }
                i += 1;
            }

            // Write the new body.
            result.push_str(&new_sec.body);
            if !new_sec.body.ends_with('\n') {
                result.push('\n');
            }
            replaced = true;
        } else {
            result.push_str(lines[i]);
            result.push('\n');
            i += 1;
        }
    }

    result
}

/// Return the heading level (1–6) of `line`, or 0 if it's not a heading.
pub(super) fn heading_level_of(line: &str) -> usize {
    let mut count = 0usize;
    let mut chars = line.chars().peekable();
    while chars.peek() == Some(&'#') {
        count += 1;
        chars.next();
        if count > 6 {
            return 0;
        }
    }
    if count == 0 {
        return 0;
    }
    match chars.next() {
        Some(' ') | Some('\t') => count,
        _ => 0,
    }
}

/// Write `content` to `path` atomically via a `.tmp` sibling + rename.
pub(super) fn atomic_write(path: &Path, content: &str) -> Result<()> {
    // Ensure parent directory exists.
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent)
            .map_err(|e| CascadeError::Other(format!("create dirs {}: {}", parent.display(), e)))?;
    }

    let tmp_path = path.with_extension("tmp");
    std::fs::write(&tmp_path, content)
        .map_err(|e| CascadeError::Other(format!("write tmp {}: {}", tmp_path.display(), e)))?;
    std::fs::rename(&tmp_path, path).map_err(|e| {
        CascadeError::Other(format!(
            "rename {} -> {}: {}",
            tmp_path.display(),
            path.display(),
            e
        ))
    })?;
    Ok(())
}
