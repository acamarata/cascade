//! ONNX model cache directory resolver for cascade-rag.
//!
//! # Purpose
//!
//! Resolves the on-disk cache directory for local ONNX embedding model
//! artifacts. The actual HTTP download is delegated to fastembed-rs
//! (`InitOptions::with_cache_dir`); this module only answers two questions:
//!
//! 1. Where should the models live? (`model_cache_dir`)
//! 2. Is a specific model already cached? (`is_model_cached`)
//!
//! # Inputs / Outputs
//!
//! - `model_cache_dir() -> Result<PathBuf, ModelCacheError>` — base cache dir.
//! - `is_model_cached(model_id: &str) -> bool` — true if the model dir exists.
//!
//! # Constraints
//!
//! - Paths are always HOME-confined: `$CASCADE_MODEL_DIR` or
//!   `~/.cascade/models/`.  No absolute paths outside HOME are accepted.
//! - Offline: if neither the env var nor a home dir is resolvable, returns
//!   `ModelCacheError::HomeNotFound`.
//! - Tests use `env::set_var` in a single-threaded context with a temp dir
//!   to avoid touching the real HOME.
//!
//! # SPORT
//!
//! Entity: cascade-rag crate
//! Task: T-P4-E01-07

use std::{env, path::PathBuf};

use thiserror::Error;

/// The environment variable that overrides the default model cache location.
pub const CASCADE_MODEL_DIR_ENV: &str = "CASCADE_MODEL_DIR";

// ── Error type ────────────────────────────────────────────────────────────────

/// Errors returned by the model-cache resolver.
#[derive(Debug, Error)]
pub enum ModelCacheError {
    /// The model directory could not be determined because neither
    /// `CASCADE_MODEL_DIR` is set nor a home directory is available.
    #[error("HOME directory not found; set CASCADE_MODEL_DIR to override")]
    HomeNotFound,

    /// The requested model is not present in the cache directory.
    /// The caller should trigger a download (e.g. via fastembed InitOptions).
    #[error(
        "model '{model_id}' is not cached; expected at {cache_dir}",
        cache_dir = cache_dir.display()
    )]
    ModelNotDownloaded {
        /// The model identifier (e.g. `"intfloat/multilingual-e5-large"`).
        model_id: String,
        /// The cache directory where the model is expected.
        cache_dir: PathBuf,
    },

    /// The model cache directory is a symlink whose target is unreadable — most
    /// commonly because the volume it points at (e.g. an external drive) is
    /// temporarily unmounted. Erroring here is deliberate: the alternative is
    /// that the dir *looks* empty, which would kick off a redundant multi-GB
    /// re-download into a temp dir that gets abandoned when the volume returns.
    #[error(
        "model cache dir {path} is a symlink to an unreadable target ({target}) — \
         the volume may be unmounted; refusing to re-download",
        path = path.display(),
        target = target.display()
    )]
    CacheDirUnreadable {
        /// The symlink path (e.g. `~/.cascade/models`).
        path: PathBuf,
        /// The (unreadable) symlink target.
        target: PathBuf,
    },
}

// ── Public API ────────────────────────────────────────────────────────────────

/// Return the base model cache directory.
///
/// Resolution order:
/// 1. `$CASCADE_MODEL_DIR` (if set and non-empty)
/// 2. `~/.cascade/models/`
///
/// The directory is **not** created by this function; callers that want to
/// ensure the directory exists must call `std::fs::create_dir_all` themselves.
///
/// # Errors
///
/// Returns [`ModelCacheError::HomeNotFound`] when `CASCADE_MODEL_DIR` is unset
/// **and** `dirs::home_dir()` returns `None` (e.g. sandboxed environments with
/// no `HOME`).
pub fn model_cache_dir() -> Result<PathBuf, ModelCacheError> {
    // 1. Env override wins.
    if let Ok(val) = env::var(CASCADE_MODEL_DIR_ENV) {
        if !val.is_empty() {
            return Ok(PathBuf::from(val));
        }
    }

    // 2. Fall back to ~/.cascade/models/.
    let home = dirs::home_dir().ok_or(ModelCacheError::HomeNotFound)?;
    Ok(home.join(".cascade").join("models"))
}

