//! `cascade snapshot` subcommand — list and restore pre-generation snapshots.
//!
//! ## Purpose
//!
//! Before every generation run, Cascade copies the files it is about to
//! overwrite into `<workspace>/.cascade/snapshots/<timestamp>/`.  This command
//! surfaces those snapshots and optionally restores them.
//!
//! ## Subcommands
//!
//! ```text
//! cascade snapshot list [--workspace <path>]
//! cascade snapshot restore <timestamp> [--workspace <path>] [--apply]
//! ```
//!
//! `restore` is a dry-run by default: it prints what would be restored without
//! touching any files.  Pass `--apply` to actually write the files.
//!
//! ## Recovery example
//!
//! ```text
//! $ cascade snapshot list
//! 3 snapshots (oldest first):
//!   20260616-142301-007  (.cascade/snapshots/20260616-142301-007/)
//!   20260616-150012-042
//!   20260616-153344-118
//!
//! $ cascade snapshot restore 20260616-150012-042 --apply
//! restored: .cascade/snapshots/.../CLAUDE.md -> /project/CLAUDE.md
//! ```
//!
//! ## SPORT
//!
//! MASTER-CLI.md: cascade snapshot command (P12, snapshot-before-regenerate)

use std::path::PathBuf;

use async_trait::async_trait;
use cascade_harness::generate::safe_write::{list_snapshots, restore_snapshot};
use cascade_types::error::{CascadeError, Result};
use clap::{Args, Subcommand};

use super::Command;

// ── Args ──────────────────────────────────────────────────────────────────────

/// Arguments for `cascade snapshot`.
#[derive(Debug, Args)]
pub struct SnapshotArgs {
    #[command(subcommand)]
    pub cmd: SnapshotSubcommand,
}

/// Subcommands for `cascade snapshot`.
#[derive(Debug, Subcommand)]
pub enum SnapshotSubcommand {
    /// List available snapshots for the workspace (oldest first).
    List {
        /// Workspace directory.  Defaults to the current working directory.
        #[arg(long, value_name = "PATH")]
        workspace: Option<PathBuf>,
    },

    /// Restore files from a snapshot back into the workspace.
    ///
    /// Dry-run by default: prints planned restores without writing.
    /// Pass `--apply` to execute the restore.
    Restore {
        /// Timestamp of the snapshot to restore (from `cascade snapshot list`).
        timestamp: String,

        /// Workspace directory.  Defaults to the current working directory.
        #[arg(long, value_name = "PATH")]
        workspace: Option<PathBuf>,

        /// Write files instead of only printing what would be restored.
        #[arg(long)]
        apply: bool,
    },
}

#[async_trait]
impl Command for SnapshotArgs {
    async fn run(&self) -> Result<()> {
        match &self.cmd {
            SnapshotSubcommand::List { workspace } => {
                let ws = resolve_workspace(workspace)?;
                run_list(&ws)
            }
            SnapshotSubcommand::Restore {
                timestamp,
                workspace,
                apply,
            } => {
                let ws = resolve_workspace(workspace)?;
                run_restore(&ws, timestamp, !apply)
            }
        }
    }
}

// ── Handlers ──────────────────────────────────────────────────────────────────

/// List snapshots in the workspace, oldest first.
fn run_list(workspace: &std::path::Path) -> Result<()> {
    let entries = list_snapshots(workspace)?;

    if entries.is_empty() {
        println!("No snapshots found in {}", workspace.display());
        return Ok(());
    }

    println!(
        "{} snapshot{} (oldest first):",
        entries.len(),
        if entries.len() == 1 { "" } else { "s" }
    );
    for entry in &entries {
        println!("  {}  ({})", entry.timestamp, entry.path.display());
    }

    Ok(())
}

/// Restore files from the snapshot with `timestamp` into `workspace`.
///
/// `dry_run = true` means only print, no writes.
fn run_restore(workspace: &std::path::Path, timestamp: &str, dry_run: bool) -> Result<()> {
    // Locate the snapshot directory.
    let snap_dir = workspace.join(".cascade").join("snapshots").join(timestamp);

    if !snap_dir.exists() {
        return Err(CascadeError::Other(format!(
            "snapshot '{}' not found at '{}'.\n\
             Run `cascade snapshot list` to see available snapshots.",
            timestamp,
            snap_dir.display()
        )));
    }

    let pairs = restore_snapshot(&snap_dir, workspace, dry_run)?;

    if pairs.is_empty() {
        println!("Snapshot '{}' is empty — nothing to restore.", timestamp);
        return Ok(());
    }

    if dry_run {
        println!(
            "\n{} file{} would be restored.  Pass --apply to execute.",
            pairs.len(),
            if pairs.len() == 1 { "" } else { "s" }
        );
    } else {
        println!(
            "\n{} file{} restored from snapshot '{}'.",
            pairs.len(),
            if pairs.len() == 1 { "" } else { "s" },
            timestamp
        );
    }

    Ok(())
}

