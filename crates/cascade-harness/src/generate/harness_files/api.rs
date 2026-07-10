//! Public API — generate, inject, and init harness instruction files.

use std::fs;
use std::path::{Path, PathBuf};

use cascade_core::cascade_resolution::ResolvedCascade;
use cascade_core::pbd::active_work::ActiveWorkBlock;
use cascade_types::error::{CascadeError, Result};

use super::super::safe_write::{atomic_write_content, snapshot_files};
use super::constants::{
    ACTIVE_WORK_BEGIN, ACTIVE_WORK_END, UNIFIED_HARNESS_MARKER, UNIFIED_HARNESS_MARKER_BASE,
};
use super::kind::HarnessKind;
use super::render::render_harness_file;

/// Generate harness-native instruction files from a resolved cascade.
///
/// Before writing, all target files that already exist are captured into a
/// timestamped snapshot under `<workspace>/.cascade/snapshots/` so they can
/// be restored with `cascade snapshot restore` if something goes wrong.
///
/// Each generated file is written atomically (temp-file + rename) and carries
/// a content hash in the marker line so hand-edits are detectable.
///
/// # Arguments
/// * `resolved`    — fully merged cascade (supplies merged_instructions + mcp_server_url).
/// * `workspace`   — directory to write the output files into.
/// * `harnesses`   — list of harnesses to generate for (pass `HarnessKind::ALL` for all).
/// * `dry_run`     — if true, print planned writes without touching the filesystem.
///
/// # Returns
/// List of paths that were written (empty in dry-run mode).
pub fn generate_for_harnesses(
    resolved: &ResolvedCascade,
    workspace: &Path,
    harnesses: &[HarnessKind],
    dry_run: bool,
) -> Result<Vec<PathBuf>> {
    // Determine which targets need writing (skip files with our marker).
    let mut to_write: Vec<(HarnessKind, PathBuf, String)> = Vec::new();

    for &harness in harnesses {
        let dest = workspace.join(harness.output_filename());
        let content = render_harness_file(harness, resolved);

        if dest.exists() {
            let existing = fs::read_to_string(&dest).map_err(|e| CascadeError::Io {
                path: dest.clone(),
                operation: "read existing harness file",
                source: e,
            })?;
            if existing.contains(UNIFIED_HARNESS_MARKER_BASE) {
                if dry_run {
                    println!(
                        "[dry-run] {} ({}): cascade marker present, skip",
                        dest.display(),
                        harness.id()
                    );
                }
                continue;
            }
        }

        to_write.push((harness, dest, content));
    }

    if to_write.is_empty() {
        return Ok(Vec::new());
    }

    // Snapshot all targets that currently exist before overwriting any of them.
    if !dry_run {
        let snap_paths: Vec<PathBuf> = to_write
            .iter()
            .map(|(_, dest, _)| dest.clone())
            .filter(|p| p.exists())
            .collect();
        if !snap_paths.is_empty() {
            match snapshot_files(workspace, &snap_paths) {
                Ok(Some(snap_dir)) => {
                    println!("snapshot: {}", snap_dir.display());
                }
                Ok(None) => {}
                Err(e) => {
                    // Snapshot failure is non-fatal: warn and continue.
                    eprintln!("warning: snapshot failed (continuing): {e}");
                }
            }
        }
    }

    let mut written = Vec::new();

    for (harness, dest, content) in to_write {
        if dry_run {
            println!(
                "[dry-run] would write {} ({}) — {} bytes",
                dest.display(),
                harness.id(),
                content.len()
            );
        } else {
            atomic_write_content(&dest, &content, workspace)?;
            println!("wrote: {} ({})", dest.display(), harness.id());
            written.push(dest);
        }
    }

    Ok(written)
}

/// Inject or replace the active-work section in a harness instruction file.
///
/// Finds the `<!-- cascade:active-work-begin -->` … `<!-- cascade:active-work-end -->`
/// delimiters and replaces the block with the current active-work content.
/// If no delimiters are present, appends the section to the end of the file.
///
/// When `active_work.is_empty()`, the section is removed if present (idempotent).
///
/// # Arguments
/// * `dest`         — path to the existing harness instruction file.
/// * `active_work`  — the built active-work block.
/// * `dry_run`      — if true, print planned changes without writing.
///
/// # Returns
/// `true` if the file was modified, `false` if no change was needed.
pub fn inject_active_work_section(
    dest: &Path,
    active_work: &ActiveWorkBlock,
    dry_run: bool,
) -> Result<bool> {
    // Build the replacement block (delimited so we can find it on re-runs).
    let block_text = if active_work.is_empty() {
        String::new()
    } else {
        format!(
            "\n{begin}\n{body}{end}\n",
            begin = ACTIVE_WORK_BEGIN,
            body = active_work.text(),
            end = ACTIVE_WORK_END,
        )
    };

    if !dest.exists() {
        // Nothing to inject into — skip silently.
        return Ok(false);
    }

    let existing = fs::read_to_string(dest).map_err(|e| CascadeError::Io {
        path: dest.to_path_buf(),
        operation: "read harness file for active-work injection",
        source: e,
    })?;

    let new_content = replace_active_work_block(&existing, &block_text);

    if new_content == existing {
        return Ok(false); // no change
    }

    if dry_run {
        println!(
            "[dry-run] would inject active-work section into {} ({} bytes)",
            dest.display(),
            block_text.len()
        );
        return Ok(false);
    }

    // Use the dest's own parent as the workspace context for symlink checks.
    let ws = dest.parent().unwrap_or(dest);
    atomic_write_content(dest, &new_content, ws)?;
    Ok(true)
}

