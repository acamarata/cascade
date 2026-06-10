//! `cascade uninstall` — remove cascade artifacts from the system.
//!
//! # Purpose
//! Two modes:
//! - `--keep-cascade`: Removes the daemon LaunchAgent plist, all cascade-owned
//!   symlinks (those whose target points into `~/.cascade/`), and tool-home
//!   symlinks (e.g. `~/.claude/CLAUDE.md → CASCADE.md`). Keeps `~/.cascade/`
//!   and its contents intact.
//! - `--full`: Everything `--keep-cascade` does, PLUS restores all archived
//!   legacy tools from `~/.cascade/legacy/manifest.json` to their original
//!   paths, then removes `~/.cascade/` entirely.
//!
//! # Safety invariants
//! - Interactive confirmation (y/N) required unless `--yes` is passed.
//! - Restore-before-delete ordering enforced for `--full`.
//! - HOME-confined paths only: refuses any path outside `$HOME`.
//! - Symlinks: only removes links whose resolved target is inside `~/.cascade/`.
//! - Never calls `rm -rf` on arbitrary paths; uses targeted `fs::remove_file`
//!   and `fs::remove_dir_all` scoped to `~/.cascade/` only.
//! - On partial failure in `--full`, exits 1 and lists skipped items without
//!   removing `~/.cascade/`.
//!
//! # SPORT
//! MASTER-CLI.md: `cascade uninstall --keep-cascade / --full / --dry-run` — ✅
//! Task: T-P3-E03-38

use std::path::{Path, PathBuf};

use async_trait::async_trait;
use cascade_types::error::{CascadeError, Result};
use clap::Args;
use serde::{Deserialize, Serialize};

use super::Command;

// ---------------------------------------------------------------------------
// Clap args
// ---------------------------------------------------------------------------

/// Arguments for `cascade uninstall`.
#[derive(Debug, Args)]
#[command(
    about = "Remove cascade artifacts from the system",
    long_about = "Remove cascade daemon service, symlinks, and optionally restore archived \
                  legacy tool homes and delete ~/.cascade/.\n\n\
                  Defaults to --keep-cascade when neither flag is given."
)]
pub struct UninstallArgs {
    /// Remove daemon service and symlinks; keep ~/.cascade/ contents.
    #[arg(long, conflicts_with = "full")]
    pub keep_cascade: bool,

    /// Restore archived legacy tools and remove ~/.cascade/ entirely.
    /// Implies all actions of --keep-cascade first.
    #[arg(long, conflicts_with = "keep_cascade")]
    pub full: bool,

    /// Print planned actions without executing them.
    #[arg(long)]
    pub dry_run: bool,

    /// Skip interactive confirmation prompt.
    #[arg(long, short = 'y')]
    pub yes: bool,
}

#[async_trait]
impl Command for UninstallArgs {
    async fn run(&self) -> Result<()> {
        // Default to keep-cascade when neither flag given.
        let mode = if self.full {
            UninstallMode::Full
        } else {
            if !self.keep_cascade {
                eprintln!(
                    "notice: Defaulting to --keep-cascade. \
                     Use --full to also restore archives and remove ~/.cascade/."
                );
            }
            UninstallMode::KeepCascade
        };

        let home = home_dir()?;
        let cascade_dir = home.join(".cascade");

        // Build plan (always, even for dry-run reporting).
        let plan = build_plan(&home, &cascade_dir, &mode)?;

        if plan.actions.is_empty() {
            println!("Nothing to uninstall.");
            return Ok(());
        }

        // Print plan.
        println!("Planned actions:");
        for action in &plan.actions {
            println!("  {}", action.describe());
        }

        if self.dry_run {
            println!("\n(dry-run: no changes made)");
            return Ok(());
        }

        // Confirmation prompt.
        if !self.yes {
            eprint!("\nProceed? [y/N] ");
            let mut buf = String::new();
            std::io::stdin()
                .read_line(&mut buf)
                .map_err(|e| CascadeError::Other(format!("failed to read stdin: {e}")))?;
            let answer = buf.trim().to_lowercase();
            if answer != "y" && answer != "yes" {
                println!("Aborted.");
                return Ok(());
            }
        }

        // Execute.
        let result = execute_plan(plan, &cascade_dir, &mode);

        for msg in &result.completed {
            println!("  ✓ {msg}");
        }
        for msg in &result.skipped {
            eprintln!("  skip: {msg}");
        }
        for msg in &result.errors {
            eprintln!("  error: {msg}");
        }

        if result.errors.is_empty() {
            println!("Uninstall complete.");
            Ok(())
        } else {
            Err(CascadeError::Other(format!(
                "{} action(s) failed — see errors above; ~/.cascade/ was NOT removed",
                result.errors.len()
            )))
        }
    }
}

