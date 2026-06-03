//! Symlink management — create and verify CLAUDE.md / AGENTS.md siblings.
//!
//! Every `.cascade/` directory should have:
//! - `CLAUDE.md` → symlink to `CASCADE.md`
//! - `AGENTS.md` → symlink to `CASCADE.md`
//!
//! These allow Claude Code and OpenCode to load cascade instructions without
//! knowing the `.cascade/` directory structure.

use cascade_types::error::{CascadeError, Result};
use cascade_types::paths::{AGENTS_MD_NAME, CASCADE_MD_NAME, CLAUDE_MD_NAME};
use std::path::Path;

/// Create the CLAUDE.md and AGENTS.md symlinks inside `cascade_dir`.
///
/// Non-destructive: skips a symlink if it already exists and is valid.
/// Set `force` to `true` to replace broken or stale symlinks.
pub fn create_siblings(cascade_dir: &Path, force: bool) -> Result<()> {
    let target = std::path::Path::new(CASCADE_MD_NAME); // relative target
    for name in [CLAUDE_MD_NAME, AGENTS_MD_NAME] {
        let link = cascade_dir.join(name);
        if link.exists() {
            if !force {
                continue;
            }
            std::fs::remove_file(&link).map_err(|e| CascadeError::Io {
                path: link.clone(),
                operation: "remove stale symlink",
                source: e,
            })?;
        }
        #[cfg(unix)]
        std::os::unix::fs::symlink(target, &link).map_err(|e| CascadeError::Io {
            path: link.clone(),
            operation: "create unix symlink",
            source: e,
        })?;
        #[cfg(windows)]
        std::os::windows::fs::symlink_file(target, &link).map_err(|e| CascadeError::Io {
            path: link.clone(),
            operation: "create windows symlink",
            source: e,
        })?;
    }
    Ok(())
}

/// Verify that `cascade_dir/CLAUDE.md` and `cascade_dir/AGENTS.md` are
/// valid symlinks pointing to `CASCADE.md`.
///
/// Returns a list of issues; an empty list means all siblings are healthy.
pub fn verify_siblings(cascade_dir: &Path) -> Vec<String> {
    let mut issues = Vec::new();
    for name in [CLAUDE_MD_NAME, AGENTS_MD_NAME] {
        let link = cascade_dir.join(name);
        if !link.exists() {
            issues.push(format!("{} is missing", name));
        } else if !link.is_symlink() {
            issues.push(format!("{} exists but is not a symlink", name));
        } else if let Ok(dest) = std::fs::read_link(&link) {
            if dest != std::path::Path::new(CASCADE_MD_NAME) {
                issues.push(format!(
                    "{} points to {:?}, expected {}",
                    name, dest, CASCADE_MD_NAME
                ));
            }
        }
    }
    issues
}
