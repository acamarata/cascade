//! `cascade unlink --tool <name>` — remove a tool-specific symlink.
//!
//! Removes the symlink created by `cascade link`. Only removes the symlink
//! file; never removes `CASCADE.md` itself.

use std::path::{Path, PathBuf};

use async_trait::async_trait;
use clap::Args;
use cascade_types::error::{CascadeError, Result};

use super::Command;

/// Arguments for `cascade unlink`.
#[derive(Debug, Args)]
pub struct UnlinkArgs {
    /// AI tool to unlink. Same names as `cascade link --tool`.
    #[arg(long)]
    pub tool: String,

    /// Cascade directory containing the symlink. Defaults to nearest `.cascade/`.
    #[arg(long)]
    pub dir: Option<PathBuf>,
}

#[async_trait]
impl Command for UnlinkArgs {
    async fn run(&self) -> Result<()> {
        let cascade_dir = resolve_cascade_dir(self.dir.as_deref())?;
        let link_name = crate::cmd::link::tool_link_name_pub(&self.tool)?;
        let link_path = cascade_dir.join(link_name);

        if !link_path.exists() {
            eprintln!("not found: {}", link_path.display());
            std::process::exit(1);
        }
        if !link_path.is_symlink() {
            eprintln!(
                "error: {} is not a symlink — refusing to remove a regular file",
                link_path.display()
            );
            std::process::exit(1);
        }

        std::fs::remove_file(&link_path).map_err(|e| CascadeError::Io {
            path: link_path.clone(),
            operation: "remove symlink",
            source: e,
        })?;
        println!("Unlinked: {}", link_path.display());
        Ok(())
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
    Ok(cwd.join(".cascade"))
}
