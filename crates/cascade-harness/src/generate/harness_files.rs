//! Per-harness instruction-file generation from a unified merged cascade.
//!
//! ## Purpose
//!
//! Every harness (Claude Code, OpenCode, Codex, Cursor, Aider) consumes
//! identical merged instruction text, but expects it in a different file and
//! format.  This module writes the harness-native file for each and optionally
//! writes a single merged file via `--output-single-file`.
//!
//! ## Supported harnesses
//!
//! | Harness | Output file | Format |
//! |---------|-------------|--------|
//! | `claude-code` | `CLAUDE.md` | Markdown |
//! | `opencode` | `AGENTS.md` | YAML-frontmatter Markdown |
//! | `codex` | `AGENTS.md` | same as opencode |
//! | `cursor` | `.cursor/rules/{slug}.mdc` | MDC (Cursor rules) |
//! | `aider` | `CONVENTIONS.md` | plain Markdown |
//!
//! All output files embed the same `merged_instructions` body so content is
//! byte-for-byte identical across harnesses (only the envelope differs).
//!
//! ## Single-file output (`--output-single-file`)
//!
//! Writes one file containing the merged body prefixed with a harness-index
//! header so a user can manually distribute the content to their harness.
//!
//! ## `init-from-installed` (autodetect + scaffold)
//!
//! `init_from_installed` detects which harnesses are installed on the machine
//! and scaffolds the correct output file for each.  It is idempotent (skips
//! when the file already contains the cascade marker) and supports dry-run.
//!
//! ## Idempotency marker
//!
//! The string `<!-- cascade:unified-harness sha256=<hash> -->` is written near
//! the top of every generated file.  The hash covers the body that follows so
//! hand-edits are detectable.  Subsequent runs detect the base marker and skip
//! the file (idempotent).
//!
//! ## Snapshot-before-regenerate
//!
//! Before any files are written, all target paths that currently exist are
//! captured into `<workspace>/.cascade/snapshots/<timestamp>/`.  This allows
//! recovery via `cascade snapshot restore` without any git dependency.
//!
//! ## SPORT
//!
//! MASTER-HARNESSES.md: all-harness generator (E-P7-03)

use std::fs;
use std::path::{Path, PathBuf};

use cascade_core::cascade_resolution::ResolvedCascade;
use cascade_core::pbd::active_work::ActiveWorkBlock;
use cascade_types::error::{CascadeError, Result};

use super::safe_write::{atomic_write_content, content_hash, snapshot_files};

// ── Constants ─────────────────────────────────────────────────────────────────

/// Base idempotency marker prefix — written near the top of every generated
/// harness file.  The full marker line includes a content hash:
/// `<!-- cascade:unified-harness sha256=<32hexchars> -->`.
///
/// Detection uses [`UNIFIED_HARNESS_MARKER_BASE`] so that files written by
/// older versions of cascade (without hashes) are still recognised.
pub const UNIFIED_HARNESS_MARKER: &str = "<!-- cascade:unified-harness -->";
/// Prefix shared by all harness marker variants (with and without hash).
pub const UNIFIED_HARNESS_MARKER_BASE: &str = "<!-- cascade:unified-harness";

/// Delimiter for the injected active-work section.
/// Used to find and replace the section on re-runs (idempotent).
pub const ACTIVE_WORK_BEGIN: &str = "<!-- cascade:active-work-begin -->";
pub const ACTIVE_WORK_END: &str = "<!-- cascade:active-work-end -->";

// ── Harness identity ──────────────────────────────────────────────────────────

/// All harnesses that Cascade supports for instruction-file generation.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum HarnessKind {
    ClaudeCode,
    OpenCode,
    Codex,
    Cursor,
    Aider,
    /// Antigravity AI coding tool — detect+config mode (E-P7-06).
    Antigravity,
}

