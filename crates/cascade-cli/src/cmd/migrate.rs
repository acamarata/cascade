//! `cascade migrate` — migrate a legacy `.claude/` or `.opencode/` directory.
//!
//! Detects known legacy tool home directories and non-destructively copies
//! their contents into the cascade layout under `.cascade/`.
//!
//! # Migration map
//!
//! | Legacy path | Cascade path |
//! |-------------|--------------|
//! | `.claude/memory/` | `.cascade/memory/` |
//! | `.claude/rules/` | `.cascade/rules/` |
//! | `.claude/docs/` | `.cascade/docs/` |
//! | `.claude/ideas/` | `.cascade/ideas/` |
//! | `.claude/inbox/` | `.cascade/inbox/` |
//! | `.claude/tasks/` | `.cascade/tasks/` |
//! | `.claude/planning/` | `.cascade/planning/` |
//! | `.claude/CLAUDE.md` (non-symlink) | `.cascade/CASCADE.md` |
//! | `.opencode/phases/` | `.cascade/phases/` |
//!
//! `--dry-run` prints the mapping table without writing.
//! `--confirm-delete` removes source files after a successful copy.

use std::path::{Path, PathBuf};

use async_trait::async_trait;
use cascade_types::error::{CascadeError, Result};
use clap::Args;

use super::Command;

/// Arguments for `cascade migrate`.
#[derive(Debug, Args)]
pub struct MigrateArgs {
    /// Legacy tool to migrate from. Auto-detected if omitted.
    ///
    /// Valid values: claude, opencode, codex
    #[arg(long)]
    pub from: Option<String>,

    /// Print what would be migrated without writing anything.
    #[arg(long)]
    pub dry_run: bool,

    /// Delete source files after a successful copy.
    ///
    /// DESTRUCTIVE — source files are permanently removed. Use with caution.
    #[arg(long)]
    pub confirm_delete: bool,

    /// Destination `.cascade/` directory. Defaults to nearest detected one.
    #[arg(long)]
    pub dest: Option<PathBuf>,
}

/// A single migration action.
#[derive(Debug)]
struct MigrationEntry {
    source: PathBuf,
    dest: PathBuf,
    /// Whether the dest already existed (conflict → skip).
    conflict: bool,
}

#[async_trait]
impl Command for MigrateArgs {
    async fn run(&self) -> Result<()> {
        let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
        let home = PathBuf::from(std::env::var("HOME").unwrap_or_else(|_| ".".into()));

        let from_tool = self
            .from
            .as_deref()
            .unwrap_or_else(|| detect_legacy_tool(&home, &cwd));

        let legacy_root = legacy_home(&home, &cwd, from_tool);
        if !legacy_root.exists() {
            eprintln!(
                "no {} directory found — nothing to migrate",
                legacy_root.display()
            );
            return Ok(());
        }

        let dest_cascade = match &self.dest {
            Some(d) => d.clone(),
            None => cwd.join(".cascade"),
        };

        let entries = build_migration_plan(&legacy_root, &dest_cascade, from_tool);

        // Print table.
        println!("{:<55} {:<55} STATUS", "SOURCE", "DEST");
        println!("{}", "-".repeat(120));
        for e in &entries {
            let status = if e.conflict {
                "SKIP (conflict)"
            } else {
                "COPY"
            };
            println!(
                "{:<55} {:<55} {}",
                e.source.display(),
                e.dest.display(),
                status
            );
        }

        if self.dry_run {
            println!("\n(dry run — no files written)");
            return Ok(());
        }

        let mut copied = 0usize;
        let mut skipped = 0usize;
        for e in &entries {
            if e.conflict {
                skipped += 1;
                continue;
            }
            if let Some(parent) = e.dest.parent() {
                std::fs::create_dir_all(parent).ok();
            }
            std::fs::copy(&e.source, &e.dest).map_err(|err| CascadeError::Io {
                path: e.dest.clone(),
                operation: "copy file",
                source: err,
            })?;
            if self.confirm_delete {
                std::fs::remove_file(&e.source).map_err(|err| CascadeError::Io {
                    path: e.source.clone(),
                    operation: "delete source after copy",
                    source: err,
                })?;
            }
            copied += 1;
        }

        // Write migration report.
        let report = format!(
            "# Migration Report\n\nFrom: {}\nTo: {}\nCopied: {}\nSkipped (conflicts): {}\n",
            legacy_root.display(),
            dest_cascade.display(),
            copied,
            skipped,
        );
        let report_name = format!("migration-{}.md", simple_date());
        let report_path = dest_cascade.join("memory").join(&report_name);
        std::fs::create_dir_all(report_path.parent().unwrap()).ok();
        std::fs::write(&report_path, &report).ok();

        println!("\nMigrated {copied} files, skipped {skipped} conflicts.");
        println!("Report written to {}", report_path.display());
        Ok(())
    }
}

