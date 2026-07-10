//! Structured JSON logging for cascaded via `tracing-subscriber`.
//!
//! Purpose: Initialize a global tracing subscriber that writes newline-
//! delimited JSON to `~/.cascade/logs/daemon.log` with automatic rotation.
//! Console output is also enabled (stderr) filtered to WARN by default so
//! the supervisor can see errors without drowning in JSON.
//!
//! Rotation policy (Q25 scoped — full Sentry integration deferred):
//!   - Rotate at 10 MiB OR 7 days, whichever comes first.
//!   - Keep at most 5 rotated files.
//!   - Rotation is best-effort: failures are logged to stderr and do not
//!     crash the daemon.

use std::path::{Path, PathBuf};
use tracing_subscriber::{
    fmt::{self, time::UtcTime},
    layer::SubscriberExt,
    util::SubscriberInitExt,
    EnvFilter, Layer,
};

const LOG_DIR: &str = "logs";
const LOG_FILE: &str = "daemon.log";
/// Max file size in bytes before rotation.
const ROTATE_BYTES: u64 = 10 * 1024 * 1024; // 10 MiB
/// Max age in seconds before rotation.
const ROTATE_AGE_SECS: u64 = 7 * 24 * 60 * 60; // 7 days
const KEEP_FILES: usize = 5;

/// Initialize the global tracing subscriber. Call once at process start.
///
/// Inputs:
///   `config_log_level`  — optional `[daemon] log_level` from config.toml
///                         (e.g. `"debug"`).  `CASCADE_LOG_LEVEL` env wins when
///                         set, so this acts as the config-tier default only.
///   `config_log_format` — optional `[daemon] log_format` from config.toml
///                         (`"json"` or `"pretty"`; default `"json"`).
///                         The file sink always uses JSON; the format flag affects
///                         the stderr console layer.
/// Outputs: side effect — installs global subscriber.
/// Constraints: must be called before any `tracing::info!` / `error!` macro.
pub fn init_with_config(
    config_log_level: Option<&str>,
    config_log_format: Option<&str>,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let log_dir = log_dir()?;
    std::fs::create_dir_all(&log_dir)?;

    let log_path = log_dir.join(LOG_FILE);

    // Rotate before opening so we don't append to a file that should roll.
    let _ = rotate_if_needed(&log_dir, &log_path);

    let file = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(&log_path)?;

    // ENV wins; config-file level is the fallback; hard default is "info".
    let level_str = if std::env::var("CASCADE_LOG_LEVEL").is_ok() {
        "cascade_log_level_from_env".to_string() // sentinel — use try_from_env below
    } else {
        config_log_level
            .filter(|s| !s.is_empty())
            .unwrap_or("info")
            .to_string()
    };

    let env_filter = if std::env::var("CASCADE_LOG_LEVEL").is_ok() {
        EnvFilter::try_from_env("CASCADE_LOG_LEVEL").unwrap_or_else(|_| EnvFilter::new("info"))
    } else {
        EnvFilter::new(level_str)
    };

    let file_layer = fmt::layer()
        .json()
        .with_timer(UtcTime::rfc_3339())
        .with_target(true)
        .with_writer(std::sync::Mutex::new(file));

    let stderr_layer = fmt::layer()
        .with_target(false)
        .with_writer(std::io::stderr)
        .with_filter(EnvFilter::new("warn"));

    let use_pretty = config_log_format.map(|f| f == "pretty").unwrap_or(false)
        && std::env::var("CASCADE_LOG_LEVEL").is_err(); // env override disables pretty too

    if use_pretty {
        // Pretty console layer for development/debug mode.
        let pretty_stderr = fmt::layer()
            .pretty()
            .with_target(true)
            .with_writer(std::io::stderr)
            .with_filter(EnvFilter::new("debug"));
        tracing_subscriber::registry()
            .with(env_filter)
            .with(file_layer)
            .with(pretty_stderr)
            .init();
    } else {
        tracing_subscriber::registry()
            .with(env_filter)
            .with(file_layer)
            .with(stderr_layer)
            .init();
    }

    Ok(())
}

/// Initialize the global tracing subscriber with defaults.
///
/// Equivalent to `init_with_config(None, None)`.  Kept for callers that don't
/// have a config available yet (e.g. unit tests, early boot before config load).
pub fn init() -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    init_with_config(None, None)
}

/// Rotate `log_path` if it exceeds `ROTATE_BYTES` or `ROTATE_AGE_SECS`.
/// Old rotated files beyond `KEEP_FILES` are removed.
///
/// Rotation is best-effort: any I/O error is printed to stderr and ignored.
pub fn rotate_if_needed(log_dir: &Path, log_path: &Path) -> Result<(), std::io::Error> {
    if !log_path.exists() {
        return Ok(());
    }

    let meta = std::fs::metadata(log_path)?;
    let size_ok = meta.len() < ROTATE_BYTES;
    let age_ok = meta
        .modified()
        .ok()
        .and_then(|m| m.elapsed().ok())
        .map(|age| age.as_secs() < ROTATE_AGE_SECS)
        .unwrap_or(true);

    if size_ok && age_ok {
        return Ok(());
    }

    // Build a timestamped rotation name.
    let ts = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0);
    let rotated = log_dir.join(format!("daemon-{ts}.log"));
    std::fs::rename(log_path, &rotated)?;

    // Prune old rotated files.
    prune_rotated(log_dir)?;
    Ok(())
}

fn prune_rotated(log_dir: &Path) -> Result<(), std::io::Error> {
    let mut rotated: Vec<PathBuf> = std::fs::read_dir(log_dir)?
        .filter_map(|e| e.ok())
        .map(|e| e.path())
        .filter(|p| {
            p.file_name()
                .and_then(|n| n.to_str())
                .map(|n| n.starts_with("daemon-") && n.ends_with(".log"))
                .unwrap_or(false)
        })
        .collect();

    // Sort oldest first (name is timestamp-prefixed so lexicographic works).
    rotated.sort();

    if rotated.len() > KEEP_FILES {
        for old in rotated.iter().take(rotated.len() - KEEP_FILES) {
            let _ = std::fs::remove_file(old);
        }
    }
    Ok(())
}

fn log_dir() -> Result<PathBuf, Box<dyn std::error::Error + Send + Sync>> {
    let home = dirs::home_dir().ok_or("no home directory")?;
    Ok(home.join(".cascade").join(LOG_DIR))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;
    use tempfile::tempdir;

    #[test]
    fn rotation_triggers_on_size() {
        let dir = tempdir().unwrap();
        let path = dir.path().join("daemon.log");
        let mut f = std::fs::File::create(&path).unwrap();
        // Write just over the rotate threshold.
        let chunk = vec![b'x'; 1024];
        for _ in 0..=(ROTATE_BYTES / 1024 + 1) {
            f.write_all(&chunk).unwrap();
        }
        drop(f);

        rotate_if_needed(dir.path(), &path).unwrap();
        assert!(!path.exists(), "original should be rotated away");
    }

    #[test]
    fn rotation_skips_small_file() {
        let dir = tempdir().unwrap();
        let path = dir.path().join("daemon.log");
        std::fs::write(&path, b"small").unwrap();
        rotate_if_needed(dir.path(), &path).unwrap();
        assert!(path.exists(), "small file should not be rotated");
    }
}