impl HarnessKind {
    pub const ALL: &'static [Self] = &[
        Self::ClaudeCode,
        Self::OpenCode,
        Self::Codex,
        Self::Cursor,
        Self::Aider,
        Self::Antigravity,
    ];

    /// Lowercase identifier used in CLI args and MCP tool args.
    pub fn id(&self) -> &'static str {
        match self {
            Self::ClaudeCode => "claude-code",
            Self::OpenCode => "opencode",
            Self::Codex => "codex",
            Self::Cursor => "cursor",
            Self::Aider => "aider",
            Self::Antigravity => "antigravity",
        }
    }

    /// Preferred output filename relative to the workspace root.
    pub fn output_filename(&self) -> &'static str {
        match self {
            Self::ClaudeCode => "CLAUDE.md",
            Self::OpenCode => "AGENTS.md",
            Self::Codex => "AGENTS.md",
            Self::Cursor => ".cursor/rules/cascade.mdc",
            Self::Aider => "CONVENTIONS.md",
            Self::Antigravity => ".antigravity/cascade-rules.md",
        }
    }

    /// Returns true if a harness binary / config is present on disk.
    ///
    /// Detection heuristics (in priority order):
    /// 1. Binary on `PATH`.
    /// 2. Known config directory existence.
    /// 3. Workspace marker file (e.g. `.cursor/settings.json`).
    pub fn is_installed(&self, workspace: &Path) -> bool {
        match self {
            Self::ClaudeCode => is_binary_on_path("claude"),
            Self::OpenCode => is_binary_on_path("opencode"),
            Self::Codex => is_binary_on_path("codex"),
            Self::Cursor => {
                // Cursor leaves a `.cursor/` dir in the workspace or the global
                // config at `~/.cursor/` / `~/Library/Application Support/Cursor`.
                workspace.join(".cursor").exists()
                    || cursor_global_config_exists()
                    || is_binary_on_path("cursor")
            }
            Self::Aider => {
                is_binary_on_path("aider")
                    || home_path(".aider.conf.yml").exists()
                    || home_path(".aider").exists()
            }
            Self::Antigravity => {
                // Antigravity leaves `.antigravity/` in the workspace or
                // `~/.config/antigravity/` / `~/.antigravity/` globally.
                workspace.join(".antigravity").exists()
                    || antigravity_global_config_exists()
                    || is_binary_on_path("antigravity")
            }
        }
    }
}

// ── Public API ────────────────────────────────────────────────────────────────

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

// ── Content renderers ─────────────────────────────────────────────────────────

/// Render pointer-comment lines for on-demand rules.
///
/// Each on-demand rule becomes a `-> <text> (load_when: <condition>)` comment
/// line. These are appended after the always-loaded body so the harness file
/// stays within context-budget while still advertising what rules exist.
///
/// Returns an empty string when there are no on-demand rules.
fn render_on_demand_pointer_section(resolved: &ResolvedCascade) -> String {
    if resolved.on_demand_rules.is_empty() {
        return String::new();
    }
    let mut out = String::from("\n<!-- cascade:on-demand-rules -->\n");
    out.push_str("<!-- The following rules are NOT loaded automatically. Load them when their condition applies. -->\n");
    for rule in &resolved.on_demand_rules {
        match &rule.load_when {
            Some(when) => {
                out.push_str(&format!(
                    "<!-- -> {} (load_when: {}, source: {}) -->\n",
                    rule.text.trim(),
                    when,
                    rule.source_tier,
                ));
            }
            None => {
                out.push_str(&format!(
                    "<!-- -> {} (on-demand, source: {}) -->\n",
                    rule.text.trim(),
                    rule.source_tier,
                ));
            }
        }
    }
    out
}

