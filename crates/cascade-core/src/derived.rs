//! Derived-file regeneration — rebuilds tool-specific siblings from CASCADE.md.
//!
//! When CASCADE.md changes, this module regenerates:
//! - `.cursorrules` — plain text for Cursor IDE
//! - `.aider.conf.yml` — minimal YAML stub for Aider

use cascade_types::error::Result;
use std::path::Path;

/// Regenerate all derived files under `cascade_dir` from the given content.
///
/// Non-destructive: only writes if the content has changed.
pub async fn regenerate(cascade_dir: &Path, content: &str) -> Result<()> {
    write_if_changed(&cascade_dir.join(".cursorrules"), content).await?;
    let aider_stub =
        "# Derived from CASCADE.md — do not edit manually.\nread_files:\n  - CASCADE.md\n";
    write_if_changed(&cascade_dir.join(".aider.conf.yml"), aider_stub).await?;
    Ok(())
}

async fn write_if_changed(path: &Path, content: &str) -> Result<()> {
    if let Ok(existing) = tokio::fs::read_to_string(path).await {
        if existing == content {
            return Ok(());
        }
    }
    tokio::fs::write(path, content)
        .await
        .map_err(|e| cascade_types::error::CascadeError::Io {
            path: path.to_path_buf(),
            operation: "write derived file",
            source: e,
        })
}
