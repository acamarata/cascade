//! Daemon runtime configuration.
//!
//! Purpose: Full `~/.cascade/config.toml` schema with serde-based parsing,
//! typed defaults, and field-range validation. Covers daemon, watcher,
//! quota_poller, project_poller, and history sections.
//!
//! Inputs:  config directory path (usually `~/.cascade`)
//! Outputs: `Config` (owned, cloned where needed -- not behind Arc)
//! Constraints: no global state; no hot-reload (file-watch handled elsewhere)
//! SPORT: .claude/docs/MASTER-DAEMON.md -- Config struct + T-P2-E02-01

use cascade_types::HookConfigEntry;
use serde::{Deserialize, Serialize};
use std::path::Path;

// -- Error type ----------------------------------------------------------------

/// Errors returned by `Config::load`.
#[derive(Debug, thiserror::Error)]
pub enum ConfigError {
    /// Home directory cannot be determined from the environment.
    #[error("cannot determine home directory")]
    MissingHome,
    /// Failed to read the config.toml file.
    #[error("failed to read config.toml: {0}")]
    Io(#[from] std::io::Error),
    /// TOML parse error.
    #[error("config.toml parse error: {0}")]
    Parse(#[from] toml::de::Error),
    /// A field value is outside the allowed range or set.
    #[error("invalid config value: field={field} constraint={constraint}")]
    InvalidValue {
        /// Dotted field name, e.g. "watcher.debounce_ms".
        field: String,
        /// Human-readable constraint description.
        constraint: String,
    },
}

// -- Sub-config structs --------------------------------------------------------

/// `[daemon]` section of config.toml.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default)]
pub struct DaemonConfig {
    /// Tracing filter string; default "info".
    pub log_level: String,
    /// Log format: "json" | "pretty"; default "json".
    pub log_format: String,
    /// Override for `~/.cascade/daemon.sock`; empty = use default.
    pub socket_path: String,
    /// How often the health-check sampler fires (seconds); default 30.
    pub health_sample_interval_secs: u64,
    /// How often the event bus is flushed (seconds); default 60.
    pub event_bus_flush_interval_secs: u64,
}

impl Default for DaemonConfig {
    fn default() -> Self {
        Self {
            log_level: "info".into(),
            log_format: "json".into(),
            socket_path: String::new(),
            health_sample_interval_secs: 30,
            event_bus_flush_interval_secs: 60,
        }
    }
}

/// `[watcher]` section of config.toml.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default)]
pub struct WatcherConfig {
    /// File-change debounce window (ms); default 200; range 10..=5000.
    pub debounce_ms: u64,
    /// Watch `~/.cascade/CASCADE.md`; default true.
    pub watch_global_cascade: bool,
    /// Watch per-project `.cascade/CASCADE.md` files; default true.
    pub watch_projects: bool,
    /// Explicit project root dirs to watch; empty = auto-discover.
    pub project_roots: Vec<String>,
}

impl Default for WatcherConfig {
    fn default() -> Self {
        Self {
            debounce_ms: 200,
            watch_global_cascade: true,
            watch_projects: true,
            project_roots: vec![],
        }
    }
}

/// `[quota_poller]` section of config.toml.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default)]
pub struct QuotaPollerConfig {
    /// Enable Gemini quota polling; default true.
    pub enabled: bool,
    /// Poll interval (seconds); default 60; range 10..=3600.
    pub interval_secs: u64,
    /// Gemini pool proxy URL; default "http://localhost:3761".
    pub proxy_url: String,
}

impl Default for QuotaPollerConfig {
    fn default() -> Self {
        Self {
            enabled: true,
            interval_secs: 60,
            proxy_url: "http://localhost:3761".into(),
        }
    }
}

/// `[project_poller]` section of config.toml.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default)]
pub struct ProjectPollerConfig {
    /// Enable project-state polling; default true.
    pub enabled: bool,
    /// Poll interval (seconds); default 30.
    pub interval_secs: u64,
}

impl Default for ProjectPollerConfig {
    fn default() -> Self {
        Self {
            enabled: true,
            interval_secs: 30,
        }
    }
}

/// `[history]` section of config.toml.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default)]
pub struct HistoryConfig {
    /// Days to keep quota_history rows; 0 = keep forever; default 90; range 0..=36500.
    pub retention_days: u64,
}

impl Default for HistoryConfig {
    fn default() -> Self {
        Self { retention_days: 90 }
    }
}

/// `[scheduler]` section of config.toml.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default)]
pub struct SchedulerConfig {
    /// Enable the scheduled-task execution engine; default true.
    pub enabled: bool,
    /// How often the scheduler polls for due tasks (seconds); default 30.
    pub poll_interval_secs: u64,
    /// Maximum number of tasks that may run concurrently; default 4.
    pub max_parallel: usize,
}

impl Default for SchedulerConfig {
    fn default() -> Self {
        Self {
            enabled: true,
            poll_interval_secs: 30,
            max_parallel: 4,
        }
    }
}

/// `[quota_store]` section of config.toml.
///
/// Controls the persistent quota store that aggregates quota snapshots from
/// the daemon poller.
///
/// # TOML example
///
/// ```toml
/// [quota_store]
/// max_snapshots = 1000
/// path = "~/.cascade/quota-store.json"
/// ```
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default)]
pub struct QuotaStoreConfig {
    /// Maximum number of in-memory quota snapshots; default 1000.
    /// The quota poller maintains a VecDeque bounded to this size.
    pub max_snapshots: usize,
    /// Path to the quota-store.json file; tilde-expanded at load; default `~/.cascade/quota-store.json`.
    pub path: String,
}

