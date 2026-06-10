//! # ipc_pool_register
//!
//! Daemon-side IPC handlers for Gemini Pool key registration/deregistration.
//!
//! ## Purpose
//! Implements the handlers for T-P3-E03-40:
//! - `cascade_pool_register_key`: validate key, append to gemini-keys.json, hot-reload.
//! - `cascade_pool_deregister_key`: remove entry, atomic write, hot-reload.
//!
//! ## Pool config file
//! `~/.cascade/pool/gemini-keys.json` — append-only schema:
//! `{ "keys": [{ "email", "apiKey", "projectId", "addedAt", "enabled" }] }`
//!
//! ## Inputs
//! - `email`, `api_key`, `project_id` for registration.
//! - `email` for deregistration.
//!
//! ## Outputs
//! - `RegisterResult` (tagged JSON).
//!
//! ## Constraints
//! - api_key is never logged.
//! - Pool config writes are atomic (write to .tmp, rename — no window of empty file).
//! - Duplicate email guard: returns `AlreadyRegistered` without modifying the file.

use std::fs;
use std::path::{Path, PathBuf};

use chrono::Utc;

use cascade_providers::validate_gemini_key;
use cascade_types::pool::{GeminiKeyEntry, GeminiPoolConfig, RegisterResult};

// ── Pool config path ──────────────────────────────────────────────────────────

/// Returns the path to `~/.cascade/pool/gemini-keys.json`.
pub fn pool_config_path() -> PathBuf {
    dirs::home_dir()
        .unwrap_or_else(|| PathBuf::from("/tmp"))
        .join(".cascade")
        .join("pool")
        .join("gemini-keys.json")
}

/// Returns the .tmp path for atomic writes.
fn pool_config_tmp_path(base: &Path) -> PathBuf {
    let mut tmp = base.to_path_buf();
    let mut name = tmp.file_name().unwrap_or_default().to_owned();
    name.push(".tmp");
    tmp.set_file_name(name);
    tmp
}

// ── File I/O helpers ──────────────────────────────────────────────────────────

/// Read and parse the pool config from disk. Returns an empty config if absent.
fn read_pool_config(path: &Path) -> Result<GeminiPoolConfig, String> {
    if !path.exists() {
        return Ok(GeminiPoolConfig::default());
    }
    let data =
        fs::read_to_string(path).map_err(|e| format!("Failed to read pool config: {}", e))?;
    serde_json::from_str(&data).map_err(|e| format!("Failed to parse pool config: {}", e))
}

/// Write the pool config atomically via a .tmp file + rename.
fn write_pool_config_atomic(config: &GeminiPoolConfig, path: &Path) -> Result<(), String> {
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent)
            .map_err(|e| format!("Failed to create pool dir: {}", e))?;
    }
    let tmp = pool_config_tmp_path(path);
    let json = serde_json::to_string_pretty(config)
        .map_err(|e| format!("Failed to serialize pool config: {}", e))?;
    fs::write(&tmp, json).map_err(|e| format!("Failed to write tmp: {}", e))?;
    fs::rename(&tmp, path).map_err(|e| format!("Failed to rename tmp: {}", e))?;
    Ok(())
}

// ── Daemon reload ─────────────────────────────────────────────────────────────

/// Send SIGHUP to the pool daemon via PID file at `~/.cascade/daemon.pid`.
/// Silently succeeds if no daemon is running.
#[cfg(unix)]
fn signal_pool_reload() {
    let pid_path = dirs::home_dir()
        .unwrap_or_else(|| PathBuf::from("/tmp"))
        .join(".cascade")
        .join("daemon.pid");
    if let Ok(pid_str) = fs::read_to_string(&pid_path) {
        if let Ok(pid) = pid_str.trim().parse::<i32>() {
            // SAFETY: sending SIGHUP to a process we own; safe to ignore errors.
            unsafe { libc::kill(pid, libc::SIGHUP) };
        }
    }
}

#[cfg(not(unix))]
fn signal_pool_reload() {}

// ── Handler: register ─────────────────────────────────────────────────────────

/// Handle a `cascade_pool_register_key` request.
///
/// # Purpose
/// 1. Reads `~/.cascade/pool/gemini-keys.json` (creates empty config if absent).
/// 2. Checks for duplicate email → returns `AlreadyRegistered` (file unchanged).
/// 3. Calls `validate_gemini_key(api_key)` → returns `InvalidKey` on failure.
/// 4. Appends entry to config, writes atomically (temp+rename).
/// 5. Sends reload signal to P2 pool daemon (SIGHUP via PID file).
/// 6. Returns `RegisterResult::Success`.
///
/// # Inputs
/// - `email`: Google account email.
/// - `api_key`: the API key to register (never logged).
/// - `project_id`: GCP project ID (empty for manually entered keys).
pub async fn handle_pool_register_key(
    email: String,
    api_key: String,
    project_id: String,
) -> RegisterResult {
    handle_pool_register_key_with_path(email, api_key, project_id, &pool_config_path()).await
}

