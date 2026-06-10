// merge/writer.rs — Cascade content writer for the merge engine.
//
// Purpose: Persists approved merge sections to ~/.cascade/CASCADE.md.
//   Appends new sections using an atomic write pattern (write to .tmp then
//   rename). A wizard-end marker keeps successive appends clean. If the
//   target file already exists it is backed up first (CASCADE.md.bak) so
//   the operation is always non-destructive.
//
// Inputs:  tier (String), sections (Vec<ApprovedSection>).
// Outputs: WriteResult { path, bytes_written, section_count } or Err(String).
//
// Constraints:
//   - Target: ~/.cascade/CASCADE.md (global tier; project tiers deferred to P4).
//   - Creates ~/.cascade/ if it does not exist.
//   - HOME-confined: resolves ~ via $HOME env var.
//   - Append-only: never overwrites existing cascade content.
//   - Atomic write: write to <target>.tmp then std::fs::rename.
//   - Section format: ## {title}\n\n{content}\n\n
//   - Marker: <!-- cascade-wizard-end --> inserted after new sections.
//
// SPORT: MASTER-COMPONENTS.md — write_cascade_content / cascadeWriter — merge/writer.rs
// Task: T-P3-E03-26

use std::fs;
use std::path::PathBuf;

use crate::merge::types::{ApprovedSection, WriteResult};

/// Wizard-end marker. Subsequent appends insert new content before this line
/// so the file stays coherent across multiple wizard runs.
const WIZARD_END_MARKER: &str = "<!-- cascade-wizard-end -->";

/// Resolve the target path for the given tier.
///
/// Global tier → `~/.cascade/CASCADE.md`.
/// All other tier strings are treated as global in P3; project-tier paths are
/// deferred to P4.
fn resolve_target_path(tier: &str) -> Result<PathBuf, String> {
    let home = std::env::var("HOME")
        .map_err(|_| "HOME environment variable not set".to_string())?;

    // P3 only supports global tier.  Project-tier paths deferred to P4.
    let _ = tier; // tier reserved for P4 routing

    let dir = PathBuf::from(&home).join(".cascade");
    Ok(dir.join("CASCADE.md"))
}

/// Assemble the markdown block for a slice of approved sections.
///
/// Each section is rendered as:
/// ```text
/// ## {title}
///
/// {content}
///
/// ```
fn render_sections(sections: &[ApprovedSection]) -> String {
    let mut out = String::new();
    for section in sections {
        out.push_str("## ");
        out.push_str(&section.title);
        out.push_str("\n\n");
        out.push_str(&section.content);
        // Ensure exactly one blank line after content.
        if !section.content.ends_with('\n') {
            out.push('\n');
        }
        out.push('\n');
    }
    out
}

