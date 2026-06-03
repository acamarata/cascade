//! `cascade link --tool <name>` — create a tool-specific symlink.
//!
//! Creates the appropriate symlink file inside the nearest `.cascade/`
//! directory, pointing to `CASCADE.md`.
//!
//! Supported tools and their symlink names:
//! - `claude`    → `CLAUDE.md`
//! - `opencode`  → `AGENTS.md`
//! - `cursor`    → `.cursorrules`
//! - `aider`     → `.aider.conf.yml`
//! - `codex`     → `AGENTS.md` (same as opencode)
//! - `continue`  → `.continuerc.json` (stub)

use std::path::{Path, PathBuf};

use async_trait::async_trait;
use cascade_types::error::{CascadeError, Result};
use clap::Args;

use super::Command;

/// Arguments for `cascade link`.
#[derive(Debug, Args)]
pub struct LinkArgs {
    /// AI tool to link. Supported: claude, opencode, cursor, aider, codex, continue.
    #[arg(long)]
    pub tool: String,

    /// Cascade directory to create the symlink in. Defaults to nearest `.cascade/`.
    #[arg(long)]
    pub dir: Option<PathBuf>,

    /// Replace an existing file or symlink without asking.
    #[arg(long)]
    pub force: bool,
}

#[async_trait]
impl Command for LinkArgs {
    async fn run(&self) -> Result<()> {
        let cascade_dir = resolve_cascade_dir(self.dir.as_deref())?;
        let link_name = tool_link_name(&self.tool)?;
        let link_path = cascade_dir.join(link_name);
        let target = Path::new("CASCADE.md"); // relative symlink target

        if link_path.exists() {
            if !self.force {
                eprintln!(
                    "error: {} already exists. Pass --force to replace.",
                    link_path.display()
                );
                std::process::exit(1);
            }
            std::fs::remove_file(&link_path).map_err(|e| CascadeError::Io {
                path: link_path.clone(),
                operation: "remove existing link",
                source: e,
            })?;
        }

        create_symlink(target, &link_path)?;
        println!("Linked {} → {}", link_path.display(), target.display());
        Ok(())
    }
}

/// Map a tool name to the symlink filename it expects (public for `unlink`).
pub fn tool_link_name_pub(tool: &str) -> Result<&'static str> {
    tool_link_name(tool)
}

/// Map a tool name to the symlink filename it expects.
fn tool_link_name(tool: &str) -> Result<&'static str> {
    match tool.to_lowercase().as_str() {
        "claude" => Ok("CLAUDE.md"),
        "opencode" | "codex" => Ok("AGENTS.md"),
        "cursor" => Ok(".cursorrules"),
        "aider" => Ok(".aider.conf.yml"),
        "continue" => Ok(".continuerc.json"),
        other => Err(CascadeError::ConfigInvalidValue {
            key: "tool".into(),
            detail: format!("unknown tool '{other}'; supported: claude, opencode, cursor, aider, codex, continue"),
        }),
    }
}

fn resolve_cascade_dir(explicit: Option<&Path>) -> Result<PathBuf> {
    if let Some(dir) = explicit {
        return Ok(dir.to_path_buf());
    }
    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    for ancestor in cwd.ancestors() {
        let c = ancestor.join(".cascade");
        if c.is_dir() {
            return Ok(c);
        }
    }
    // Not found — use current dir (init may not have run yet).
    Ok(cwd.join(".cascade"))
}

fn create_symlink(target: &Path, link: &Path) -> Result<()> {
    #[cfg(unix)]
    std::os::unix::fs::symlink(target, link).map_err(|e| CascadeError::Io {
        path: link.to_path_buf(),
        operation: "create symlink",
        source: e,
    })?;
    #[cfg(windows)]
    std::os::windows::fs::symlink_file(target, link).map_err(|e| CascadeError::Io {
        path: link.to_path_buf(),
        operation: "create symlink",
        source: e,
    })?;
    Ok(())
}