fn detect_legacy_tool(home: &Path, _cwd: &Path) -> &'static str {
    if home.join(".claude").exists() {
        "claude"
    } else if home.join(".opencode").exists() {
        "opencode"
    } else if home.join(".codex").exists() {
        "codex"
    } else {
        "claude"
    }
}

fn legacy_home(home: &Path, cwd: &Path, tool: &str) -> PathBuf {
    match tool {
        "opencode" => {
            // Check local first, then $HOME.
            let local = cwd.join(".opencode");
            if local.exists() {
                local
            } else {
                home.join(".opencode")
            }
        }
        "codex" => home.join(".codex"),
        _ => {
            let local = cwd.join(".claude");
            if local.exists() {
                local
            } else {
                home.join(".claude")
            }
        }
    }
}

fn build_migration_plan(legacy: &Path, dest: &Path, tool: &str) -> Vec<MigrationEntry> {
    let mappings: &[(&str, &str)] = match tool {
        "opencode" => &[
            ("phases", "phases"),
            ("memory", "memory"),
            ("docs", "docs"),
            ("inbox", "inbox"),
        ],
        _ => &[
            ("memory", "memory"),
            ("rules", "rules"),
            ("docs", "docs"),
            ("ideas", "ideas"),
            ("inbox", "inbox"),
            ("tasks", "tasks"),
            ("planning", "planning"),
            ("phases", "phases"),
        ],
    };

    let mut entries = Vec::new();
    for (src_sub, dst_sub) in mappings {
        let src_dir = legacy.join(src_sub);
        if !src_dir.exists() {
            continue;
        }
        if let Ok(walk) = walk_files(&src_dir) {
            for src_file in walk {
                let rel = src_file.strip_prefix(&src_dir).unwrap_or(&src_file);
                let dest_file = dest.join(dst_sub).join(rel);
                let conflict = dest_file.exists();
                entries.push(MigrationEntry {
                    source: src_file,
                    dest: dest_file,
                    conflict,
                });
            }
        }
    }

    // Special: CLAUDE.md at legacy root (non-symlink) → CASCADE.md.
    if tool == "claude" {
        let claude_md = legacy.join("CLAUDE.md");
        if claude_md.exists() && !claude_md.is_symlink() {
            let dest_file = dest.join("CASCADE.md");
            let conflict = dest_file.exists();
            entries.push(MigrationEntry {
                source: claude_md,
                dest: dest_file,
                conflict,
            });
        }
    }

    entries
}

fn walk_files(dir: &Path) -> std::io::Result<Vec<PathBuf>> {
    let mut result = Vec::new();
    for entry in std::fs::read_dir(dir)? {
        let entry = entry?;
        let path = entry.path();
        if path.is_dir() {
            result.extend(walk_files(&path)?);
        } else {
            result.push(path);
        }
    }
    Ok(result)
}

fn simple_date() -> String {
    use std::time::{SystemTime, UNIX_EPOCH};
    let secs = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs();
    let days = secs / 86400;
    let y = 1970 + (days / 365);
    let m = (days % 365 / 30) + 1;
    let d = (days % 30) + 1;
    format!("{:04}-{:02}-{:02}", y, m.min(12), d.min(31))
}
