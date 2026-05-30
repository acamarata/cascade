//! `cascade init [tier]` — scaffold a `.cascade/` directory.
//!
//! # Behaviour
//!
//! Without a tier argument, auto-detects the appropriate tier by inspecting
//! the current working directory:
//! - `~` or no git repo found above → `gci`
//! - Inside a Sites-style project root (contains sub-repos) → `ppc`
//! - Inside a git repo → `prc`
//! - Deeper than a git repo root (app subdirectory) → `pac`
//!
//! `--dry-run` prints what would be created without writing anything.
//! `--force` overwrites an existing `.cascade/` without prompting.
//!
//! # Exit codes
//! - `0` success
//! - `1` `.cascade/` already exists and `--force` was not passed

use std::path::{Path, PathBuf};

use async_trait::async_trait;
use clap::Args;
use cascade_types::error::{CascadeError, Result};
use cascade_types::paths::{CASCADE_DIR_NAME, CASCADE_MD_NAME, AGENTS_MD_NAME, CLAUDE_MD_NAME, subdirs};

use super::Command;

/// Arguments for `cascade init`.
#[derive(Debug, Args)]
pub struct InitArgs {
    /// Cascade tier to initialise. Auto-detected from CWD if omitted.
    ///
    /// Valid values: gci, pci, apc, ppc, prc, pac
    pub tier: Option<String>,

    /// Overwrite an existing `.cascade/` directory.
    #[arg(long)]
    pub force: bool,

    /// Print what would be created without writing anything.
    #[arg(long)]
    pub dry_run: bool,
}

/// The 14 standard subdirectories under `.cascade/`.
const SUBDIRS: &[&str] = &[
    subdirs::MEMORY,
    subdirs::DOCS,
    subdirs::IDEAS,
    subdirs::INBOX,
    subdirs::RULES,
    subdirs::TASKS,
    subdirs::PLANNING,
    subdirs::PHASES,
    subdirs::QA,
    subdirs::AGENTS,
    subdirs::SKILLS,
    subdirs::ARCHIVE,
    subdirs::TEMP,
    subdirs::PROVIDERS,
];

#[async_trait]
impl Command for InitArgs {
    async fn run(&self) -> Result<()> {
        let cwd = std::env::current_dir()
            .map_err(|e| CascadeError::Io { path: PathBuf::from("."), operation: "get cwd", source: e })?;

        let tier_label = self.tier.as_deref().unwrap_or_else(|| auto_detect_tier(&cwd));
        let root = tier_root(&cwd, tier_label)?;
        let cascade_dir = root.join(CASCADE_DIR_NAME);

        if cascade_dir.exists() && !self.force {
            eprintln!(
                "error: {} already exists. Pass --force to overwrite.",
                cascade_dir.display()
            );
            std::process::exit(1);
        }

        if self.dry_run {
            println!("Would create: {}/", cascade_dir.display());
            for sub in SUBDIRS {
                println!("  {}/{}/", CASCADE_DIR_NAME, sub);
            }
            println!("  {}/{}", CASCADE_DIR_NAME, CASCADE_MD_NAME);
            println!("  {}/{} -> {}", CASCADE_DIR_NAME, CLAUDE_MD_NAME, CASCADE_MD_NAME);
            println!("  {}/{} -> {}", CASCADE_DIR_NAME, AGENTS_MD_NAME, CASCADE_MD_NAME);
            println!("  {}/.gitignore", CASCADE_DIR_NAME);
            return Ok(());
        }

        // Create subdirectories.
        for sub in SUBDIRS {
            let dir = cascade_dir.join(sub);
            std::fs::create_dir_all(&dir)
                .map_err(|e| CascadeError::Io { path: dir.clone(), operation: "create_dir_all", source: e })?;
        }

        // Write starter CASCADE.md.
        let cascade_md = cascade_dir.join(CASCADE_MD_NAME);
        if !cascade_md.exists() || self.force {
            let content = starter_template(tier_label);
            std::fs::write(&cascade_md, content)
                .map_err(|e| CascadeError::Io { path: cascade_md.clone(), operation: "write CASCADE.md", source: e })?;
        }

        // Create CLAUDE.md and AGENTS.md symlinks.
        cascade_core::symlinks::create_siblings(&cascade_dir, self.force)?;

        // Write .gitignore.
        let gitignore = cascade_dir.join(".gitignore");
        if !gitignore.exists() || self.force {
            std::fs::write(&gitignore, GITIGNORE_CONTENT)
                .map_err(|e| CascadeError::Io { path: gitignore, operation: "write .gitignore", source: e })?;
        }

        println!("Initialised {} cascade at {}", tier_label.to_uppercase(), cascade_dir.display());
        Ok(())
    }
}

/// Detect the most appropriate tier for the given directory.
fn auto_detect_tier(cwd: &Path) -> &'static str {
    if let Some(home) = std::env::var("HOME").ok().map(PathBuf::from) {
        if cwd == home {
            return "gci";
        }
    }
    // If inside a git repo, default to PRC.
    if find_git_root(cwd).is_some() {
        return "prc";
    }
    "gci"
}

/// Resolve the filesystem root for the chosen tier.
fn tier_root(cwd: &Path, tier: &str) -> Result<PathBuf> {
    match tier {
        "gci" => {
            let home = std::env::var("HOME")
                .map(PathBuf::from)
                .map_err(|_| CascadeError::ConfigMissingKey { key: "HOME".into() })?;
            Ok(home)
        }
        "prc" => {
            // Use git repo root if inside one, otherwise CWD.
            Ok(find_git_root(cwd).unwrap_or_else(|| cwd.to_path_buf()))
        }
        _ => Ok(cwd.to_path_buf()),
    }
}

fn find_git_root(cwd: &Path) -> Option<PathBuf> {
    cwd.ancestors().find(|p| p.join(".git").exists()).map(|p| p.to_path_buf())
}

fn starter_template(tier: &str) -> String {
    format!(
        "# Cascade Instructions — {tier_upper}\n\
         \n\
         This file contains AI context instructions for the `{tier}` tier.\n\
         \n\
         Edit this file to add project-specific rules, patterns, and conventions.\n\
         The `CLAUDE.md` and `AGENTS.md` siblings in this directory are symlinks\n\
         that point here — edit only this file.\n\
         \n\
         ## Purpose\n\
         \n\
         (Describe the scope of this cascade tier here.)\n\
         \n\
         ## Rules\n\
         \n\
         (Add rules and conventions below.)\n",
        tier_upper = tier.to_uppercase(),
        tier = tier
    )
}

const GITIGNORE_CONTENT: &str = "\
# AI working memory — not committed to version control.\nmemory/\ndocs/\nideas/\ntemp/\ninbox/\n";