/// Testable variant: accepts an explicit config path.
pub async fn handle_pool_register_key_with_path(
    email: String,
    api_key: String,
    project_id: String,
    config_path: &Path,
) -> RegisterResult {
    let mut config = match read_pool_config(config_path) {
        Ok(c) => c,
        Err(e) => return RegisterResult::Error { message: e },
    };

    // Duplicate email guard — return without touching the file.
    if config.keys.iter().any(|k| k.email == email) {
        return RegisterResult::AlreadyRegistered;
    }

    // Validate key (never log the api_key value).
    match validate_gemini_key(&api_key).await {
        Ok(()) => {}
        Err(e) => return RegisterResult::InvalidKey { message: e.to_string() },
    }

    // Append entry.
    config.keys.push(GeminiKeyEntry {
        email,
        api_key,
        project_id,
        added_at: Utc::now(),
        enabled: true,
    });

    // Atomic write.
    if let Err(e) = write_pool_config_atomic(&config, config_path) {
        return RegisterResult::Error { message: e };
    }

    signal_pool_reload();
    RegisterResult::Success
}

// ── Handler: deregister ───────────────────────────────────────────────────────

/// Handle a `cascade_pool_deregister_key` request.
///
/// # Purpose
/// Filters out the entry with matching `email`, writes atomically, signals reload.
/// Idempotent: returns Success even if the email was not found.
///
/// # Inputs
/// - `email`: Google account email of the key to remove.
pub async fn handle_pool_deregister_key(email: String) -> RegisterResult {
    handle_pool_deregister_key_with_path(email, &pool_config_path()).await
}

/// Testable variant: accepts an explicit config path.
pub async fn handle_pool_deregister_key_with_path(
    email: String,
    config_path: &Path,
) -> RegisterResult {
    let mut config = match read_pool_config(config_path) {
        Ok(c) => c,
        Err(e) => return RegisterResult::Error { message: e },
    };

    config.keys.retain(|k| k.email != email);

    if let Err(e) = write_pool_config_atomic(&config, config_path) {
        return RegisterResult::Error { message: e };
    }

    signal_pool_reload();
    RegisterResult::Success
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    fn make_config_path(tmp: &TempDir) -> PathBuf {
        let pool_dir = tmp.path().join("pool");
        fs::create_dir_all(&pool_dir).unwrap();
        pool_dir.join("gemini-keys.json")
    }

    fn write_entry(config_path: &Path, email: &str) {
        let mut config = read_pool_config(config_path).unwrap();
        config.keys.push(GeminiKeyEntry {
            email: email.to_string(),
            api_key: "AIza-fixture-key".to_string(),
            project_id: "".to_string(),
            added_at: Utc::now(),
            enabled: true,
        });
        write_pool_config_atomic(&config, config_path).unwrap();
    }

    #[tokio::test]
    async fn register_schema_after_direct_write() {
        // Network-free: verify JSON schema shape using direct write helper.
        let tmp = TempDir::new().unwrap();
        let config_path = make_config_path(&tmp);
        write_entry(&config_path, "schema@example.com");

        let config = read_pool_config(&config_path).unwrap();
        assert_eq!(config.keys.len(), 1);
        assert_eq!(config.keys[0].email, "schema@example.com");
        assert!(config.keys[0].enabled);

        let json = fs::read_to_string(&config_path).unwrap();
        // camelCase serialization from serde rename_all = "camelCase"
        assert!(json.contains("apiKey") || json.contains("api_key"),
            "expected apiKey field in JSON: {}", json);
        assert!(json.contains("addedAt") || json.contains("added_at"),
            "expected addedAt field in JSON: {}", json);
    }

    #[tokio::test]
    async fn duplicate_guard_returns_already_registered() {
        let tmp = TempDir::new().unwrap();
        let config_path = make_config_path(&tmp);
        write_entry(&config_path, "dup@example.com");

        // Attempt second registration via handle fn (no network call needed —
        // duplicate check happens before validate_gemini_key).
        let result = handle_pool_register_key_with_path(
            "dup@example.com".to_string(),
            "AIza-any-key".to_string(),
            "".to_string(),
            &config_path,
        )
        .await;

        assert!(
            matches!(result, RegisterResult::AlreadyRegistered),
            "expected AlreadyRegistered, got: {:?}",
            result
        );

        // File should be unchanged.
        let config = read_pool_config(&config_path).unwrap();
        assert_eq!(config.keys.len(), 1, "file should be unmodified");
    }

    #[tokio::test]
    async fn deregister_removes_correct_entry() {
        let tmp = TempDir::new().unwrap();
        let config_path = make_config_path(&tmp);
        write_entry(&config_path, "keep@example.com");
        write_entry(&config_path, "remove@example.com");

        let result =
            handle_pool_deregister_key_with_path("remove@example.com".to_string(), &config_path)
                .await;

        assert!(matches!(result, RegisterResult::Success));
        let config = read_pool_config(&config_path).unwrap();
        assert_eq!(config.keys.len(), 1);
        assert_eq!(config.keys[0].email, "keep@example.com");
    }

    #[tokio::test]
    async fn deregister_nonexistent_is_idempotent() {
        let tmp = TempDir::new().unwrap();
        let config_path = make_config_path(&tmp);
        write_pool_config_atomic(&GeminiPoolConfig::default(), &config_path).unwrap();

        let result =
            handle_pool_deregister_key_with_path("nobody@example.com".to_string(), &config_path)
                .await;

        assert!(matches!(result, RegisterResult::Success));
    }

    #[tokio::test]
    async fn atomic_write_creates_parent_dirs() {
        let tmp = TempDir::new().unwrap();
        // Deeply nested — parent dirs do not exist yet.
        let config_path = tmp.path().join("a").join("b").join("c").join("keys.json");
        let config = GeminiPoolConfig::default();
        write_pool_config_atomic(&config, &config_path).unwrap();
        assert!(config_path.exists());
    }
}