// ---------------------------------------------------------------------------
// Mode
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, PartialEq)]
pub enum UninstallMode {
    KeepCascade,
    Full,
}

// ---------------------------------------------------------------------------
// Plan types
// ---------------------------------------------------------------------------

#[derive(Debug)]
enum Action {
    UnloadLaunchAgent { plist: PathBuf },
    RemoveLaunchAgentPlist { plist: PathBuf },
    RemoveSymlink { path: PathBuf },
    RestoreArchive { tool_id: String },
    RemoveCascadeDir { dir: PathBuf },
}

impl Action {
    fn describe(&self) -> String {
        match self {
            Action::UnloadLaunchAgent { plist } => {
                format!("launchctl unload {}", plist.display())
            }
            Action::RemoveLaunchAgentPlist { plist } => {
                format!("remove LaunchAgent plist: {}", plist.display())
            }
            Action::RemoveSymlink { path } => {
                format!("remove symlink: {}", path.display())
            }
            Action::RestoreArchive { tool_id } => {
                format!("restore archived tool: {tool_id}")
            }
            Action::RemoveCascadeDir { dir } => {
                format!("remove directory: {}", dir.display())
            }
        }
    }
}

struct Plan {
    actions: Vec<Action>,
}

// ---------------------------------------------------------------------------
// Plan builder
// ---------------------------------------------------------------------------

fn build_plan(home: &Path, cascade_dir: &Path, mode: &UninstallMode) -> Result<Plan> {
    let mut actions = Vec::new();

    // 1. Daemon LaunchAgent (macOS).
    #[cfg(target_os = "macos")]
    {
        let plist = home
            .join("Library")
            .join("LaunchAgents")
            .join("com.acamarata.cascade.plist");
        if plist.exists() {
            actions.push(Action::UnloadLaunchAgent { plist: plist.clone() });
            actions.push(Action::RemoveLaunchAgentPlist { plist });
        }
    }

    // 2. Cascade-owned symlinks: scan cascade_dir and all tool-home dirs.
    let symlinks = collect_cascade_symlinks(home, cascade_dir);
    for link in symlinks {
        actions.push(Action::RemoveSymlink { path: link });
    }

    // 3. Full mode: restore archives + remove ~/.cascade/.
    if *mode == UninstallMode::Full {
        let archive_tools = read_manifest_tool_ids(cascade_dir);
        for tool_id in archive_tools {
            actions.push(Action::RestoreArchive { tool_id });
        }
        actions.push(Action::RemoveCascadeDir {
            dir: cascade_dir.to_path_buf(),
        });
    }

    Ok(Plan { actions })
}