impl Default for QuotaStoreConfig {
    fn default() -> Self {
        Self {
            max_snapshots: 1000,
            path: "~/.cascade/quota-store.json".into(),
        }
    }
}

/// `[proxy]` section of config.toml.
///
/// Controls the Gemini proxy's 429 retry behaviour and rate-limit cooldown.
///
/// # TOML example
///
/// ```toml
/// [proxy]
/// cooldown_secs = 60
/// max_retries   = 3
/// ```
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default)]
pub struct ProxyConfig {
    /// Seconds a provider slot stays rate-limited after receiving HTTP 429.
    ///
    /// After this duration, `tick_cooldowns` re-enables the slot automatically
    /// the next time `pick_next()` is called on the routing table.
    /// Default: `60`. Must be ≥ 1.
    pub cooldown_secs: u64,

    /// Maximum number of retry attempts across available providers before the
    /// proxy returns HTTP 503 to the client.
    ///
    /// Counts attempts (not distinct providers) — if there are only 2 enabled
    /// providers, 3 retries will cycle at most across those 2.
    /// Default: `3`. Must be ≥ 1.
    pub max_retries: usize,
}

impl Default for ProxyConfig {
    fn default() -> Self {
        Self {
            cooldown_secs: 60,
            max_retries: 3,
        }
    }
}

/// `[backup]` section of config.toml.
///
/// Controls the [`crate::backup::BackupSync`] daemon task that mirrors tier
/// `.cascade/` directories to a local backup location after each regen cycle.
///
/// # TOML example
///
/// ```toml
/// [backup]
/// enabled = true
/// backup_root = "~/.cascade/backups"
/// sync_interval_secs = 60
/// max_versions = 7
/// ```
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default)]
pub struct BackupConfig {
    /// Enable backup sync; default `true`.
    ///
    /// Set to `false` to disable all backup operations (e.g. in CI or
    /// resource-constrained environments).
    pub enabled: bool,

    /// Root directory for backups; `~` is expanded at runtime.
    ///
    /// Each tier gets a subdirectory: `{backup_root}/{tier_name}/`.
    /// Default: `"~/.cascade/backups"`.
    pub backup_root: String,

    /// Minimum seconds between successive backups of the same tier.
    ///
    /// Prevents backup storms when many regen events fire in quick succession.
    /// Default: `60`. A value of `0` means no per-tier throttling.
    pub sync_interval_secs: u64,

    /// Maximum number of versioned snapshots to keep per tier.
    ///
    /// After each backup, older snapshots are pruned, keeping only the
    /// `max_versions` most recent. Default: `7`. Must be ≥ 1.
    pub max_versions: usize,
}