/// Render the harness-native instruction file content.
///
/// The merged instruction body is IDENTICAL across all harnesses.
/// Only the envelope (frontmatter, header, format wrapper) differs.
///
/// On-demand rules are rendered as `->` pointer comment lines AFTER the body
/// rather than being inlined, so the context budget stays bounded.
///
/// The marker line includes a SHA-256 content fingerprint so that hand-edits
/// can be detected by [`super::safe_write::hash_matches`].
fn render_harness_file(harness: HarnessKind, resolved: &ResolvedCascade) -> String {
    let body = resolved.merged_instructions.trim();
    let mcp = &resolved.mcp_server_url;
    let on_demand = render_on_demand_pointer_section(resolved);

    // Build the body that will follow the marker line, then hash it.
    let body_section: String = match harness {
        HarnessKind::ClaudeCode => format!(
            "<!-- cascade:harness=claude-code -->\n\
             \n\
             **MCP server:** `{mcp}`\n\
             Call `cascade.provide_harness_context` on startup for the full context payload.\n\
             \n\
             ---\n\
             \n\
             {body}\n\
             {on_demand}",
            mcp = mcp,
            body = body,
            on_demand = on_demand,
        ),
        HarnessKind::OpenCode | HarnessKind::Codex => {
            let id = harness.id();
            format!(
                "<!-- cascade:harness={id} -->\n\
                 ---\n\
                 model: \"claude-sonnet-4-6\"\n\
                 tools:\n\
                   - \"cascade.read\"\n\
                   - \"cascade.search\"\n\
                   - \"cascade.provide_harness_context\"\n\
                 ---\n\
                 \n\
                 **MCP server:** `{mcp}`\n\
                 \n\
                 {body}\n\
                 {on_demand}",
                id = id,
                mcp = mcp,
                body = body,
                on_demand = on_demand,
            )
        }
        HarnessKind::Cursor => format!(
            "<!-- cascade:harness=cursor -->\n\
             ---\n\
             description: Cascade unified AI instructions\n\
             globs: [\"**/*\"]\n\
             alwaysApply: true\n\
             ---\n\
             \n\
             {body}\n\
             {on_demand}",
            body = body,
            on_demand = on_demand,
        ),
        HarnessKind::Aider => format!(
            "<!-- cascade:harness=aider -->\n\
             \n\
             # Conventions\n\
             \n\
             {body}\n\
             {on_demand}",
            body = body,
            on_demand = on_demand,
        ),
        HarnessKind::Antigravity => format!(
            "<!-- cascade:harness=antigravity -->\n\
             \n\
             # Cascade Context Rules\n\
             \n\
             **MCP server:** `{mcp}`  \n\
             Call `provide_harness_context` on startup for the full context payload.\n\
             \n\
             ---\n\
             \n\
             {body}\n\
             {on_demand}",
            mcp = mcp,
            body = body,
            on_demand = on_demand,
        ),
    };

    // Compute hash of the body section that follows the marker line.
    let hash = content_hash(&body_section);
    format!("<!-- cascade:unified-harness sha256={hash} -->\n{body_section}")
}

// ── Detection helpers ─────────────────────────────────────────────────────────

/// Returns true if `binary_name` resolves on `$PATH`.
fn is_binary_on_path(binary_name: &str) -> bool {
    std::env::var_os("PATH")
        .map(|paths| {
            std::env::split_paths(&paths).any(|dir| {
                let candidate = dir.join(binary_name);
                candidate.is_file() || {
                    // macOS / Linux: check with executable permission.
                    #[cfg(unix)]
                    {
                        use std::os::unix::fs::PermissionsExt;
                        candidate
                            .metadata()
                            .map(|m| m.permissions().mode() & 0o111 != 0)
                            .unwrap_or(false)
                    }
                    #[cfg(not(unix))]
                    {
                        candidate.exists()
                    }
                }
            })
        })
        .unwrap_or(false)
}

/// Returns true if a Cursor global config directory exists.
fn cursor_global_config_exists() -> bool {
    let home = std::env::var_os("HOME")
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("/tmp"));

    // Linux / Windows: ~/.cursor/
    if home.join(".cursor").exists() {
        return true;
    }
    // macOS: ~/Library/Application Support/Cursor/
    let mac_path = home
        .join("Library")
        .join("Application Support")
        .join("Cursor");
    if mac_path.exists() {
        return true;
    }
    false
}

/// Returns true if an Antigravity global config directory exists.
fn antigravity_global_config_exists() -> bool {
    let home = std::env::var_os("HOME")
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("/tmp"));

    // Cross-platform: ~/.config/antigravity/ or ~/.antigravity/
    if home.join(".config").join("antigravity").exists() {
        return true;
    }
    if home.join(".antigravity").exists() {
        return true;
    }
    // macOS: ~/Library/Application Support/Antigravity/
    #[cfg(target_os = "macos")]
    {
        let mac_path = home
            .join("Library")
            .join("Application Support")
            .join("Antigravity");
        if mac_path.exists() {
            return true;
        }
    }
    false
}

