//! Memory file access — read/write `.cascade/memory/` files.
//!
//! Memory files are plain markdown with no required frontmatter. Common files:
//! - `decisions.md` — significant technical choices
//! - `lessons.md` — gotchas and learnings
//! - `patterns.md` — codebase conventions

use cascade_types::error::{CascadeError, Result};
use std::path::{Path, PathBuf};

/// Read a memory file from `cascade_dir/memory/{name}.md`.
///
/// Returns `None` if the file does not exist.
pub async fn read(cascade_dir: &Path, name: &str) -> Result<Option<String>> {
    let path = memory_path(cascade_dir, name);
    match tokio::fs::read_to_string(&path).await {
        Ok(content) => Ok(Some(content)),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(None),
        Err(e) => Err(CascadeError::Io {
            path,
            operation: "read memory file",
            source: e,
        }),
    }
}

/// Write content to a memory file, creating it if absent.
///
/// If `append` is `true`, the content is appended to the existing file.
pub async fn write(cascade_dir: &Path, name: &str, content: &str, append: bool) -> Result<PathBuf> {
    let memory_dir = cascade_dir.join("memory");
    tokio::fs::create_dir_all(&memory_dir).await.ok();
    let path = memory_path(cascade_dir, name);
    if append && path.exists() {
        use tokio::io::AsyncWriteExt;
        let mut file = tokio::fs::OpenOptions::new()
            .append(true)
            .open(&path)
            .await
            .map_err(|e| CascadeError::Io {
                path: path.clone(),
                operation: "open memory file for append",
                source: e,
            })?;
        file.write_all(content.as_bytes())
            .await
            .map_err(|e| CascadeError::Io {
                path: path.clone(),
                operation: "write memory file append",
                source: e,
            })?;
    } else {
        tokio::fs::write(&path, content)
            .await
            .map_err(|e| CascadeError::Io {
                path: path.clone(),
                operation: "write memory file",
                source: e,
            })?;
    }
    Ok(path)
}

fn memory_path(cascade_dir: &Path, name: &str) -> PathBuf {
    let filename = if name.ends_with(".md") {
        name.to_string()
    } else {
        format!("{}.md", name)
    };
    cascade_dir.join("memory").join(filename)
}
