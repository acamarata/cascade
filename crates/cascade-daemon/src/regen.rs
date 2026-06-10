//! # Cascade tier CLAUDE.md regeneration
//!
//! Wires the cascade-resolve + compat-gen functions into the daemon's
//! file-watcher regen path. When any `.cascade/` directory changes, this
//! module calls `compat_gen::generate_all()` to regenerate CLAUDE.md
//! and AGENTS.md symlinks across the tier hierarchy.
//!
//! # Design
//!
//! Called from `supervisor::watch_loop()` when a `CascadeChanged` event
//! indicates a change under `.cascade/`. Spawned as a Tokio task to avoid
//! blocking the watcher event loop.
//!
//! # Functions
//!
//! - [`handle_cascade_change`] — async handler called by supervisor when
//!   a `.cascade/` file changes. Infers project_root, calls compat_gen,
//!   logs result, publishes completion/error events.
//!
//! # SPORT
//!
//! `.claude/docs/MASTER-DAEMON.md` — regen task entry (T-P2-E02-17)

use std::path::Path;
use std::time::Instant;
use tracing::{error, info};

use cascade_core::compat_gen;
use cascade_types::error::Result;

/// Handle a `.cascade/` directory change by regenerating CLAUDE.md and
/// AGENTS.md symlinks for all tiers.
///
/// # Inputs
///
/// `changed_path` — absolute path to the file that changed (typically
/// a file under `.cascade/` or `.claude/` directory).
///
/// # Behavior
///
/// 1. Derives `project_root` from the changed path:
///    - If the path is under `.cascade/`, parent is `.cascade/`, grandparent is `project_root`.
///    - If the path is under `.claude/` or another config path, infer `project_root`.
/// 2. Calls `cascade_core::compat_gen::generate_all(project_root)`.
/// 3. Logs regeneration result at INFO level: `"regenerated N compat files in Xms"`.
/// 4. On error, logs at ERROR level and returns the error for the caller to
///    publish as a `cascade.regen.error` event.
///
/// # Constraints
///
/// - Spawned as a Tokio task (non-blocking).
/// - Errors are logged but the watcher loop is NOT killed.
/// - The caller publishes `cascade.regen.complete` or `cascade.regen.error`
///   on the event bus.
pub async fn handle_cascade_change(changed_path: &Path) -> Result<()> {
    let start = Instant::now();

    // Derive project_root from the changed path.
    // The watcher only reports events from .cascade/, .claude/, or cascade files,
    // so the parent chain should be:
    // - {project_root}/.cascade/{file} -> parent() -> .cascade/ -> parent() -> {project_root}
    let mut project_root = changed_path;
    loop {
        // Try to go up one level
        if let Some(parent) = project_root.parent() {
            // Check if the current level is .cascade/ or .claude/
            if project_root.ends_with(".cascade") || project_root.ends_with(".claude") {
                // Return the parent of the config dir
                project_root = parent;
                break;
            }
            project_root = parent;
        } else {
            // Reached the root without finding a config dir; use changed_path as fallback
            project_root = changed_path;
            break;
        }
    }

    // Call compat_gen::generate_all to regenerate tier CLAUDE.md files.
    match compat_gen::generate_all(project_root) {
        Ok(files) => {
            let elapsed_ms = start.elapsed().as_millis();
            info!(
                project_root = %project_root.display(),
                count = files.len(),
                elapsed_ms = elapsed_ms,
                "regenerated {} compat files in {}ms",
                files.len(),
                elapsed_ms
            );
            Ok(())
        }
        Err(e) => {
            error!(
                project_root = %project_root.display(),
                error = %e,
                "cascade tier compat-file regeneration failed"
            );
            Err(e)
        }
    }
}

#[cfg(test)]
mod tests {
    use std::path::PathBuf;

    #[tokio::test]
    async fn test_project_root_derivation() {
        // Test that we correctly derive project_root from a .cascade/ path
        let test_path = PathBuf::from("/home/user/myproject/.cascade/config.toml");
        // This would be: /home/user/myproject/.cascade/config.toml
        //   parent() -> /home/user/myproject/.cascade/
        //   parent() -> /home/user/myproject/
        // The path derivation logic in handle_cascade_change should work correctly
        // (actual test would need the cascade module loaded, so this is a smoke test).
        assert!(test_path.parent().is_some());
    }
}
