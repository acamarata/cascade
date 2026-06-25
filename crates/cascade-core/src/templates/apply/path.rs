//! Path canonicalization helper (symlink-safe, non-existent-path-safe).

use std::path::{Path, PathBuf};

/// Return the canonical (symlink-resolved) form of `p`, or `p` itself if
/// canonicalization fails (e.g. the path doesn't exist yet).
pub(super) fn canonical_path(p: &Path) -> PathBuf {
    // For non-existent paths we resolve the longest existing prefix.
    let mut parts = Vec::new();
    let mut current = p.to_path_buf();
    loop {
        match std::fs::canonicalize(&current) {
            Ok(c) => {
                let mut result = c;
                for part in parts.iter().rev() {
                    result = result.join(part);
                }
                return result;
            }
            Err(_) => match current.file_name() {
                Some(name) => {
                    parts.push(name.to_owned());
                    match current.parent() {
                        Some(parent) => current = parent.to_path_buf(),
                        None => break,
                    }
                }
                None => break,
            },
        }
    }
    p.to_path_buf()
}