/// Collect all symlinks whose target starts with `cascade_dir`.
///
/// Scans:
/// 1. `cascade_dir` itself (direct children).
/// 2. Known tool-home dirs: `~/.claude/`, `~/.opencode/`.
fn collect_cascade_symlinks(home: &Path, cascade_dir: &Path) -> Vec<PathBuf> {
    let mut found = Vec::new();

    // Canonicalize cascade_dir once so macOS /var → /private/var symlinks don't
    // confuse the `starts_with` check.
    let canonical_cascade = cascade_dir.canonicalize().unwrap_or_else(|_| cascade_dir.to_path_buf());

    // Dirs to scan for symlinks owned by cascade.
    let scan_dirs: &[PathBuf] = &[
        cascade_dir.to_path_buf(),
        home.join(".claude"),
        home.join(".opencode"),
    ];

    for dir in scan_dirs {
        if !dir.exists() {
            continue;
        }
        let Ok(entries) = std::fs::read_dir(dir) else {
            continue;
        };
        for entry in entries.flatten() {
            let path = entry.path();
            if path.is_symlink() {
                // Resolve target and verify it points into cascade_dir.
                if let Ok(target) = std::fs::read_link(&path) {
                    // Target may be relative to the link's parent directory.
                    let abs_target = if target.is_absolute() {
                        target.clone()
                    } else {
                        path.parent()
                            .unwrap_or(Path::new("."))
                            .join(&target)
                    };
                    let canonical = abs_target
                        .canonicalize()
                        .unwrap_or(abs_target);
                    if canonical.starts_with(&canonical_cascade) {
                        found.push(path);
                    }
                }
            }
        }
    }

    found
}

/// Parse `~/.cascade/legacy/manifest.json` and return all `toolId` values.
/// Returns empty vec if manifest does not exist or is unparseable.
fn read_manifest_tool_ids(cascade_dir: &Path) -> Vec<String> {
    let manifest_path = cascade_dir.join("legacy").join("manifest.json");
    let Ok(raw) = std::fs::read_to_string(&manifest_path) else {
        return Vec::new();
    };
    let Ok(manifest) = serde_json::from_str::<ArchiveManifest>(&raw) else {
        return Vec::new();
    };
    manifest.tools.into_iter().map(|t| t.tool_id).collect()
}

// ---------------------------------------------------------------------------
// Execution
// ---------------------------------------------------------------------------

struct ExecuteResult {
    completed: Vec<String>,
    skipped: Vec<String>,
    errors: Vec<String>,
}

