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

    /// The resolved model cache directory lives inside the OS temp directory,
    /// which is never a valid home for multi-GB model weights.
    ///
    /// This is the guard for the T-P7-E25-18 disk leak: tests routinely set
    /// `HOME` to a `tempfile::TempDir` for filesystem isolation, and because
    /// [`model_cache_dir`] falls back to `$HOME/.cascade/models`, that silently
    /// redirects every model download into a throwaway directory. Each such run
    /// re-fetched ~2 GB (a fresh temp `HOME` is always an empty cache) and left
    /// it behind whenever the directory was still open at drop time.
    #[error(
        "model cache dir {cache_dir} is inside the OS temp directory — refusing to \
         download model weights into ephemeral storage; set CASCADE_MODEL_DIR to a \
         persistent path, or CASCADE_ALLOW_TEMP_MODEL_DIR=1 to override",
        cache_dir = cache_dir.display()
    )]
    EphemeralCacheDir {
        /// The offending (temp-resident) cache directory.
        cache_dir: PathBuf,
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

/// Environment variable that opts back in to downloading models into a cache
/// directory that lives under the OS temp dir. Off by default.
pub const CASCADE_ALLOW_TEMP_MODEL_DIR_ENV: &str = "CASCADE_ALLOW_TEMP_MODEL_DIR";

/// Refuse to download model weights into ephemeral (OS-temp-resident) storage.
///
/// Call this immediately before handing a path to fastembed's `with_cache_dir`.
///
/// # Why
///
/// fastembed owns the download and its `.part` staging; Cascade only chooses
/// the destination. So the leak fixed here is not a missing cleanup guard on a
/// staging path we control — it is Cascade handing fastembed a *throwaway*
/// destination in the first place. 176 call sites across the workspace set
/// `HOME` to a `tempfile::TempDir` for test isolation, and [`model_cache_dir`]
/// falls back to `$HOME/.cascade/models`, so those runs pointed multi-GB
/// downloads at a directory nobody intended to keep. Refusing up front removes
/// both the wasted re-download and the orphan it leaves behind.
///
/// Downloading gigabytes into the temp dir is never correct in production
/// either, so this guard is unconditional rather than test-only, with
/// [`CASCADE_ALLOW_TEMP_MODEL_DIR_ENV`] as the deliberate escape hatch.
///
/// # Errors
///
/// [`ModelCacheError::EphemeralCacheDir`] when `dir` is inside
/// [`std::env::temp_dir`] and the override env var is unset.
pub fn ensure_persistent_cache_dir(dir: &std::path::Path) -> Result<(), ModelCacheError> {
    if let Ok(val) = env::var(CASCADE_ALLOW_TEMP_MODEL_DIR_ENV) {
        if !val.is_empty() && val != "0" {
            return Ok(());
        }
    }

    if is_under_temp_dir(dir) {
        return Err(ModelCacheError::EphemeralCacheDir {
            cache_dir: dir.to_path_buf(),
        });
    }
    Ok(())
}

/// Return `true` when `dir` is the OS temp dir or nested inside it.
///
/// Both sides are canonicalised where possible so that macOS's `/var` ->
/// `/private/var` symlink does not defeat the comparison — `env::temp_dir()`
/// reports `/var/folders/...` while the real path is `/private/var/folders/...`.
fn is_under_temp_dir(dir: &std::path::Path) -> bool {
    let tmp = env::temp_dir();
    let canon_tmp = std::fs::canonicalize(&tmp).unwrap_or(tmp);

    // The dir may not exist yet, so canonicalize the nearest existing ancestor
    // and re-attach the remainder.
    let canon_dir = canonicalize_lossy(dir);
    canon_dir.starts_with(&canon_tmp)
}

/// Canonicalise as much of `path` as exists, leaving the rest untouched.
fn canonicalize_lossy(path: &std::path::Path) -> PathBuf {
    if let Ok(p) = std::fs::canonicalize(path) {
        return p;
    }
    let mut existing = path;
    let mut trailing: Vec<&std::ffi::OsStr> = Vec::new();
    while let Some(parent) = existing.parent() {
        if let Some(name) = existing.file_name() {
            trailing.push(name);
        }
        if let Ok(p) = std::fs::canonicalize(parent) {
            let mut out = p;
            for part in trailing.iter().rev() {
                out.push(part);
            }
            return out;
        }
        existing = parent;
    }
    path.to_path_buf()
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::env;

    // ── T-P7-E25-18: ephemeral cache-dir guard ───────────────────────────────

    /// The negative the ticket asks for: a temp-resident cache dir is REFUSED,
    /// so no download is ever started and no orphan can be left behind.
    #[test]
    fn download_refused_when_cache_dir_is_under_temp() {
        let tmp = tempfile::tempdir().expect("tempdir");
        // Mirror the real failure shape: $TMPDIR/.tmpXXXX/.cascade/models
        let cache_dir = tmp.path().join(".cascade").join("models");

        let err = ensure_persistent_cache_dir(&cache_dir)
            .expect_err("temp-resident cache dir must be refused");

        match err {
            ModelCacheError::EphemeralCacheDir { cache_dir: got } => {
                assert_eq!(got, cache_dir);
            }
            other => panic!("expected EphemeralCacheDir, got {other:?}"),
        }
    }

    /// The guard must fire for a path that does not exist yet — the download
    /// destination is normally created only after this check passes.
    #[test]
    fn guard_fires_for_not_yet_created_temp_path() {
        let tmp = tempfile::tempdir().expect("tempdir");
        let missing = tmp
            .path()
            .join("does")
            .join("not")
            .join("exist")
            .join("models");
        assert!(!missing.exists());
        assert!(ensure_persistent_cache_dir(&missing).is_err());
    }

    /// A persistent (non-temp) cache dir is allowed — no behaviour change for
    /// the real `~/.cascade/models` path.
    #[test]
    fn persistent_cache_dir_is_allowed() {
        let home_like = PathBuf::from("/opt/cascade-models-test-path");
        assert!(ensure_persistent_cache_dir(&home_like).is_ok());
    }

    /// The documented escape hatch re-enables temp-resident downloads.
    #[test]
    #[serial_test::serial(env_model_dir)]
    fn override_env_allows_temp_cache_dir() {
        let tmp = tempfile::tempdir().expect("tempdir");
        let cache_dir = tmp.path().join(".cascade").join("models");

        // SAFETY: guarded by #[serial] so no other test reads this var concurrently.
        env::set_var(CASCADE_ALLOW_TEMP_MODEL_DIR_ENV, "1");
        let allowed = ensure_persistent_cache_dir(&cache_dir);
        env::remove_var(CASCADE_ALLOW_TEMP_MODEL_DIR_ENV);

        assert!(allowed.is_ok(), "override must permit temp cache dir");
        // And with the override cleared it is refused again.
        assert!(ensure_persistent_cache_dir(&cache_dir).is_err());
    }

    /// `0` and empty are not treated as opting in.
    #[test]
    #[serial_test::serial(env_model_dir)]
    fn override_env_zero_or_empty_does_not_opt_in() {
        let tmp = tempfile::tempdir().expect("tempdir");
        let cache_dir = tmp.path().join(".cascade").join("models");

        for val in ["0", ""] {
            env::set_var(CASCADE_ALLOW_TEMP_MODEL_DIR_ENV, val);
            let got = ensure_persistent_cache_dir(&cache_dir);
            env::remove_var(CASCADE_ALLOW_TEMP_MODEL_DIR_ENV);
            assert!(got.is_err(), "value {val:?} must not opt in");
        }
    }

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
