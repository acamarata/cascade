//! `cascade link --tool <name>` — create a tool-specific symlink.
//!
//! Creates the appropriate symlink file inside the nearest `.cascade/`
//! directory, pointing to `CASCADE.md`.
//!
//! Supported tools and their symlink names:
//! - `claude`    → `CLAUDE.md`
//! - `opencode`  → `AGENTS.md`
//! - `cursor`    → `.cursorrules`
//! - `aider`     → `.aider.md`   (Aider reads this via --read; .aider.conf.yml is tool config, not instructions)
//! - `codex`     → `AGENTS.md` (same as opencode)
//! - `continue`  → `.continue/config.yaml` (Continue reads a YAML config under `.continue/`)

use std::path::{Path, PathBuf};

use async_trait::async_trait;
use cascade_types::error::{CascadeError, Result};
use clap::Args;

use super::Command;

/// Arguments for `cascade link`.
#[derive(Debug, Args)]
pub struct LinkArgs {
    /// AI tool to link. Supported: claude, opencode, cursor, aider, codex, continue.
    /// aider creates `.aider.md` (the instruction file, not `.aider.conf.yml`).
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
        // Relative symlink target, adjusted for any directory component in
        // the link name (e.g. `.continue/config.yaml` sits one level deeper
        // than CASCADE.md, so it must point at `../CASCADE.md`).
        let depth = link_name.matches('/').count();
        let target: PathBuf = ("../".repeat(depth) + "CASCADE.md").into();

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

        // Nested link names (`.continue/config.yaml`) need their parent dir.
        if let Some(parent) = link_path.parent() {
            std::fs::create_dir_all(parent).map_err(|e| CascadeError::Io {
                path: parent.to_path_buf(),
                operation: "create link parent dir",
                source: e,
            })?;
        }

        create_symlink(&target, &link_path)?;
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
        "aider" => Ok(".aider.md"),
        "continue" => Ok(".continue/config.yaml"),
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn tool_link_names_unchanged_for_other_tools() {
        assert_eq!(tool_link_name("claude").unwrap(), "CLAUDE.md");
        assert_eq!(tool_link_name("opencode").unwrap(), "AGENTS.md");
        assert_eq!(tool_link_name("codex").unwrap(), "AGENTS.md");
        assert_eq!(tool_link_name("cursor").unwrap(), ".cursorrules");
        assert_eq!(tool_link_name("aider").unwrap(), ".aider.md");
    }

    #[test]
    fn continue_maps_to_continue_config_yaml() {
        assert_eq!(tool_link_name("continue").unwrap(), ".continue/config.yaml");
    }

    /// `cascade link --tool continue` must produce `.continue/config.yaml`
    /// inside .cascade/, as a symlink that RESOLVES to CASCADE.md content.
    #[cfg(unix)]
    #[tokio::test]
    async fn link_continue_creates_resolving_symlink() {
        let tmp = tempfile::tempdir().unwrap();
        let cascade_dir = tmp.path().join(".cascade");
        std::fs::create_dir_all(&cascade_dir).unwrap();
        std::fs::write(cascade_dir.join("CASCADE.md"), "# cascade\n").unwrap();

        LinkArgs {
            tool: "continue".into(),
            dir: Some(cascade_dir.clone()),
            force: false,
        }
        .run()
        .await
        .unwrap();

        let link = cascade_dir.join(".continue").join("config.yaml");
        assert!(link.exists(), "symlink must resolve to CASCADE.md");
        let content = std::fs::read_to_string(&link).unwrap();
        assert_eq!(content, "# cascade\n");
        assert_eq!(
            std::fs::read_link(&link).unwrap(),
            Path::new("../CASCADE.md"),
            "nested link must point up one level"
        );
    }

    /// The flat tools keep a flat relative target (`CASCADE.md`).
    #[cfg(unix)]
    #[tokio::test]
    async fn link_claude_target_unchanged() {
        let tmp = tempfile::tempdir().unwrap();
        let cascade_dir = tmp.path().join(".cascade");
        std::fs::create_dir_all(&cascade_dir).unwrap();
        std::fs::write(cascade_dir.join("CASCADE.md"), "# cascade\n").unwrap();

        LinkArgs {
            tool: "claude".into(),
            dir: Some(cascade_dir.clone()),
            force: false,
        }
        .run()
        .await
        .unwrap();

        let link = cascade_dir.join("CLAUDE.md");
        assert!(link.exists());
        assert_eq!(std::fs::read_link(&link).unwrap(), Path::new("CASCADE.md"));
    }
}