/// Return `true` if `<model_cache_dir>/<model_id>/` exists on disk.
///
/// A `false` return means the caller should trigger a fastembed download via
/// `InitOptions::with_cache_dir(model_cache_dir()?)`.
///
/// A directory being present is treated as "cached" — fastembed manages the
/// individual file layout inside the model directory.
///
/// If `model_cache_dir()` itself fails (e.g. no HOME), this returns `false`
/// rather than propagating the error, so callers can unconditionally call it
/// as a cheap guard.
pub fn is_model_cached(model_id: &str) -> bool {
    match model_cache_dir() {
        Ok(base) => base.join(model_id).exists(),
        Err(_) => false,
    }
}

/// Prepare the model cache directory for use, failing loudly on a
/// dangling/unreadable symlink instead of silently triggering a re-download.
///
/// Behaviour:
///
/// 1. If `dir` is a symlink, verify its target is readable (i.e. the volume is
///    mounted). A dangling symlink (unmounted external drive) returns
///    [`ModelCacheError::CacheDirUnreadable`] rather than being treated as an
///    empty cache — which is what triggered the abandoned multi-GB `.part`
///    downloads under `/private/var/folders/.../T/`.
/// 2. Otherwise `create_dir_all` the directory. An `AlreadyExists` error (the
///    common case when `dir` is a symlink pointing at an existing directory, or
///    the dir already exists) is treated as success — a plain `create_dir_all`
///    was returning `File exists (os error 17)` and aborting model init.
///
/// Callers should invoke this before handing the path to fastembed's
/// `with_cache_dir`.
pub fn ensure_cache_dir_ready(dir: &std::path::Path) -> Result<(), ModelCacheError> {
    // A symlink whose target does not resolve == unmounted/unreadable volume.
    // `symlink_metadata` inspects the link itself; `metadata` follows it.
    if let Ok(link_meta) = std::fs::symlink_metadata(dir) {
        if link_meta.file_type().is_symlink() {
            let target = std::fs::read_link(dir).unwrap_or_else(|_| dir.to_path_buf());
            // Following the link fails when the target volume is gone.
            if std::fs::metadata(dir).is_err() {
                return Err(ModelCacheError::CacheDirUnreadable {
                    path: dir.to_path_buf(),
                    target,
                });
            }
            // Target is readable: the (symlinked) directory already exists, so
            // there is nothing to create. Returning here avoids the EEXIST that
            // `create_dir_all` raises on some platforms for symlink-to-dir.
            return Ok(());
        }
    }

    match std::fs::create_dir_all(dir) {
        Ok(()) => Ok(()),
        // A pre-existing directory is fine; only surface other IO errors.
        Err(e) if e.kind() == std::io::ErrorKind::AlreadyExists => Ok(()),
        Err(e) => Err(ModelCacheError::CacheDirUnreadable {
            path: dir.to_path_buf(),
            target: std::io::Error::to_string(&e).into(),
        }),
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::env;

    // ── env override ─────────────────────────────────────────────────────────

    /// `model_cache_dir()` respects `CASCADE_MODEL_DIR` env override.
    #[test]
    fn model_cache_dir_respects_env_override() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let dir = tempfile::tempdir().unwrap();
        let expected = dir.path().to_path_buf();

        // Safety: single-threaded test environment.
        unsafe {
            env::set_var(CASCADE_MODEL_DIR_ENV, &expected);
        }
        let result = model_cache_dir().unwrap();
        unsafe {
            env::remove_var(CASCADE_MODEL_DIR_ENV);
        }

        assert_eq!(result, expected, "must return the env-overridden path");
    }

    /// `model_cache_dir()` defaults to `~/.cascade/models/` when env is unset.
    #[test]
    fn model_cache_dir_defaults_to_home_cascade_models() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        // Remove the override if it happens to be set.
        unsafe {
            env::remove_var(CASCADE_MODEL_DIR_ENV);
        }

        let result = model_cache_dir().unwrap();
        let result_str = result.to_string_lossy();

        assert!(
            result_str.contains(".cascade") && result_str.contains("models"),
            "default path must contain .cascade/models; got {result_str}"
        );
        assert!(result.is_absolute(), "path must be absolute");
    }

    // ── is_model_cached ───────────────────────────────────────────────────────

    /// `is_model_cached` returns `false` for a non-existent model directory.
    #[test]
    fn is_model_cached_returns_false_for_missing_dir() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let dir = tempfile::tempdir().unwrap();
        // Point cache at a temp dir that has NO model subdirs.
        unsafe {
            env::set_var(CASCADE_MODEL_DIR_ENV, dir.path());
        }

        let cached = is_model_cached("BAAI/bge-m3");

        unsafe {
            env::remove_var(CASCADE_MODEL_DIR_ENV);
        }

        assert!(!cached, "non-existent model dir must return false");
    }

    /// `is_model_cached` returns `true` when the model directory exists.
    #[test]
    fn is_model_cached_returns_true_when_dir_exists() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let dir = tempfile::tempdir().unwrap();
        let model_dir = dir.path().join("BAAI/bge-m3");
        std::fs::create_dir_all(&model_dir).unwrap();

        unsafe {
            env::set_var(CASCADE_MODEL_DIR_ENV, dir.path());
        }

        let cached = is_model_cached("BAAI/bge-m3");

        unsafe {
            env::remove_var(CASCADE_MODEL_DIR_ENV);
        }

        assert!(cached, "existing model dir must return true");
    }

    // ── ModelNotDownloaded error ──────────────────────────────────────────────

    /// `ModelCacheError::ModelNotDownloaded` carries model_id and cache_dir.
    #[test]
    fn model_not_downloaded_error_carries_context() {
        let cache_dir = PathBuf::from("/tmp/.cascade/models");
        let err = ModelCacheError::ModelNotDownloaded {
            model_id: "BAAI/bge-m3".to_string(),
            cache_dir: cache_dir.clone(),
        };
        let msg = err.to_string();
        assert!(
            msg.contains("BAAI/bge-m3"),
            "error message must contain model_id; got: {msg}"
        );
        assert!(
            msg.contains("/tmp/.cascade/models"),
            "error message must contain cache_dir; got: {msg}"
        );
    }

    // ── ensure_cache_dir_ready ────────────────────────────────────────────────

    /// A normal (non-existent) directory is created successfully.
    #[test]
    fn ensure_ready_creates_missing_dir() {
        let dir = tempfile::tempdir().unwrap();
        let target = dir.path().join("models");
        assert!(!target.exists());
        ensure_cache_dir_ready(&target).expect("should create missing dir");
        assert!(target.is_dir(), "dir must exist after ensure_ready");
    }

    /// A symlink pointing at an existing directory succeeds WITHOUT an EEXIST
    /// error (regression: plain create_dir_all raised `File exists (os error 17)`
    /// on a symlink-to-dir, aborting model init).
    #[cfg(unix)]
    #[test]
    fn ensure_ready_symlink_to_existing_dir_is_ok() {
        let dir = tempfile::tempdir().unwrap();
        let real = dir.path().join("real-models");
        std::fs::create_dir_all(&real).unwrap();
        let link = dir.path().join("models-link");
        std::os::unix::fs::symlink(&real, &link).unwrap();

        ensure_cache_dir_ready(&link).expect("symlink-to-existing-dir must be OK");
    }

    /// A dangling symlink (target missing — models on an unmounted volume) errors
    /// with `CacheDirUnreadable` instead of being treated as an empty cache that
    /// triggers a redundant re-download.
    #[cfg(unix)]
    #[test]
    fn ensure_ready_dangling_symlink_errors() {
        let dir = tempfile::tempdir().unwrap();
        let missing_target = dir.path().join("unmounted-volume").join("models");
        let link = dir.path().join("models-link");
        // Symlink to a path that does not exist → simulates an unmounted volume.
        std::os::unix::fs::symlink(&missing_target, &link).unwrap();

        let err = ensure_cache_dir_ready(&link)
            .expect_err("dangling symlink must error, not silently re-download");
        assert!(
            matches!(err, ModelCacheError::CacheDirUnreadable { .. }),
            "expected CacheDirUnreadable, got: {err:?}"
        );
    }

    // ── empty env var falls through to HOME ───────────────────────────────────

    /// An empty `CASCADE_MODEL_DIR` falls through to the HOME default.
    #[test]
    fn empty_env_var_falls_through_to_home() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        unsafe {
            env::set_var(CASCADE_MODEL_DIR_ENV, "");
        }

        let result = model_cache_dir().unwrap();
        let s = result.to_string_lossy();

        unsafe {
            env::remove_var(CASCADE_MODEL_DIR_ENV);
        }

        assert!(
            s.contains(".cascade") && s.contains("models"),
            "empty env var must fall through to ~/.cascade/models; got {s}"
        );
    }
}