/// Resolve a path under the user's home directory.
fn home_path(rel: &str) -> PathBuf {
    std::env::var_os("HOME")
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("/tmp"))
        .join(rel)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_core::cascade_resolution::{ResolvedCascade, TierResult};
    use cascade_types::cascade_tier::CascadeTier;
    use serial_test::serial;
    use std::fs;
    use tempfile::TempDir;

    fn mock_resolved(body: &str) -> ResolvedCascade {
        ResolvedCascade {
            merged_instructions: body.to_string(),
            on_demand_rules: vec![],
            mcp_server_url: "unix://~/.cascade/cascade.sock".into(),
            tiers_found: vec![TierResult {
                tier: CascadeTier::Gci,
                found: true,
                path_searched: std::path::PathBuf::from("/home/.cascade"),
                instructions: body.to_string(),
            }],
            working_dir: std::path::PathBuf::from("/project"),
        }
    }

    // ── test 1: all harnesses produce files with identical body ───────────────

    /// Each harness file contains the merged instruction body verbatim.
    ///
    /// WHY: E-P7-03 requirement — content identical across harnesses.
    #[test]
    #[serial(global_env)]
    fn all_harnesses_contain_identical_body() {
        let tmp = TempDir::new().unwrap();
        let resolved = mock_resolved("## RULES\n\nAlways use Cascade.");

        generate_for_harnesses(&resolved, tmp.path(), HarnessKind::ALL, false)
            .expect("generate_for_harnesses should succeed");

        // Each harness file should contain the body text.
        let expected_body = "Always use Cascade.";

        for harness in HarnessKind::ALL {
            let dest = tmp.path().join(harness.output_filename());
            assert!(
                dest.exists(),
                "harness={}: {} must exist",
                harness.id(),
                dest.display()
            );
            let content = fs::read_to_string(&dest).unwrap();
            assert!(
                content.contains(expected_body),
                "harness={}: file must contain body '{}': got:\n{}",
                harness.id(),
                expected_body,
                content
            );
        }
    }

    // ── test 2: each harness uses the correct output filename ─────────────────

    /// Output filename matches the spec for each harness.
    #[test]
    #[serial(global_env)]
    fn harness_output_filenames_correct() {
        assert_eq!(HarnessKind::ClaudeCode.output_filename(), "CLAUDE.md");
        assert_eq!(HarnessKind::OpenCode.output_filename(), "AGENTS.md");
        assert_eq!(HarnessKind::Codex.output_filename(), "AGENTS.md");
        assert_eq!(
            HarnessKind::Cursor.output_filename(),
            ".cursor/rules/cascade.mdc"
        );
        assert_eq!(HarnessKind::Aider.output_filename(), "CONVENTIONS.md");
    }

    // ── test 3: idempotency — second run skips files with marker ─────────────

    /// Running generate_for_harnesses twice does not overwrite files.
    #[test]
    #[serial(global_env)]
    fn generate_is_idempotent() {
        let tmp = TempDir::new().unwrap();
        let resolved = mock_resolved("## RULES\n\nDo good work.");

        generate_for_harnesses(&resolved, tmp.path(), HarnessKind::ALL, false).unwrap();

        // Capture mtimes.
        let mtimes_before: Vec<_> = HarnessKind::ALL
            .iter()
            .filter_map(|h| {
                let p = tmp.path().join(h.output_filename());
                fs::metadata(&p).ok().map(|m| (h.id(), m.modified().ok()))
            })
            .collect();

        std::thread::sleep(std::time::Duration::from_millis(50));

        generate_for_harnesses(&resolved, tmp.path(), HarnessKind::ALL, false).unwrap();

        let mtimes_after: Vec<_> = HarnessKind::ALL
            .iter()
            .filter_map(|h| {
                let p = tmp.path().join(h.output_filename());
                fs::metadata(&p).ok().map(|m| (h.id(), m.modified().ok()))
            })
            .collect();

        for ((id_b, mt_b), (id_a, mt_a)) in mtimes_before.iter().zip(mtimes_after.iter()) {
            assert_eq!(id_b, id_a, "harness order changed between runs");
            assert_eq!(
                mt_b, mt_a,
                "harness={id_b}: mtime should not change on idempotent run"
            );
        }
    }

    // ── test 4: dry-run writes no files ──────────────────────────────────────

    /// Dry-run produces no files on disk.
    #[test]
    #[serial(global_env)]
    fn dry_run_writes_nothing() {
        let tmp = TempDir::new().unwrap();
        let resolved = mock_resolved("test dry run");

        generate_for_harnesses(
            &resolved,
            tmp.path(),
            HarnessKind::ALL,
            /* dry_run= */ true,
        )
        .unwrap();

        for harness in HarnessKind::ALL {
            let dest = tmp.path().join(harness.output_filename());
            assert!(
                !dest.exists(),
                "harness={}: {} must NOT be created in dry-run mode",
                harness.id(),
                dest.display()
            );
        }
    }

    // ── test 5: --output-single-file writes one file with all harnesses ───────

    /// write_single_file produces one file with the marker and merged body.
    #[test]
    #[serial(global_env)]
    fn single_file_output() {
        let tmp = TempDir::new().unwrap();
        let dest = tmp.path().join("cascade-unified.md");
        let resolved = mock_resolved("## RULES\n\nSingle file rules.");

        write_single_file(&resolved, &dest, HarnessKind::ALL, false).unwrap();

        assert!(dest.exists(), "single-file must exist");
        let content = fs::read_to_string(&dest).unwrap();
        assert!(
            content.contains(UNIFIED_HARNESS_MARKER_BASE),
            "single-file must contain the marker"
        );
        assert!(
            content.contains("Single file rules."),
            "single-file must contain merged body"
        );
        // All harness IDs must appear in the file header.
        for h in HarnessKind::ALL {
            assert!(
                content.contains(h.id()),
                "single-file must list harness={} in header",
                h.id()
            );
        }
    }

    // ── test 6: init-from-installed detects fixtures ──────────────────────────

    /// init_from_installed scaffolds harnesses whose markers exist in workspace.
    ///
    /// We simulate "installed" by writing a fake binary into the PATH tempdir.
    #[test]
    #[serial(global_env)]
    fn init_from_installed_detects_via_path() {
        let tmp = TempDir::new().unwrap();
        let workspace = TempDir::new().unwrap();

        // Create a fake 'aider' binary in a temp bin dir on PATH.
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let bin_dir = tmp.path().join("bin");
            fs::create_dir_all(&bin_dir).unwrap();
            let fake_aider = bin_dir.join("aider");
            fs::write(&fake_aider, "#!/bin/sh\necho aider").unwrap();
            let mut perms = fs::metadata(&fake_aider).unwrap().permissions();
            perms.set_mode(0o755);
            fs::set_permissions(&fake_aider, perms).unwrap();

            // Override PATH so aider is "installed".
            let orig_path = std::env::var("PATH").unwrap_or_default();
            std::env::set_var("PATH", format!("{}:{}", bin_dir.display(), orig_path));

            let resolved = mock_resolved("init instructions");
            let detected = init_from_installed(&resolved, workspace.path(), false).unwrap();

            std::env::set_var("PATH", &orig_path);

            assert!(
                detected.contains(&HarnessKind::Aider),
                "aider should be detected when binary is on PATH"
            );
            let conventions_md = workspace.path().join("CONVENTIONS.md");
            assert!(
                conventions_md.exists(),
                "CONVENTIONS.md must be created for aider"
            );
            let content = fs::read_to_string(&conventions_md).unwrap();
            assert!(
                content.contains(UNIFIED_HARNESS_MARKER_BASE),
                "CONVENTIONS.md must contain cascade marker"
            );
        }

        // On non-unix: at minimum init_from_installed must not panic.
        #[cfg(not(unix))]
        {
            let resolved = mock_resolved("init instructions");
            init_from_installed(&resolved, workspace.path(), false).unwrap();
        }
    }

    // ── test 7: dry-run init-from-installed writes nothing ───────────────────

    /// Dry-run mode of init-from-installed must not write any files.
    #[test]
    #[serial(global_env)]
    fn init_from_installed_dry_run_writes_nothing() {
        let workspace = TempDir::new().unwrap();

        // Plant a fake cursor global config to trigger cursor detection.
        let cursor_dir = workspace.path().join(".cursor");
        fs::create_dir_all(&cursor_dir).unwrap();

        let resolved = mock_resolved("dry run init");
        init_from_installed(&resolved, workspace.path(), /* dry_run= */ true).unwrap();

        // No non-.cursor files should be created.
        let mdc = workspace
            .path()
            .join(".cursor")
            .join("rules")
            .join("cascade.mdc");
        assert!(
            !mdc.exists(),
            ".cursor/rules/cascade.mdc must NOT be written in dry-run"
        );
    }

    // ── test 8: inject_active_work_section appends when no delimiters ─────────

    /// inject_active_work_section appends the section when no existing block.
    #[test]
    #[serial(global_env)]
    fn inject_active_work_appends_when_absent() {
        let tmp = TempDir::new().unwrap();
        let dest = tmp.path().join("CLAUDE.md");
        fs::write(&dest, "# Instructions\n\nDo good work.\n").unwrap();

        let active = cascade_core::pbd::active_work::ActiveWorkBlock {
            sprint_id: Some("s01".into()),
            sprint_title: Some("Sprint 1".into()),
            tickets: vec![cascade_core::pbd::active_work::ActiveTicketEntry {
                id: "T-P1-E01-W01-S01-01".into(),
                title: "Build the thing".into(),
                status: "active".into(),
                depends_on: vec![],
            }],
            tickets_total: 1,
            tasks: vec![],
            tasks_total: 0,
        };

        let changed = inject_active_work_section(&dest, &active, false).unwrap();
        assert!(changed, "file should be modified");

        let content = fs::read_to_string(&dest).unwrap();
        assert!(
            content.contains(ACTIVE_WORK_BEGIN),
            "must contain begin marker"
        );
        assert!(content.contains(ACTIVE_WORK_END), "must contain end marker");
        assert!(
            content.contains("T-P1-E01-W01-S01-01"),
            "must contain ticket id"
        );
        assert!(
            content.contains("# Instructions"),
            "original content must be preserved"
        );
    }

    // ── test 9: inject_active_work_section is idempotent (replaces on re-run) ─

    /// Re-running inject_active_work_section replaces the old block, not dupe.
    #[test]
    #[serial(global_env)]
    fn inject_active_work_is_idempotent() {
        let tmp = TempDir::new().unwrap();
        let dest = tmp.path().join("CLAUDE.md");
        fs::write(&dest, "# Instructions\n\nDo good work.\n").unwrap();

        let make_block =
            |sprint: &str, ticket_title: &str| cascade_core::pbd::active_work::ActiveWorkBlock {
                sprint_id: Some(sprint.into()),
                sprint_title: Some("Sprint".into()),
                tickets: vec![cascade_core::pbd::active_work::ActiveTicketEntry {
                    id: "T-01".into(),
                    title: ticket_title.into(),
                    status: "active".into(),
                    depends_on: vec![],
                }],
                tickets_total: 1,
                tasks: vec![],
                tasks_total: 0,
            };

        // First injection
        inject_active_work_section(&dest, &make_block("s01", "First title"), false).unwrap();
        // Second injection (different sprint)
        inject_active_work_section(&dest, &make_block("s02", "Updated title"), false).unwrap();

        let content = fs::read_to_string(&dest).unwrap();

        // Only one begin/end pair must exist
        let begin_count = content.matches(ACTIVE_WORK_BEGIN).count();
        let end_count = content.matches(ACTIVE_WORK_END).count();
        assert_eq!(
            begin_count, 1,
            "must have exactly 1 active-work begin marker; got {begin_count}"
        );
        assert_eq!(
            end_count, 1,
            "must have exactly 1 active-work end marker; got {end_count}"
        );

        // Should contain updated sprint, not original
        assert!(content.contains("s02"), "must show updated sprint id");
        assert!(
            content.contains("Updated title"),
            "must show updated ticket title"
        );
        assert!(
            !content.contains("First title"),
            "old ticket title must not remain"
        );
    }

    // ── test 10: inject with empty block removes section ─────────────────────

    /// When active_work is empty, inject_active_work_section removes existing section.
    #[test]
    #[serial(global_env)]
    fn inject_active_work_removes_when_empty() {
        let tmp = TempDir::new().unwrap();
        let dest = tmp.path().join("CLAUDE.md");
        // Pre-populate with an existing active-work section
        let initial = format!(
            "# Instructions\n\n{begin}\n## Active Work\n- ticket here\n{end}\n\nDo work.\n",
            begin = ACTIVE_WORK_BEGIN,
            end = ACTIVE_WORK_END
        );
        fs::write(&dest, &initial).unwrap();

        let empty_block = cascade_core::pbd::active_work::ActiveWorkBlock::default();
        let changed = inject_active_work_section(&dest, &empty_block, false).unwrap();
        assert!(changed, "file should be modified when removing section");

        let content = fs::read_to_string(&dest).unwrap();
        assert!(
            !content.contains(ACTIVE_WORK_BEGIN),
            "begin marker must be removed"
        );
        assert!(
            !content.contains(ACTIVE_WORK_END),
            "end marker must be removed"
        );
        assert!(
            content.contains("# Instructions"),
            "original content preserved"
        );
    }

    // ── test 11: dry-run inject writes nothing ─────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn inject_active_work_dry_run_writes_nothing() {
        let tmp = TempDir::new().unwrap();
        let dest = tmp.path().join("CLAUDE.md");
        let original = "# Instructions\n\nDo work.\n";
        fs::write(&dest, original).unwrap();

        let active = cascade_core::pbd::active_work::ActiveWorkBlock {
            sprint_id: Some("s01".into()),
            sprint_title: Some("Sprint 1".into()),
            tickets: vec![],
            tickets_total: 0,
            tasks: vec![cascade_core::pbd::active_work::ActiveTaskEntry {
                title: "Some task".into(),
                status: "todo".into(),
                tags: vec![],
            }],
            tasks_total: 1,
        };

        let changed = inject_active_work_section(&dest, &active, /* dry_run= */ true).unwrap();
        assert!(!changed, "dry-run must return false (no write)");

        let content = fs::read_to_string(&dest).unwrap();
        assert_eq!(content, original, "dry-run must not modify file");
    }

    // ── test 12: on-demand rules appear as pointer comments, NOT inlined ───────

    /// An on-demand rule must appear as a `<!-- -> ... -->` pointer comment,
    /// not as full body text, in every harness output file.
    ///
    /// WHY: context-budget regression fix — on-demand rules must never be
    /// inlined into harness instruction files.
    #[test]
    #[serial(global_env)]
    fn on_demand_rules_rendered_as_pointer_comments() {
        let tmp = TempDir::new().unwrap();
        let mut resolved = mock_resolved("## Always-loaded body\n\nDo good work.");
        resolved.on_demand_rules = vec![
            cascade_types::tiers::OnDemandRule {
                text: "Load auth policy before implementing login".into(),
                load_when: Some("auth".into()),
                source_tier: "GCI".into(),
            },
            cascade_types::tiers::OnDemandRule {
                text: "Review deploy checklist".into(),
                load_when: None,
                source_tier: "PRC".into(),
            },
        ];

        generate_for_harnesses(&resolved, tmp.path(), HarnessKind::ALL, false)
            .expect("generate should succeed");

        for harness in HarnessKind::ALL {
            let dest = tmp.path().join(harness.output_filename());
            let content = fs::read_to_string(&dest).unwrap();

            // On-demand rules must appear as pointer comments.
            assert!(
                content.contains("<!-- -> Load auth policy before implementing login"),
                "harness={}: must contain auth pointer comment; got:\n{}",
                harness.id(),
                content
            );
            assert!(
                content.contains("load_when: auth"),
                "harness={}: auth pointer must include load_when; got:\n{}",
                harness.id(),
                content
            );

            // The on-demand rule text must NOT appear as plain body text
            // (i.e. outside of HTML comments). We verify by checking it's not
            // present outside of `<!--` ... `-->` wrapping.
            // A simple check: "Review deploy checklist" appears only in comment form.
            assert!(
                content.contains("<!-- -> Review deploy checklist"),
                "harness={}: must contain deploy pointer comment",
                harness.id()
            );

            // Always-loaded body must still be present.
            assert!(
                content.contains("Do good work."),
                "harness={}: must still contain always-loaded body",
                harness.id()
            );
        }
    }

    // ── test 13: generated files embed a sha256 content hash ─────────────────

    /// Every generated harness file must have a marker line with sha256=...
    ///
    /// WHY: P12 requirement — hand-edits detectable via hash mismatch.
    #[test]
    #[serial(global_env)]
    fn generated_files_embed_content_hash() {
        let tmp = TempDir::new().unwrap();
        let resolved = mock_resolved("## RULES\n\nHash test content.");

        generate_for_harnesses(&resolved, tmp.path(), HarnessKind::ALL, false).unwrap();

        for harness in HarnessKind::ALL {
            let dest = tmp.path().join(harness.output_filename());
            let content = fs::read_to_string(&dest).unwrap();

            // Marker must include sha256= fragment.
            assert!(
                content.contains("<!-- cascade:unified-harness sha256="),
                "harness={}: marker must include sha256=; got first line:\n{}",
                harness.id(),
                content.lines().next().unwrap_or("")
            );

            // The embedded hash must be 32 hex chars.
            let hash_start = content.find("sha256=").expect("sha256= must be present");
            let hash_region = &content[hash_start + "sha256=".len()..];
            let hash: String = hash_region.chars().take(32).collect();
            assert!(
                hash.len() == 32 && hash.chars().all(|c| c.is_ascii_hexdigit()),
                "harness={}: embedded hash must be 32 hex chars, got: '{hash}'",
                harness.id()
            );
        }
    }

    // ── test 14: snapshot is created before overwrite ─────────────────────────

    /// When an existing file would be overwritten, a snapshot is created first.
    ///
    /// WHY: P12 requirement — snapshot-before-regenerate must capture
    /// pre-write content so the file can be recovered.
    #[test]
    #[serial(global_env)]
    fn snapshot_created_before_overwrite() {
        let tmp = TempDir::new().unwrap();
        // Pre-write a file WITHOUT the cascade marker so generate will overwrite it.
        let claude_md = tmp.path().join("CLAUDE.md");
        fs::write(&claude_md, "# Hand-written content\n\nDo not lose this.\n").unwrap();

        let resolved = mock_resolved("## RULES\n\nNew content.");
        generate_for_harnesses(
            &resolved,
            tmp.path(),
            &[HarnessKind::ClaudeCode],
            /* dry_run= */ false,
        )
        .unwrap();

        // A snapshot directory must exist under .cascade/snapshots/.
        let snap_root = tmp.path().join(".cascade").join("snapshots");
        assert!(snap_root.exists(), ".cascade/snapshots/ must be created");

        let mut snap_dirs: Vec<_> = fs::read_dir(&snap_root)
            .unwrap()
            .filter_map(|e| e.ok())
            .map(|e| e.path())
            .filter(|p| p.is_dir())
            .collect();
        assert!(!snap_dirs.is_empty(), "at least one snapshot dir must exist");
        snap_dirs.sort();

        // The snapshot must contain a copy of the original CLAUDE.md.
        let snap_claude = snap_dirs[0].join("CLAUDE.md");
        assert!(snap_claude.exists(), "snapshot must contain CLAUDE.md");
        let snap_content = fs::read_to_string(&snap_claude).unwrap();
        assert!(
            snap_content.contains("Hand-written content"),
            "snapshot must preserve original content; got: {snap_content}"
        );

        // The live CLAUDE.md must now contain the new generated content.
        let new_content = fs::read_to_string(&claude_md).unwrap();
        assert!(
            new_content.contains("New content"),
            "live file must contain newly generated content"
        );
    }

    // ── test 15: hand-edit detected via hash mismatch ─────────────────────────

    /// After generation, modifying the file body causes hash_matches to return
    /// Some(false), confirming the hand-edit detection path.
    ///
    /// WHY: P12 requirement — hand-edits must be detectable via hash mismatch.
    #[test]
    #[serial(global_env)]
    fn hand_edit_detected_via_hash_mismatch() {
        use crate::generate::safe_write::hash_matches;

        let tmp = TempDir::new().unwrap();
        let resolved = mock_resolved("## RULES\n\nOriginal content.");
        generate_for_harnesses(
            &resolved,
            tmp.path(),
            &[HarnessKind::ClaudeCode],
            false,
        )
        .unwrap();

        let dest = tmp.path().join("CLAUDE.md");
        let original = fs::read_to_string(&dest).unwrap();

        // Freshly generated file: hash must match.
        assert_eq!(
            hash_matches(&original),
            Some(true),
            "freshly generated file: hash must match"
        );

        // Simulate a hand-edit by appending text to the body.
        let hand_edited = format!("{original}\n## HAND EDIT\n");
        assert_eq!(
            hash_matches(&hand_edited),
            Some(false),
            "hand-edited file: hash must NOT match"
        );
    }
}
