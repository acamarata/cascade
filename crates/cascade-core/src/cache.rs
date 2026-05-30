//! Resolution cache — persists merged cascade text to disk.
//!
//! Writes the resolved cascade to `.cascade/temp/.resolved-cascade.md` so
//! downstream tools (daemon, IDE extensions) can read it without re-walking
//! the filesystem on every invocation.

use std::path::Path;

use cascade_types::error::Result;

use crate::resolution::ResolvedCascade;

/// Write a resolved cascade to the temp cache file for the given root.
pub async fn write(root: &Path, resolved: &ResolvedCascade) -> Result<()> {
    use cascade_types::paths::{CASCADE_DIR_NAME, subdirs};
    let temp_dir = root.join(CASCADE_DIR_NAME).join(subdirs::TEMP);
    tokio::fs::create_dir_all(&temp_dir).await.ok();
    let cache_path = temp_dir.join(".resolved-cascade.md");
    tokio::fs::write(&cache_path, &resolved.merged_text).await.map_err(|e| {
        cascade_types::error::CascadeError::Io {
            path: cache_path.clone(),
            operation: "write resolved cascade cache",
            source: e,
        }
    })
}

/// Read the cached resolved cascade text, if present.
pub async fn read(root: &Path) -> Option<String> {
    use cascade_types::paths::{CASCADE_DIR_NAME, subdirs};
    let path = root
        .join(CASCADE_DIR_NAME)
        .join(subdirs::TEMP)
        .join(".resolved-cascade.md");
    tokio::fs::read_to_string(&path).await.ok()
}