/// Replace (or append) the active-work delimited block within `content`.
///
/// - If both delimiters exist: replaces everything between them (inclusive).
/// - If no delimiters exist and `block_text` is non-empty: appends.
/// - If no delimiters exist and `block_text` is empty: returns `content` unchanged.
fn replace_active_work_block(content: &str, block_text: &str) -> String {
    if let (Some(begin_pos), Some(end_pos)) = (
        content.find(ACTIVE_WORK_BEGIN),
        content.find(ACTIVE_WORK_END),
    ) {
        if begin_pos <= end_pos {
            let end_of_end = end_pos + ACTIVE_WORK_END.len();
            // Remove leading newline before begin marker if present
            let trim_start =
                if begin_pos > 0 && content.as_bytes().get(begin_pos - 1) == Some(&b'\n') {
                    begin_pos - 1
                } else {
                    begin_pos
                };
            let mut result = content[..trim_start].to_string();
            result.push_str(block_text);
            // Preserve content after the end marker
            let after = &content[end_of_end..];
            result.push_str(after);
            return result;
        }
    }

    // No existing section — append if non-empty
    if block_text.is_empty() {
        return content.to_string();
    }

    let mut result = content.trim_end().to_string();
    result.push('\n');
    result.push_str(block_text);
    result
}

/// Write a single merged file containing the full cascade body.
///
/// The file begins with a harness-index header listing which harnesses were
/// generated, followed by the merged instruction body.
///
/// # Arguments
/// * `resolved`    — fully merged cascade.
/// * `dest`        — absolute path to write the file to.
/// * `harnesses`   — harnesses listed in the header (informational).
/// * `dry_run`     — if true, print planned write without touching the filesystem.
pub fn write_single_file(
    resolved: &ResolvedCascade,
    dest: &Path,
    harnesses: &[HarnessKind],
    dry_run: bool,
) -> Result<()> {
    let harness_list = harnesses
        .iter()
        .map(|h| h.id())
        .collect::<Vec<_>>()
        .join(", ");

    let content = format!(
        "{marker}\n\
         <!-- cascade:single-file harnesses=[{harnesses}] -->\n\
         \n\
         # Cascade Unified Instructions\n\
         \n\
         This file contains the merged Cascade instruction payload for:\n\
         **{harnesses}**\n\
         \n\
         Copy the body below into the appropriate harness-native file.\n\
         \n\
         ---\n\
         \n\
         {body}\n",
        marker = UNIFIED_HARNESS_MARKER,
        harnesses = harness_list,
        body = resolved.merged_instructions.trim(),
    );

    if dry_run {
        println!(
            "[dry-run] would write single-file {} — {} bytes",
            dest.display(),
            content.len()
        );
        return Ok(());
    }

    let ws = dest.parent().unwrap_or(dest);
    atomic_write_content(dest, &content, ws)?;
    println!("wrote single-file: {}", dest.display());
    Ok(())
}

/// Autodetect installed harnesses and scaffold their instruction files.
///
/// Idempotent: files that already contain the cascade marker are skipped.
/// Returns the list of harnesses that were scaffolded.
///
/// # Arguments
/// * `resolved`    — fully merged cascade.
/// * `workspace`   — working directory (used both for detection and as write dest).
/// * `dry_run`     — if true, print planned operations without touching the filesystem.
pub fn init_from_installed(
    resolved: &ResolvedCascade,
    workspace: &Path,
    dry_run: bool,
) -> Result<Vec<HarnessKind>> {
    let detected: Vec<HarnessKind> = HarnessKind::ALL
        .iter()
        .copied()
        .filter(|h| h.is_installed(workspace))
        .collect();

    if detected.is_empty() {
        if dry_run {
            println!("[dry-run] init-from-installed: no harnesses detected");
        } else {
            println!("No harnesses detected. Install claude, opencode, codex, cursor, or aider.");
        }
        return Ok(Vec::new());
    }

    if dry_run {
        println!(
            "[dry-run] init-from-installed: detected harnesses: {}",
            detected
                .iter()
                .map(|h| h.id())
                .collect::<Vec<_>>()
                .join(", ")
        );
    } else {
        println!(
            "init-from-installed: detected {} harness(es): {}",
            detected.len(),
            detected
                .iter()
                .map(|h| h.id())
                .collect::<Vec<_>>()
                .join(", ")
        );
    }

    generate_for_harnesses(resolved, workspace, &detected, dry_run)?;
    Ok(detected)
}
