//! Path traversal guard for the Cascade runtime.
//!
//! # Purpose
//! Validates that a `&Path` supplied by untrusted input (e.g. a discovered
//! `.cascade/CASCADE.md` path) cannot escape the intended directory tree via
//! `..` components, absolute paths, or null-byte injection.
//!
//! # Inputs
//! `path: &Path` — a relative or absolute path from caller-controlled input.
//!
//! # Outputs
//! `Result<(), SecurityError>` — Ok(()) if safe; Err(SecurityError::*) otherwise.
//!
//! # Constraints
//! Called before any file I/O in `resolution::Resolver::resolve()`.
//! STRIDE: Tampering (T) — prevents path traversal attacks.
//!
//! # SPORT
//! MASTER-SECURITY.md — security/path_guard: validate_cascade_path

use std::path::{Component, Path};
use thiserror::Error;

/// Errors produced by [`validate_cascade_path`].
#[derive(Debug, Error, PartialEq)]
pub enum SecurityError {
    /// The path contains a `..` component that could escape the intended root.
    #[error("path traversal detected: {0}")]
    PathTraversal(String),

    /// The path contains a null byte (`\0`), which some OS calls truncate silently.
    #[error("null byte in path")]
    NullByte,

    /// An absolute path was supplied where only relative paths are accepted.
    #[error("absolute path not permitted")]
    AbsolutePath,
}

/// Validate that `path` is safe to use as a cascade tier path.
///
/// Rejects:
/// - Absolute paths
/// - Paths containing `..` (parent-directory) components
/// - Paths containing null bytes
///
/// # Errors
/// Returns [`SecurityError`] describing the first violation found.
pub fn validate_cascade_path(path: &Path) -> Result<(), SecurityError> {
    if path.is_absolute() {
        return Err(SecurityError::AbsolutePath);
    }

    let s = path.to_string_lossy();
    if s.contains('\0') {
        return Err(SecurityError::NullByte);
    }

    for component in path.components() {
        if component == Component::ParentDir {
            return Err(SecurityError::PathTraversal(s.into_owned()));
        }
    }

    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::Path;

    #[test]
    fn rejects_parent_dir_traversal() {
        let path = Path::new("../../etc/passwd");
        assert!(matches!(
            validate_cascade_path(path),
            Err(SecurityError::PathTraversal(_))
        ));
    }

    #[test]
    fn rejects_complex_traversal() {
        let path = Path::new("foo/../../../bar");
        assert!(matches!(
            validate_cascade_path(path),
            Err(SecurityError::PathTraversal(_))
        ));
    }

    #[test]
    fn rejects_absolute_path() {
        let path = Path::new("/absolute/path");
        assert_eq!(
            validate_cascade_path(path),
            Err(SecurityError::AbsolutePath)
        );
    }

    #[test]
    fn rejects_null_byte() {
        // Build the path string with an embedded null byte.
        let raw = "valid/path\0with_null";
        let path = Path::new(raw);
        assert_eq!(validate_cascade_path(path), Err(SecurityError::NullByte));
    }

    #[test]
    fn accepts_valid_relative_path() {
        let path = Path::new("valid/cascade/path");
        assert_eq!(validate_cascade_path(path), Ok(()));
    }
}