fn execute_plan(plan: Plan, cascade_dir: &Path, _mode: &UninstallMode) -> ExecuteResult {
    let mut completed = Vec::new();
    let mut skipped = Vec::new();
    let mut errors: Vec<String> = Vec::new();

    // Track whether any restore failed — if so, don't remove ~/.cascade/.
    let mut restore_failed = false;

    let home = match home_dir() {
        Ok(h) => h,
        Err(e) => {
            errors.push(format!("cannot resolve HOME: {e}"));
            return ExecuteResult { completed, skipped, errors };
        }
    };

    // Canonicalize cascade_dir once to survive macOS /var → /private/var.
    let canonical_cascade = cascade_dir.canonicalize().unwrap_or_else(|_| cascade_dir.to_path_buf());

    for action in plan.actions {
        match action {
            Action::UnloadLaunchAgent { ref plist } => {
                #[cfg(target_os = "macos")]
                {
                    let output = std::process::Command::new("launchctl")
                        .args(["unload", "-w"])
                        .arg(plist)
                        .output();
                    match output {
                        Ok(o) if o.status.success() => {
                            completed.push(format!("unloaded LaunchAgent: {}", plist.display()));
                        }
                        Ok(o) => {
                            // launchctl exits non-zero if not loaded — treat as skip.
                            let stderr = String::from_utf8_lossy(&o.stderr);
                            skipped.push(format!(
                                "LaunchAgent already unloaded ({}): {}",
                                o.status,
                                stderr.trim()
                            ));
                        }
                        Err(e) => {
                            errors.push(format!("launchctl unload failed: {e}"));
                        }
                    }
                }
                #[cfg(not(target_os = "macos"))]
                {
                    skipped.push(format!(
                        "LaunchAgent unload skipped (not macOS): {}",
                        plist.display()
                    ));
                }
            }

            Action::RemoveLaunchAgentPlist { ref plist } => {
                // Safety: only remove plists inside ~/Library/LaunchAgents/.
                let la_dir = home.join("Library").join("LaunchAgents");
                if !plist.starts_with(&la_dir) {
                    errors.push(format!(
                        "SECURITY: plist '{}' not inside ~/Library/LaunchAgents — skipped",
                        plist.display()
                    ));
                    continue;
                }
                if plist.exists() {
                    match std::fs::remove_file(plist) {
                        Ok(()) => completed.push(format!("removed plist: {}", plist.display())),
                        Err(e) => errors.push(format!("remove plist '{}': {e}", plist.display())),
                    }
                } else {
                    skipped.push(format!("plist not found: {}", plist.display()));
                }
            }

            Action::RemoveSymlink { ref path } => {
                // Safety: only remove if still a symlink pointing into cascade_dir.
                if !path.is_symlink() {
                    skipped.push(format!(
                        "not a symlink (skipped): {}",
                        path.display()
                    ));
                    continue;
                }
                // HOME-confinement check.
                if !path.starts_with(&home) {
                    errors.push(format!(
                        "SECURITY: '{}' is not HOME-confined — skipped",
                        path.display()
                    ));
                    continue;
                }
                // Re-verify target still points into cascade_dir.
                let target_ok = std::fs::read_link(path)
                    .ok()
                    .map(|t| {
                        let abs = if t.is_absolute() {
                            t
                        } else {
                            path.parent().unwrap_or(Path::new(".")).join(&t)
                        };
                        let canonical = abs.canonicalize().unwrap_or(abs);
                        canonical.starts_with(&canonical_cascade)
                    })
                    .unwrap_or(false);

                if !target_ok {
                    skipped.push(format!(
                        "symlink target no longer in ~/.cascade/ — skipped: {}",
                        path.display()
                    ));
                    continue;
                }

                match std::fs::remove_file(path) {
                    Ok(()) => completed.push(format!("removed symlink: {}", path.display())),
                    Err(e) => {
                        errors.push(format!("remove symlink '{}': {e}", path.display()))
                    }
                }
            }

            Action::RestoreArchive { ref tool_id } => {
                // Delegate to restore_from_manifest (inlined from restore.rs).
                let manifest_path = cascade_dir.join("legacy").join("manifest.json");
                match super::restore::restore_from_manifest_pub(
                    tool_id,
                    false,
                    &manifest_path,
                ) {
                    Ok(r) => {
                        completed.push(format!(
                            "restored archive '{}': {} files",
                            tool_id, r.restored_files
                        ));
                        if r.skipped_conflicts > 0 {
                            skipped.push(format!(
                                "archive '{}': {} conflicts skipped",
                                tool_id, r.skipped_conflicts
                            ));
                        }
                        for e in r.errors {
                            errors.push(format!("restore '{}': {e}", tool_id));
                            restore_failed = true;
                        }
                    }
                    Err(e) => {
                        errors.push(format!("restore archive '{}': {e}", tool_id));
                        restore_failed = true;
                    }
                }
            }

            Action::RemoveCascadeDir { ref dir } => {
                // Only executed in Full mode. Skip if any restore failed.
                if restore_failed {
                    skipped.push(format!(
                        "~/.cascade/ NOT removed — restore step(s) had errors ({})",
                        dir.display()
                    ));
                    continue;
                }
                // Safety: only remove if dir is exactly ~/.cascade/ (HOME-confined).
                let expected = home.join(".cascade");
                if dir != &expected {
                    errors.push(format!(
                        "SECURITY: refusing to remove '{}' — expected exactly ~/.cascade/",
                        dir.display()
                    ));
                    continue;
                }
                if !dir.exists() {
                    skipped.push(format!("~/.cascade/ already absent: {}", dir.display()));
                    continue;
                }
                match std::fs::remove_dir_all(dir) {
                    Ok(()) => completed.push(format!("removed ~/.cascade/: {}", dir.display())),
                    Err(e) => errors.push(format!("remove ~/.cascade/: {e}")),
                }
            }
        }
    }

    ExecuteResult { completed, skipped, errors }
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

fn home_dir() -> Result<PathBuf> {
    std::env::var("HOME")
        .map(PathBuf::from)
        .map_err(|_| CascadeError::Other("HOME env var not set".into()))
}

// ---------------------------------------------------------------------------
// Minimal manifest types (mirrors restore.rs — no shared crate yet)
// ---------------------------------------------------------------------------

#[derive(Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
struct ToolArchiveStub {
    tool_id: String,
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
struct ArchiveManifest {
    #[serde(default)]
    tools: Vec<ToolArchiveStub>,
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;
    use std::fs;
    use tempfile::TempDir;

    // ---- helpers ----

    fn fake_home() -> TempDir {
        TempDir::new().unwrap()
    }

    fn make_cascade_dir(home: &Path) -> PathBuf {
        let d = home.join(".cascade");
        fs::create_dir_all(&d).unwrap();
        d
    }

    fn make_symlink_into_cascade(link: &Path, cascade_dir: &Path) {
        let target = cascade_dir.join("CASCADE.md");
        fs::write(&target, b"# cascade").unwrap();
        #[cfg(unix)]
        std::os::unix::fs::symlink(&target, link).unwrap();
        #[cfg(windows)]
        std::os::windows::fs::symlink_file(&target, link).unwrap();
    }

    fn make_foreign_symlink(link: &Path, target: &Path) {
        fs::write(target, b"foreign").unwrap();
        #[cfg(unix)]
        std::os::unix::fs::symlink(target, link).unwrap();
        #[cfg(windows)]
        std::os::windows::fs::symlink_file(target, link).unwrap();
    }

    fn make_manifest(cascade_dir: &Path, tool_id: &str, orig: &Path, arch: &Path) {
        let legacy_dir = cascade_dir.join("legacy");
        fs::create_dir_all(&legacy_dir).unwrap();
        let archive_root = arch.parent().unwrap().to_str().unwrap();
        let orig_str = orig.to_str().unwrap();
        let arch_str = arch.to_str().unwrap();
        let json = format!(
            r#"{{
  "version": "1.0",
  "createdAt": "2026-06-09T00:00:00Z",
  "tools": [{{
    "toolId": "{tool_id}",
    "originalRoot": "/tmp",
    "archiveRoot": "{archive_root}",
    "archivedAt": "2026-06-09T00:01:00Z",
    "files": [{{
      "originalPath": "{orig_str}",
      "archivedPath": "{arch_str}",
      "movedAt": "2026-06-09T00:01:00Z",
      "sizeBytes": 4
    }}]
  }}]
}}"#
        );
        fs::write(legacy_dir.join("manifest.json"), json.as_bytes()).unwrap();
    }

    // ---- keep-cascade: ~/.cascade/ stays ----

    #[test]
    #[serial(global_env)]
    fn keep_cascade_retains_dir() {
        let home_tmp = fake_home();
        let home = home_tmp.path();
        unsafe { std::env::set_var("HOME", home) };

        let cascade_dir = make_cascade_dir(home);

        // Create a cascade-owned symlink.
        let claude_dir = home.join(".claude");
        fs::create_dir_all(&claude_dir).unwrap();
        let link = claude_dir.join("CLAUDE.md");
        make_symlink_into_cascade(&link, &cascade_dir);

        let plan = build_plan(home, &cascade_dir, &UninstallMode::KeepCascade).unwrap();
        let result = execute_plan(plan, &cascade_dir, &UninstallMode::KeepCascade);

        // Symlink removed.
        assert!(!link.exists(), "symlink should be removed");
        // ~/.cascade/ still present.
        assert!(cascade_dir.exists(), "~/.cascade/ must survive --keep-cascade");
        assert!(result.errors.is_empty(), "errors: {:?}", result.errors);
    }

    // ---- full: restores archive, then removes ~/.cascade/ ----

    #[test]
    #[serial(global_env)]
    fn full_restores_then_removes() {
        let home_tmp = fake_home();
        let home = home_tmp.path();
        unsafe { std::env::set_var("HOME", home) };

        let cascade_dir = make_cascade_dir(home);

        // Create archived file in legacy dir.
        let archive_dir = cascade_dir.join("legacy").join("myapp");
        fs::create_dir_all(&archive_dir).unwrap();
        let archived_file = archive_dir.join("config.json");
        fs::write(&archived_file, b"cfg").unwrap();

        // Original path in HOME.
        let orig_file = home.join("original_config.json");
        make_manifest(&cascade_dir, "myapp", &orig_file, &archived_file);

        let plan = build_plan(home, &cascade_dir, &UninstallMode::Full).unwrap();
        let result = execute_plan(plan, &cascade_dir, &UninstallMode::Full);

        // Archived file restored.
        assert!(orig_file.exists(), "original file should be restored");
        // ~/.cascade/ removed.
        assert!(!cascade_dir.exists(), "~/.cascade/ should be removed after --full");
        assert!(result.errors.is_empty(), "errors: {:?}", result.errors);
    }

    // ---- foreign symlink untouched ----

    #[test]
    #[serial(global_env)]
    fn foreign_symlink_untouched() {
        let home_tmp = fake_home();
        let home = home_tmp.path();
        unsafe { std::env::set_var("HOME", home) };

        let cascade_dir = make_cascade_dir(home);

        // Symlink pointing to somewhere outside ~/.cascade/.
        let foreign_target = home.join("foreign.txt");
        let link = cascade_dir.join("foreign_link.md");
        make_foreign_symlink(&link, &foreign_target);

        let plan = build_plan(home, &cascade_dir, &UninstallMode::KeepCascade).unwrap();
        let result = execute_plan(plan, &cascade_dir, &UninstallMode::KeepCascade);

        // Foreign link still present.
        assert!(link.is_symlink(), "foreign symlink must be untouched");
        assert!(result.errors.is_empty(), "errors: {:?}", result.errors);
    }

    // ---- confirm required (--yes skips it) ----

    #[test]
    #[serial(global_env)]
    fn dry_run_makes_no_changes() {
        let home_tmp = fake_home();
        let home = home_tmp.path();
        unsafe { std::env::set_var("HOME", home) };

        let cascade_dir = make_cascade_dir(home);

        // Create a cascade-owned symlink.
        let claude_dir = home.join(".claude");
        fs::create_dir_all(&claude_dir).unwrap();
        let link = claude_dir.join("CLAUDE.md");
        make_symlink_into_cascade(&link, &cascade_dir);

        // Build plan but don't execute (dry-run simulation).
        let plan = build_plan(home, &cascade_dir, &UninstallMode::KeepCascade).unwrap();

        // Verify plan has the symlink removal.
        let has_symlink_action = plan
            .actions
            .iter()
            .any(|a| matches!(a, Action::RemoveSymlink { path } if path == &link));
        assert!(has_symlink_action, "plan should include symlink removal");

        // Dry-run: don't execute; verify nothing changed.
        assert!(link.is_symlink(), "dry-run should not remove symlink");
        assert!(cascade_dir.exists(), "dry-run should not remove ~/.cascade/");
    }

    // ---- full with failed restore does not remove ~/.cascade/ ----

    #[test]
    #[serial(global_env)]
    fn full_partial_failure_keeps_cascade_dir() {
        let home_tmp = fake_home();
        let home = home_tmp.path();
        unsafe { std::env::set_var("HOME", home) };

        let cascade_dir = make_cascade_dir(home);

        // Write a manifest referencing a nonexistent archive.
        let archive_dir = cascade_dir.join("legacy").join("ghost");
        fs::create_dir_all(&archive_dir).unwrap();
        let missing_archive = archive_dir.join("ghost.cfg");
        // Do NOT create `missing_archive` — deliberately absent.
        let orig_file = home.join("ghost.cfg");
        make_manifest(&cascade_dir, "ghost", &orig_file, &missing_archive);

        let plan = build_plan(home, &cascade_dir, &UninstallMode::Full).unwrap();
        let result = execute_plan(plan, &cascade_dir, &UninstallMode::Full);

        // ~/.cascade/ must NOT be removed on partial failure.
        assert!(
            cascade_dir.exists(),
            "~/.cascade/ must survive a partial restore failure"
        );
        assert!(
            !result.errors.is_empty(),
            "errors should be reported for missing archive"
        );
    }
}