impl Default for BackupConfig {
    fn default() -> Self {
        Self {
            enabled: true,
            backup_root: "~/.cascade/backups".into(),
            sync_interval_secs: 60,
            max_versions: 7,
        }
    }
}

// -- Root config ---------------------------------------------------------------

/// Full `~/.cascade/config.toml` schema.
///
/// All sections are optional in the file; missing sections or fields use
/// their typed defaults so a missing config.toml is equivalent to an empty
/// one. Config is passed by value (not `Arc`) -- it is read-only after load
/// and cloned into the subsystems that need it.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default)]
#[derive(Default)]
pub struct Config {
    /// `[daemon]` section.
    pub daemon: DaemonConfig,
    /// `[watcher]` section.
    pub watcher: WatcherConfig,
    /// `[quota_poller]` section.
    pub quota_poller: QuotaPollerConfig,
    /// `[quota_store]` section.
    pub quota_store: QuotaStoreConfig,
    /// `[project_poller]` section.
    pub project_poller: ProjectPollerConfig,
    /// `[history]` section.
    pub history: HistoryConfig,
    /// `[scheduler]` section.
    pub scheduler: SchedulerConfig,
    /// `[proxy]` section.
    pub proxy: ProxyConfig,
    /// `[backup]` section.
    pub backup: BackupConfig,
    /// `[[hooks]]` array — hook definitions loaded from this config tier.
    ///
    /// Each entry is converted to a `HookDef` and seeded into the in-memory
    /// HookStore at daemon startup. Supports all hook events and action types.
    #[serde(default)]
    pub hooks: Vec<HookConfigEntry>,
}

impl Config {
    /// Load configuration from `config_dir/config.toml`.
    ///
    /// If the file is absent, returns `Config::default()` without error.
    /// If the file is present, deserializes then validates field ranges.
    ///
    /// # Errors
    ///
    /// - `ConfigError::Io` -- file exists but cannot be read
    /// - `ConfigError::Parse` -- TOML is malformed
    /// - `ConfigError::InvalidValue` -- a field is outside its allowed range/set
    pub fn load(config_dir: &Path) -> Result<Self, ConfigError> {
        let path = config_dir.join("config.toml");

        // Missing file -> silently use defaults (not an error).
        if !path.exists() {
            return Ok(Self::default());
        }

        let raw = std::fs::read_to_string(&path)?;
        let cfg: Config = toml::from_str(&raw)?;
        cfg.validate()?;
        Ok(cfg)
    }

    /// Validate field ranges/sets after deserialization.
    ///
    /// Returns `ConfigError::InvalidValue` for the first violation found.
    fn validate(&self) -> Result<(), ConfigError> {
        // watcher.debounce_ms: 10..=5000
        if !(10..=5000).contains(&self.watcher.debounce_ms) {
            return Err(ConfigError::InvalidValue {
                field: "watcher.debounce_ms".into(),
                constraint: "must be in range 10..=5000".into(),
            });
        }

        // daemon.health_sample_interval_secs: 5..=3600
        if !(5..=3600).contains(&self.daemon.health_sample_interval_secs) {
            return Err(ConfigError::InvalidValue {
                field: "daemon.health_sample_interval_secs".into(),
                constraint: "must be in range 5..=3600".into(),
            });
        }

        // quota_poller.interval_secs: 10..=3600
        if !(10..=3600).contains(&self.quota_poller.interval_secs) {
            return Err(ConfigError::InvalidValue {
                field: "quota_poller.interval_secs".into(),
                constraint: "must be in range 10..=3600".into(),
            });
        }

        // daemon.log_level: one of trace, debug, info, warn, error
        const VALID_LEVELS: &[&str] = &["trace", "debug", "info", "warn", "error"];
        if !VALID_LEVELS.contains(&self.daemon.log_level.as_str()) {
            return Err(ConfigError::InvalidValue {
                field: "daemon.log_level".into(),
                constraint: "must be one of: trace, debug, info, warn, error".into(),
            });
        }

        // daemon.log_format: "json" | "pretty"
        if !matches!(self.daemon.log_format.as_str(), "json" | "pretty") {
            return Err(ConfigError::InvalidValue {
                field: "daemon.log_format".into(),
                constraint: "must be one of: json, pretty".into(),
            });
        }

        // history.retention_days: 0..=36500
        if self.history.retention_days > 36500 {
            return Err(ConfigError::InvalidValue {
                field: "history.retention_days".into(),
                constraint: "must be in range 0..=36500 (0 = keep forever, max ~100 years)".into(),
            });
        }

        Ok(())
    }
}

