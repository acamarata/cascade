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
//! The string `<!-- cascade:unified-harness -->` is written near the top of
//! every generated file.  Subsequent runs detect this and skip the file.
//!
//! ## SPORT
//!
//! MASTER-HARNESSES.md: all-harness generator (E-P7-03)

use std::fs;
use std::path::{Path, PathBuf};

use cascade_core::cascade_resolution::ResolvedCascade;
use cascade_types::error::{CascadeError, Result};

// ── Constants ─────────────────────────────────────────────────────────────────

/// Idempotency marker — written near the top of every generated harness file.
pub const UNIFIED_HARNESS_MARKER: &str = "<!-- cascade:unified-harness -->";

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
    let mut written = Vec::new();

    for &harness in harnesses {
        let dest = workspace.join(harness.output_filename());
        let content = render_harness_file(harness, resolved);

        if dest.exists() {
            let existing = fs::read_to_string(&dest).map_err(|e| CascadeError::Io {
                path: dest.clone(),
                operation: "read existing harness file",
                source: e,
            })?;
            if existing.contains(UNIFIED_HARNESS_MARKER) {
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

        if dry_run {
            println!(
                "[dry-run] would write {} ({}) — {} bytes",
                dest.display(),
                harness.id(),
                content.len()
            );
        } else {
            atomic_write(&dest, &content)?;
            println!("wrote: {} ({})", dest.display(), harness.id());
            written.push(dest);
        }
    }

    Ok(written)
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

    atomic_write(dest, &content)?;
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

/// Render the harness-native instruction file content.
///
/// The merged instruction body is IDENTICAL across all harnesses.
/// Only the envelope (frontmatter, header, format wrapper) differs.
fn render_harness_file(harness: HarnessKind, resolved: &ResolvedCascade) -> String {
    let body = resolved.merged_instructions.trim();
    let mcp = &resolved.mcp_server_url;

    match harness {
        HarnessKind::ClaudeCode => format!(
            "{marker}\n\
             <!-- cascade:harness=claude-code -->\n\
             \n\
             **MCP server:** `{mcp}`\n\
             Call `cascade.provide_harness_context` on startup for the full context payload.\n\
             \n\
             ---\n\
             \n\
             {body}\n",
            marker = UNIFIED_HARNESS_MARKER,
            mcp = mcp,
            body = body,
        ),
        HarnessKind::OpenCode | HarnessKind::Codex => {
            let id = harness.id();
            format!(
                "{marker}\n\
                 <!-- cascade:harness={id} -->\n\
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
                 {body}\n",
                marker = UNIFIED_HARNESS_MARKER,
                id = id,
                mcp = mcp,
                body = body,
            )
        }
        HarnessKind::Cursor => {
            // Cursor MDC format: fenced markdown with a YAML description header.
            format!(
                "{marker}\n\
                 <!-- cascade:harness=cursor -->\n\
                 ---\n\
                 description: Cascade unified AI instructions\n\
                 globs: [\"**/*\"]\n\
                 alwaysApply: true\n\
                 ---\n\
                 \n\
                 {body}\n",
                marker = UNIFIED_HARNESS_MARKER,
                body = body,
            )
        }
        HarnessKind::Aider => {
            // Aider uses CONVENTIONS.md; plain Markdown.
            format!(
                "{marker}\n\
                 <!-- cascade:harness=aider -->\n\
                 \n\
                 # Conventions\n\
                 \n\
                 {body}\n",
                marker = UNIFIED_HARNESS_MARKER,
                body = body,
            )
        }
        HarnessKind::Antigravity => {
            // Antigravity uses a Markdown rules file in `.antigravity/`.
            format!(
                "{marker}\n\
                 <!-- cascade:harness=antigravity -->\n\
                 \n\
                 # Cascade Context Rules\n\
                 \n\
                 **MCP server:** `{mcp}`  \n\
                 Call `provide_harness_context` on startup for the full context payload.\n\
                 \n\
                 ---\n\
                 \n\
                 {body}\n",
                marker = UNIFIED_HARNESS_MARKER,
                mcp = mcp,
                body = body,
            )
        }
    }
}

// ── Detection helpers ─────────────────────────────────────────────────────────

/// Returns true if `binary_name` resolves on `$PATH`.
fn is_binary_on_path(binary_name: &str) -> bool {
    std::env::var_os("PATH")
        .map(|paths| {
            std::env::split_paths(&paths).any(|dir| {
                let candidate = dir.join(binary_name);
                candidate.is_file()
                    || {
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

// ── Utilities ─────────────────────────────────────────────────────────────────

fn atomic_write(path: &Path, content: &str) -> Result<()> {
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent).map_err(|e| CascadeError::Io {
            path: parent.to_path_buf(),
            operation: "create_dir_all",
            source: e,
        })?;
    }

    let tmp = {
        let mut s = path.as_os_str().to_owned();
        s.push(".harness.tmp");
        PathBuf::from(s)
    };

    fs::write(&tmp, content).map_err(|e| CascadeError::Io {
        path: tmp.clone(),
        operation: "write_tmp",
        source: e,
    })?;

    fs::rename(&tmp, path).map_err(|e| CascadeError::Io {
        path: path.to_path_buf(),
        operation: "rename",
        source: e,
    })
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
            assert_eq!(
                id_b, id_a,
                "harness order changed between runs"
            );
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

        generate_for_harnesses(&resolved, tmp.path(), HarnessKind::ALL, /* dry_run= */ true)
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
            content.contains(UNIFIED_HARNESS_MARKER),
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
            std::env::set_var(
                "PATH",
                format!("{}:{}", bin_dir.display(), orig_path),
            );

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
                content.contains(UNIFIED_HARNESS_MARKER),
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
}