// ── Utilities ─────────────────────────────────────────────────────────────────

/// Resolve workspace to the supplied path or the current working directory.
fn resolve_workspace(workspace: &Option<PathBuf>) -> Result<PathBuf> {
    workspace
        .clone()
        .or_else(|| std::env::current_dir().ok())
        .ok_or_else(|| CascadeError::Other("cannot determine workspace directory".into()))
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    // ── test 1: list returns empty when no snapshots ──────────────────────────

    /// `run_list` returns Ok (no panic) when no snapshots exist.
    ///
    /// WHY: first-run UX — user should see a friendly "no snapshots" message.
    #[test]
    fn list_empty_workspace() {
        let tmp = TempDir::new().unwrap();
        run_list(tmp.path()).expect("list should succeed even with no snapshots");
    }

    // ── test 2: list shows snapshot names ────────────────────────────────────

    /// `run_list` prints snapshot directory names.
    #[test]
    fn list_shows_snapshots() {
        let tmp = TempDir::new().unwrap();
        let snap_root = tmp.path().join(".cascade").join("snapshots");
        fs::create_dir_all(snap_root.join("20260101-000000-001")).unwrap();
        fs::create_dir_all(snap_root.join("20260102-000000-002")).unwrap();

        // Should not error — content is printed to stdout (captured by test runner).
        run_list(tmp.path()).expect("list should succeed");
    }

    // ── test 3: restore dry-run writes nothing ────────────────────────────────

    /// `run_restore` in dry-run mode must not create any files in the workspace.
    ///
    /// WHY: P12 spec — dry-run is the default; safe to call without --apply.
    #[test]
    fn restore_dry_run_creates_nothing() {
        let tmp = TempDir::new().unwrap();
        let ts = "20260101-000000-001";
        let snap_dir = tmp.path().join(".cascade").join("snapshots").join(ts);
        fs::create_dir_all(&snap_dir).unwrap();
        fs::write(snap_dir.join("CLAUDE.md"), "snapshot content").unwrap();

        let dest = tmp.path().join("CLAUDE.md");
        run_restore(tmp.path(), ts, /* dry_run= */ true).expect("dry-run restore should succeed");
        assert!(!dest.exists(), "dry-run must not write CLAUDE.md");
    }

    // ── test 4: restore --apply writes files ─────────────────────────────────

    /// `run_restore` with `dry_run = false` writes files back.
    ///
    /// WHY: P12 spec — `--apply` must actually restore content.
    #[test]
    fn restore_apply_writes_files() {
        let tmp = TempDir::new().unwrap();
        let ts = "20260101-000000-001";
        let snap_dir = tmp.path().join(".cascade").join("snapshots").join(ts);
        fs::create_dir_all(&snap_dir).unwrap();
        fs::write(snap_dir.join("AGENTS.md"), "original agents").unwrap();

        let dest = tmp.path().join("AGENTS.md");
        run_restore(tmp.path(), ts, /* dry_run= */ false).expect("apply restore should succeed");
        assert!(dest.exists(), "AGENTS.md must be restored");
        assert_eq!(
            fs::read_to_string(&dest).unwrap(),
            "original agents",
            "restored content must match snapshot"
        );
    }

    // ── test 5: restore unknown timestamp returns error ───────────────────────

    /// `run_restore` with a non-existent timestamp returns Err (not a panic).
    ///
    /// WHY: user-facing error must be descriptive, not a crash.
    #[test]
    fn restore_unknown_timestamp_errors() {
        let tmp = TempDir::new().unwrap();
        let result = run_restore(tmp.path(), "99991231-235959-999", /* dry_run= */ true);
        assert!(result.is_err(), "unknown timestamp must return Err");
        let msg = result.unwrap_err().to_string();
        assert!(
            msg.contains("not found"),
            "error message must mention 'not found': {msg}"
        );
    }

    // ── test 6: clap argument parsing ────────────────────────────────────────

    /// Argument parser accepts `snapshot list` and `snapshot restore`.
    #[test]
    fn snapshot_args_parse() {
        use clap::Parser;

        #[derive(Parser)]
        struct Cli {
            #[command(subcommand)]
            cmd: SnapshotSubcommand,
        }

        // list
        let cli = Cli::try_parse_from(["snapshot", "list"]).unwrap();
        assert!(matches!(cli.cmd, SnapshotSubcommand::List { .. }));

        // restore dry-run (default)
        let cli2 = Cli::try_parse_from(["snapshot", "restore", "20260101-000000-001"]).unwrap();
        assert!(matches!(
            cli2.cmd,
            SnapshotSubcommand::Restore { apply: false, .. }
        ));

        // restore --apply
        let cli3 =
            Cli::try_parse_from(["snapshot", "restore", "20260101-000000-001", "--apply"]).unwrap();
        assert!(matches!(
            cli3.cmd,
            SnapshotSubcommand::Restore { apply: true, .. }
        ));
    }
}