/// Write approved merge sections to `~/.cascade/CASCADE.md`.
///
/// # Behaviour
/// 1. Resolves the target path (HOME-confined; global tier only in P3).
/// 2. Creates `~/.cascade/` if absent.
/// 3. If the file already exists, copies it to `CASCADE.md.bak` (non-destructive).
/// 4. Reads existing content; locates the `<!-- cascade-wizard-end -->` marker
///    (if present) and splices new sections in before it.  If no marker exists
///    new sections are appended at the end.
/// 5. Appends the wizard-end marker after the new sections.
/// 6. Writes the assembled content to `<target>.tmp`, then renames atomically.
///
/// # Arguments
/// * `tier`     — cascade tier string (e.g. `"global"`).
/// * `sections` — user-approved sections to append.
///
/// # Returns
/// `Ok(WriteResult)` with the resolved path, final byte count, and section count.
pub fn write_cascade_content(
    tier: String,
    sections: Vec<ApprovedSection>,
) -> Result<WriteResult, String> {
    let target = resolve_target_path(&tier)?;

    // Ensure parent directory exists.
    if let Some(parent) = target.parent() {
        fs::create_dir_all(parent)
            .map_err(|e| format!("Failed to create directory {:?}: {}", parent, e))?;
    }

    // --- Read existing content (if any) and back it up ---
    let existing = if target.exists() {
        let content = fs::read_to_string(&target)
            .map_err(|e| format!("Failed to read {:?}: {}", target, e))?;

        // Backup: CASCADE.md.bak (overwrite previous backup — the real protection
        // is the atomic tmp+rename that keeps the original intact until success).
        let bak = target.with_extension("md.bak");
        fs::copy(&target, &bak)
            .map_err(|e| format!("Failed to backup {:?}: {}", target, e))?;

        content
    } else {
        String::new()
    };

    // --- Render new sections ---
    let new_block = render_sections(&sections);

    // --- Splice: insert before marker if present, else append ---
    let assembled = if let Some(marker_pos) = existing.find(WIZARD_END_MARKER) {
        // Keep everything before the marker, splice new block, re-add marker.
        let before = &existing[..marker_pos];
        // Strip any trailing whitespace/newlines before the marker for clean output.
        let before = before.trim_end_matches('\n');
        format!(
            "{}\n\n{}{}\n",
            before,
            new_block,
            WIZARD_END_MARKER
        )
    } else {
        // No marker yet: append new sections then add the marker.
        let base = if existing.is_empty() {
            String::new()
        } else {
            // Ensure we separate from existing content with a blank line.
            let trimmed = existing.trim_end_matches('\n');
            format!("{}\n\n", trimmed)
        };
        format!("{}{}{}\n", base, new_block, WIZARD_END_MARKER)
    };

    // --- Atomic write: tmp → rename ---
    let tmp_path = target.with_extension("md.tmp");
    fs::write(&tmp_path, &assembled)
        .map_err(|e| format!("Failed to write tmp file {:?}: {}", tmp_path, e))?;
    fs::rename(&tmp_path, &target)
        .map_err(|e| format!("Failed to rename {:?} -> {:?}: {}", tmp_path, target, e))?;

    let bytes_written = assembled.len() as u64;
    let section_count = sections.len();
    let path = target.to_string_lossy().to_string();

    Ok(WriteResult {
        path,
        bytes_written,
        section_count,
    })
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;
    use std::env;
    use tempfile::TempDir;

    /// Override HOME for test isolation: returns the TempDir (must be kept alive).
    fn isolate_home() -> TempDir {
        let tmp = TempDir::new().expect("tempdir");
        env::set_var("HOME", tmp.path());
        tmp
    }

    /// Build a simple ApprovedSection for tests.
    fn section(id: &str, title: &str, content: &str) -> ApprovedSection {
        ApprovedSection {
            id: id.to_string(),
            title: title.to_string(),
            content: content.to_string(),
        }
    }

    // -- fresh write ---------------------------------------------------------

    #[test]
    #[serial(global_env)]
    fn fresh_write_creates_file_and_returns_correct_result() {
        let _tmp = isolate_home();
        let sections = vec![section("s1", "Code Quality", "No `any` types.")];

        let result = write_cascade_content("global".to_string(), sections)
            .expect("write_cascade_content failed");

        // File must exist.
        let path = PathBuf::from(&result.path);
        assert!(path.exists(), "CASCADE.md not created");

        // section_count matches.
        assert_eq!(result.section_count, 1);

        // bytes_written is non-zero and matches the actual file.
        let on_disk = fs::metadata(&path).unwrap().len();
        assert_eq!(result.bytes_written, on_disk);

        // Content structure: H2 header present, marker present.
        let content = fs::read_to_string(&path).unwrap();
        assert!(content.contains("## Code Quality"), "H2 header missing");
        assert!(content.contains("No `any` types."), "section content missing");
        assert!(content.contains(WIZARD_END_MARKER), "wizard-end marker missing");
    }

    // -- existing-file backup -----------------------------------------------

    #[test]
    #[serial(global_env)]
    fn existing_file_is_backed_up_before_write() {
        let _tmp = isolate_home();
        let home = env::var("HOME").unwrap();
        let cascade_dir = PathBuf::from(&home).join(".cascade");
        fs::create_dir_all(&cascade_dir).unwrap();
        let target = cascade_dir.join("CASCADE.md");
        let bak = cascade_dir.join("CASCADE.md.bak");

        // Seed an existing file.
        fs::write(&target, "# Existing content\n").unwrap();
        assert!(!bak.exists(), "bak must not exist before write");

        write_cascade_content("global".to_string(), vec![section("s1", "New Section", "new")])
            .expect("write failed");

        // Backup created.
        assert!(bak.exists(), "CASCADE.md.bak not created");
        let bak_content = fs::read_to_string(&bak).unwrap();
        assert_eq!(bak_content, "# Existing content\n", "backup content mismatch");

        // Original file updated and contains both old and new content.
        let updated = fs::read_to_string(&target).unwrap();
        assert!(updated.contains("# Existing content"), "old content lost");
        assert!(updated.contains("## New Section"), "new section missing");
    }

    // -- section ordering ----------------------------------------------------

    #[test]
    #[serial(global_env)]
    fn sections_written_in_input_order() {
        let _tmp = isolate_home();
        let sections = vec![
            section("a", "Alpha", "First section."),
            section("b", "Beta", "Second section."),
            section("c", "Gamma", "Third section."),
        ];

        write_cascade_content("global".to_string(), sections).expect("write failed");

        let home = env::var("HOME").unwrap();
        let content = fs::read_to_string(
            PathBuf::from(&home).join(".cascade").join("CASCADE.md"),
        )
        .unwrap();

        let pos_alpha = content.find("## Alpha").expect("Alpha missing");
        let pos_beta = content.find("## Beta").expect("Beta missing");
        let pos_gamma = content.find("## Gamma").expect("Gamma missing");
        assert!(pos_alpha < pos_beta, "Alpha must precede Beta");
        assert!(pos_beta < pos_gamma, "Beta must precede Gamma");
    }

    // -- atomicity: no .tmp after success ------------------------------------

    #[test]
    #[serial(global_env)]
    fn tmp_file_absent_after_successful_write() {
        let _tmp = isolate_home();
        let home = env::var("HOME").unwrap();
        let tmp_path = PathBuf::from(&home)
            .join(".cascade")
            .join("CASCADE.md.tmp");

        write_cascade_content(
            "global".to_string(),
            vec![section("s1", "Atomicity", "verified.")],
        )
        .expect("write failed");

        assert!(!tmp_path.exists(), ".tmp file must be absent after rename");
    }

    // -- append respects marker ----------------------------------------------

    #[test]
    #[serial(global_env)]
    fn second_write_inserts_before_existing_marker() {
        let _tmp = isolate_home();

        // First write.
        write_cascade_content(
            "global".to_string(),
            vec![section("s1", "First", "content A.")],
        )
        .expect("first write failed");

        // Second write.
        write_cascade_content(
            "global".to_string(),
            vec![section("s2", "Second", "content B.")],
        )
        .expect("second write failed");

        let home = env::var("HOME").unwrap();
        let content = fs::read_to_string(
            PathBuf::from(&home).join(".cascade").join("CASCADE.md"),
        )
        .unwrap();

        // Both sections present.
        assert!(content.contains("## First"), "First section missing");
        assert!(content.contains("## Second"), "Second section missing");

        // Only one marker.
        let marker_count = content.matches(WIZARD_END_MARKER).count();
        assert_eq!(marker_count, 1, "expected exactly one wizard-end marker, got {}", marker_count);

        // First section before Second section.
        let pos_first = content.find("## First").unwrap();
        let pos_second = content.find("## Second").unwrap();
        assert!(pos_first < pos_second, "First must precede Second");

        // Marker at end (after both sections).
        let pos_marker = content.find(WIZARD_END_MARKER).unwrap();
        assert!(pos_second < pos_marker, "Second section must precede marker");
    }

    // -- section_count matches input -----------------------------------------

    #[test]
    #[serial(global_env)]
    fn section_count_matches_input_length() {
        let _tmp = isolate_home();
        let sections = vec![
            section("a", "One", "c1"),
            section("b", "Two", "c2"),
            section("c", "Three", "c3"),
            section("d", "Four", "c4"),
        ];
        let result = write_cascade_content("global".to_string(), sections)
            .expect("write failed");
        assert_eq!(result.section_count, 4);
    }
}