// -- Unit tests ----------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    /// (a) Config::default() compiles and produces documented values.
    #[test]
    fn config_default_values() {
        let cfg = Config::default();
        assert_eq!(cfg.daemon.log_level, "info");
        assert_eq!(cfg.watcher.debounce_ms, 200);
        assert_eq!(cfg.quota_poller.interval_secs, 60);
        assert_eq!(cfg.history.retention_days, 90);
    }

    /// (b) Round-trip: serialise to TOML then load from a temp directory.
    #[test]
    fn config_round_trip() {
        let original = Config::default();
        let toml_str = toml::to_string(&original).expect("serialise failed");

        let dir = tempfile::tempdir().expect("tempdir");
        std::fs::write(dir.path().join("config.toml"), &toml_str).expect("write config.toml");

        let loaded = Config::load(dir.path()).expect("load failed");
        assert_eq!(loaded.daemon.log_level, original.daemon.log_level);
        assert_eq!(loaded.watcher.debounce_ms, original.watcher.debounce_ms);
        assert_eq!(
            loaded.quota_poller.interval_secs,
            original.quota_poller.interval_secs
        );
        assert_eq!(
            loaded.history.retention_days,
            original.history.retention_days
        );
    }

    /// (c) Missing config.toml returns Ok(Config::default()) -- no error.
    #[test]
    fn config_missing_file_returns_default() {
        let dir = tempfile::tempdir().expect("tempdir");
        // Intentionally do NOT create config.toml.
        let result = Config::load(dir.path());
        assert!(result.is_ok(), "expected Ok, got: {:?}", result.err());
        let cfg = result.unwrap();
        assert_eq!(cfg.daemon.log_level, "info");
        assert_eq!(cfg.watcher.debounce_ms, 200);
    }

    /// (d) debounce_ms below minimum returns ConfigError::InvalidValue.
    #[test]
    fn config_invalid_debounce_ms_returns_error() {
        let dir = tempfile::tempdir().expect("tempdir");
        let bad_toml = "[watcher]\ndebounce_ms = 3\n";
        std::fs::write(dir.path().join("config.toml"), bad_toml).expect("write");
        let result = Config::load(dir.path());
        assert!(result.is_err(), "expected Err for debounce_ms=3");
        match result.unwrap_err() {
            ConfigError::InvalidValue { field, .. } => {
                assert_eq!(field, "watcher.debounce_ms");
            }
            other => panic!("expected InvalidValue, got: {other:?}"),
        }
    }

    /// history.retention_days: 0 parses Ok; 99999 returns Err(InvalidValue).
    #[test]
    fn config_history_retention_days_validation() {
        let dir = tempfile::tempdir().expect("tempdir");

        // retention_days=0 should be Ok (0 = keep forever).
        std::fs::write(
            dir.path().join("config.toml"),
            "[history]\nretention_days = 0\n",
        )
        .expect("write");
        let result = Config::load(dir.path());
        assert!(
            result.is_ok(),
            "retention_days=0 should be valid, got: {:?}",
            result.err()
        );

        // retention_days=99999 exceeds 36500 -- should return InvalidValue.
        std::fs::write(
            dir.path().join("config.toml"),
            "[history]\nretention_days = 99999\n",
        )
        .expect("write");
        let result = Config::load(dir.path());
        assert!(result.is_err(), "retention_days=99999 should be invalid");
        match result.unwrap_err() {
            ConfigError::InvalidValue { field, .. } => {
                assert_eq!(field, "history.retention_days");
            }
            other => panic!("expected InvalidValue, got: {other:?}"),
        }
    }
}
