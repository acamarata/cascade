//! `cascade memory` — read or write `.cascade/memory/` files.
//!
//! Subcommands:
//! - `read <file>` — print a memory file from the active tier
//! - `write <file>` — write stdin (or `--content`) to a memory file

use std::path::PathBuf;

use async_trait::async_trait;
use cascade_types::error::Result;
use clap::{Args, Subcommand};

use super::Command;

/// Arguments for `cascade memory`.
#[derive(Debug, Args)]
pub struct MemoryArgs {
    #[command(subcommand)]
    pub subcommand: MemorySubcmd,
}

#[derive(Debug, Subcommand)]
pub enum MemorySubcmd {
    /// Read a memory file from the active tier's `.cascade/memory/`.
    Read(MemoryReadArgs),
    /// Write content to a memory file (create or append).
    Write(MemoryWriteArgs),
}

#[derive(Debug, Args)]
pub struct MemoryReadArgs {
    /// File name (e.g. `decisions`, `lessons.md`). `.md` is added if missing.
    pub file: String,
    /// Cascade dir to read from. Defaults to the nearest `.cascade/` in CWD.
    #[arg(long)]
    pub dir: Option<PathBuf>,
}

#[derive(Debug, Args)]
pub struct MemoryWriteArgs {
    /// File name to write (e.g. `lessons`, `decisions.md`).
    pub file: String,
    /// Content to write. Reads from stdin when omitted.
    #[arg(long)]
    pub content: Option<String>,
    /// Append to the file instead of overwriting it.
    #[arg(long)]
    pub append: bool,
    /// Cascade dir to write to. Defaults to the nearest `.cascade/` in CWD.
    #[arg(long)]
    pub dir: Option<PathBuf>,
}

#[async_trait]
impl Command for MemoryArgs {
    async fn run(&self) -> Result<()> {
        match &self.subcommand {
            MemorySubcmd::Read(args) => args.run().await,
            MemorySubcmd::Write(args) => args.run().await,
        }
    }
}

#[async_trait]
impl Command for MemoryReadArgs {
    async fn run(&self) -> Result<()> {
        let cascade_dir = resolve_cascade_dir(self.dir.as_deref())?;
        match cascade_core::memory::read(&cascade_dir, &self.file).await? {
            Some(content) => print!("{}", content),
            None => {
                eprintln!("not found: {}/{}.md", cascade_dir.display(), self.file);
                std::process::exit(1);
            }
        }
        Ok(())
    }
}

#[async_trait]
impl Command for MemoryWriteArgs {
    async fn run(&self) -> Result<()> {
        let cascade_dir = resolve_cascade_dir(self.dir.as_deref())?;
        let content = match &self.content {
            Some(c) => c.clone(),
            None => {
                use std::io::Read;
                let mut buf = String::new();
                std::io::stdin().read_to_string(&mut buf).ok();
                buf
            }
        };
        let path =
            cascade_core::memory::write(&cascade_dir, &self.file, &content, self.append).await?;
        println!("Written: {}", path.display());
        Ok(())
    }
}

/// Resolve the `.cascade/` directory to use, walking up from CWD if not given.
fn resolve_cascade_dir(explicit: Option<&std::path::Path>) -> Result<PathBuf> {
    if let Some(dir) = explicit {
        return Ok(dir.to_path_buf());
    }
    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    for ancestor in cwd.ancestors() {
        let candidate = ancestor.join(".cascade");
        if candidate.is_dir() {
            return Ok(candidate);
        }
    }
    // Fall back to $HOME/.cascade/.
    let home = std::env::var("HOME").unwrap_or_else(|_| ".".into());
    Ok(PathBuf::from(home).join(".cascade"))
}
