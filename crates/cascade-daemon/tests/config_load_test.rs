//! Integration tests for `cascade_daemon::Config::load`.
//!
//! Purpose: Verifies that the daemon can load its TOML configuration from a
//! directory and that parsed field values match what was written. Uses
//! `tempfile::TempDir` so the test config is cleaned up on drop.

use cascade_daemon::Config;
use std::fs;
use tempfile::TempDir;

/// Writes a minimal `[daemon]` TOML to a tempdir and asserts that
/// `Config::load` parses `socket_path` and `log_level` correctly.
#[test]
fn test_config_load_parses_socket_and_log_level() {
    let dir = TempDir::new().unwrap();
    fs::write(
        dir.path().join("config.toml"),
        "[daemon]\nsocket_path = \"/tmp/test.sock\"\nlog_level = \"debug\"\n",
    )
    .unwrap();

    let cfg = Config::load(dir.path()).expect("Config::load should succeed for a valid TOML file");

    assert_eq!(
        cfg.daemon.socket_path, "/tmp/test.sock",
        "socket_path should match the value in config.toml"
    );
    assert_eq!(
        cfg.daemon.log_level, "debug",
        "log_level should match config.toml"
    );
}

/// A TOML file with only `socket_path` set leaves `log_level` at its default.
#[test]
fn test_config_load_partial_fields_use_defaults() {
    let dir = TempDir::new().unwrap();
    fs::write(
        dir.path().join("config.toml"),
        "[daemon]\nsocket_path = \"/var/run/cascaded.sock\"\n",
    )
    .unwrap();

    let cfg = Config::load(dir.path()).unwrap();

    assert_eq!(cfg.daemon.socket_path, "/var/run/cascaded.sock");
    // log_level not present in file; should fall back to Default ("info").
    assert_eq!(
        cfg.daemon.log_level, "info",
        "missing log_level should default to 'info'"
    );
}

/// An empty TOML file (no sections) returns the default config.
#[test]
fn test_config_load_empty_file_returns_defaults() {
    let dir = TempDir::new().unwrap();
    fs::write(dir.path().join("config.toml"), "").unwrap();

    let cfg = Config::load(dir.path()).unwrap();
    let defaults = Config::default();
    assert_eq!(cfg.daemon.log_level, defaults.daemon.log_level);
    assert_eq!(cfg.daemon.socket_path, defaults.daemon.socket_path);
}

/// A non-existent config.toml returns Ok(Config::default()) -- not an error.
#[test]
fn test_config_load_missing_file_returns_default() {
    let dir = TempDir::new().unwrap();
    // Intentionally do NOT write config.toml.
    let result = Config::load(dir.path());
    assert!(
        result.is_ok(),
        "missing config.toml should return Ok(default)"
    );
    let cfg = result.unwrap();
    assert_eq!(cfg.daemon.log_level, "info");
}

/// Malformed TOML returns an error.
#[test]
fn test_config_load_invalid_toml_returns_error() {
    let dir = TempDir::new().unwrap();
    fs::write(
        dir.path().join("config.toml"),
        "this is not [ valid ] toml = {{{",
    )
    .unwrap();
    let result = Config::load(dir.path());
    assert!(result.is_err(), "expected an error for malformed TOML");
}
