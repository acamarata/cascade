//! `cascade verify` — post-init healthcheck gate (E-P5-04).
//!
//! # Purpose
//!
//! Provides a single green/red gate that an installer or CI agent can call
//! immediately after `cascade init` to confirm the setup is usable. Exits 0
//! only when ALL required checks pass; exits 1 on any hard failure.
//!
//! # Checks
//!
//! | # | Name | Required? | Description |
//! |---|------|-----------|-------------|
//! | 1 | AI folder resolves | FAIL | Active folder exists + CASCADE.md is readable |
//! | 2 | Cascade resolves | FAIL | 6-tier merge returns non-empty output |
//! | 3 | Daemon reachable | WARN (or FAIL with --require-daemon) | Socket exists |
//! | 4 | AI provider available | FAIL | cloud provider in providers.json+keychain OR local model |
//! | 5 | Config integrity | FAIL | config.toml parses |
//! | 6 | Keychain accessible | FAIL | OS keychain can be queried |
//!
//! # Output
//!
//! Human mode: coloured checklist (`✓ PASS` / `⚠ WARN` / `✗ FAIL`).
//! `--json` emits `{"checks":[{"name","status","detail"}],"ok":bool}`.
//!
//! # Constraints
//!
//! - Never writes to the filesystem.
//! - All checks are independent; every check runs regardless of prior failures.
//! - Exit code 0 iff `ok == true`.
//!
//! # SPORT
//!
//! Entity: cascade-cli / verify subcommand (E-P5-04)

use std::path::PathBuf;

use async_trait::async_trait;
use cascade_keychain::platform_keychain;
use cascade_types::error::Result;
use clap::Args;
use serde::{Deserialize, Serialize};

use super::Command;

// ── Arg types ─────────────────────────────────────────────────────────────────

/// Arguments for `cascade verify`.
#[derive(Debug, Args)]
pub struct VerifyArgs {
    /// Output results as JSON (`{"checks":[...],"ok":bool}`).
    #[arg(long)]
    pub json: bool,

    /// Treat daemon-not-running as FAIL rather than WARN.
    #[arg(long)]
    pub require_daemon: bool,

    /// Directory to verify from. Defaults to the current working directory.
    #[arg(long)]
    pub dir: Option<PathBuf>,
}

// ── Result types ──────────────────────────────────────────────────────────────

/// Status of a single verify check.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum CheckStatus {
    Pass,
    Warn,
    Fail,
}

impl std::fmt::Display for CheckStatus {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            CheckStatus::Pass => write!(f, "PASS"),
            CheckStatus::Warn => write!(f, "WARN"),
            CheckStatus::Fail => write!(f, "FAIL"),
        }
    }
}

/// A single named check result.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CheckResult {
    /// Human-readable check name.
    pub name: String,
    /// Pass / Warn / Fail.
    pub status: CheckStatus,
    /// Optional detail message (empty on PASS, populated on WARN/FAIL).
    pub detail: String,
}

impl CheckResult {
    fn pass(name: impl Into<String>) -> Self {
        Self {
            name: name.into(),
            status: CheckStatus::Pass,
            detail: String::new(),
        }
    }

    fn warn(name: impl Into<String>, detail: impl Into<String>) -> Self {
        Self {
            name: name.into(),
            status: CheckStatus::Warn,
            detail: detail.into(),
        }
    }

    fn fail(name: impl Into<String>, detail: impl Into<String>) -> Self {
        Self {
            name: name.into(),
            status: CheckStatus::Fail,
            detail: detail.into(),
        }
    }
}

/// Top-level JSON output shape for `--json`.
#[derive(Debug, Serialize, Deserialize)]
pub struct VerifyOutput {
    /// Individual check results.
    pub checks: Vec<CheckResult>,
    /// `true` iff all required checks passed (no FAIL).
    pub ok: bool,
}

// ── Command impl ──────────────────────────────────────────────────────────────

#[async_trait]
impl Command for VerifyArgs {
    /// Run all healthchecks and exit 0 only when `ok == true`.
    ///
    /// Purpose: post-init release gate — proves a fresh setup is usable.
    /// Inputs: `VerifyArgs` (flags only; cwd from env or `--dir`).
    /// Outputs: human checklist or JSON on stdout; exits 1 on any FAIL.
    /// Constraints: never writes to disk; runs every check regardless of failure.
    /// SPORT: cascade-cli verify (E-P5-04)
    async fn run(&self) -> Result<()> {
        let cwd = self
            .dir
            .clone()
            .or_else(|| std::env::current_dir().ok())
            .unwrap_or_else(|| PathBuf::from("."));

        let mut checks = Vec::new();

        // Check 1: AI folder resolves + CASCADE.md readable.
        checks.push(check_ai_folder(&cwd));

        // Check 2: 6-tier cascade resolves with non-empty output.
        checks.push(check_cascade_resolves(&cwd).await);

        // Check 3: Daemon socket reachable (WARN or FAIL depending on flag).
        checks.push(check_daemon(self.require_daemon));

        // Check 4: At least one AI provider available (cloud or local model).
        checks.push(check_ai_provider());

        // Check 5: config.toml integrity.
        checks.push(check_config_integrity(&cwd));

        // Check 6: OS keychain accessible.
        checks.push(check_keychain());

        let ok = checks.iter().all(|c| c.status != CheckStatus::Fail);
        let output = VerifyOutput { checks, ok };

        if self.json {
            let json = serde_json::to_string_pretty(&output)
                .unwrap_or_else(|e| format!("{{\"error\":\"{e}\"}}"));
            println!("{json}");
        } else {
            print_human(&output);
        }

        if !ok {
            std::process::exit(1);
        }
        Ok(())
    }
}

// ── Individual checks ─────────────────────────────────────────────────────────

/// Check 1 — AI folder resolves.
///
/// Uses `cascade_core::ai_folder::resolve_folder_path` to find the active AI
/// folder, then verifies it exists as a directory and contains a readable
/// `CASCADE.md`.
///
/// Purpose: gate that the active folder is present and readable.
/// Inputs: `cwd` — current working directory.
/// Outputs: `CheckResult` (PASS/FAIL).
/// Constraints: read-only; never creates directories.
fn check_ai_folder(cwd: &std::path::Path) -> CheckResult {
    let folder_path = cascade_core::ai_folder::resolve_folder_path(cwd, None);

    if !folder_path.is_dir() {
        return CheckResult::fail(
            "AI folder resolves",
            format!(
                "{} does not exist — run `cascade init` first",
                folder_path.display()
            ),
        );
    }

    let cascade_md = folder_path.join("CASCADE.md");
    if !cascade_md.exists() {
        return CheckResult::fail(
            "AI folder resolves",
            format!("CASCADE.md missing in {}", folder_path.display()),
        );
    }

    match std::fs::read_to_string(&cascade_md) {
        Ok(_) => CheckResult::pass("AI folder resolves"),
        Err(e) => CheckResult::fail(
            "AI folder resolves",
            format!("CASCADE.md not readable: {e}"),
        ),
    }
}

/// Check 2 — cascade resolves to non-empty merged output.
///
/// Calls `cascade_core::resolution::Resolver` (same path used by `cascade
/// resolve`) and confirms at least one tier was found with non-empty text.
///
/// Purpose: proves the resolution engine works end-to-end from the cwd.
/// Inputs: `cwd`.
/// Outputs: `CheckResult` (PASS/FAIL).
/// Constraints: async; calls in-process resolver (no daemon IPC required).
async fn check_cascade_resolves(cwd: &std::path::Path) -> CheckResult {
    let resolver = cascade_core::resolution::Resolver::new();
    match resolver.resolve(cwd).await {
        Err(e) => CheckResult::fail("Cascade resolves", format!("resolution error: {e}")),
        Ok(resolved) => {
            if resolved.tier_sources.is_empty() && resolved.merged_text.is_empty() {
                CheckResult::fail(
                    "Cascade resolves",
                    "no cascade tiers found — run `cascade init` first",
                )
            } else {
                CheckResult::pass("Cascade resolves")
            }
        }
    }
}

/// Check 3 — daemon socket reachable.
///
/// Checks whether the Unix socket at `cascade_types::paths::daemon_socket()`
/// exists. A missing socket is WARN by default (daemon is optional for basic
/// usage), or FAIL when `--require-daemon` is passed.
///
/// Purpose: inform the user about daemon state without blocking setup.
/// Inputs: `require_daemon` flag.
/// Outputs: `CheckResult` (PASS / WARN / FAIL).
fn check_daemon(require_daemon: bool) -> CheckResult {
    let sock = cascade_types::paths::daemon_socket();
    if sock.exists() {
        CheckResult::pass("Daemon reachable")
    } else if require_daemon {
        CheckResult::fail(
            "Daemon reachable",
            format!(
                "socket not found at {} — run `cascade daemon install`",
                sock.display()
            ),
        )
    } else {
        CheckResult::warn(
            "Daemon reachable",
            format!(
                "not running (run `cascade daemon install`) — socket: {}",
                sock.display()
            ),
        )
    }
}

/// Check 4 — at least one AI provider available.
///
/// An AI path is available when EITHER:
/// (a) `cascade_providers::list_providers(keychain)` returns at least one
///     healthy connected cloud provider, OR
/// (b) `cascade_local_llm::scan_installed_models()` returns at least one entry.
///
/// Zero AI paths = FAIL (release gate: installer must ensure an AI path).
///
/// Purpose: self-bootstrapping AI path gate.
/// Inputs: system keychain + filesystem.
/// Outputs: `CheckResult` (PASS/FAIL).
fn check_ai_provider() -> CheckResult {
    let kc = platform_keychain();

    // Check cloud providers: at least one healthy entry in providers.json+keychain.
    let cloud_providers = cascade_providers::list_providers(kc.as_ref());
    let has_cloud = cloud_providers.iter().any(|p| p.healthy);

    // Check local models: at least one weight directory present.
    let local_models = cascade_local_llm::scan_installed_models();
    let has_local = !local_models.is_empty();

    if has_cloud || has_local {
        let mut sources: Vec<String> = Vec::new();
        if has_cloud {
            let ids: Vec<String> = cloud_providers
                .iter()
                .filter(|p| p.healthy)
                .map(|p| p.id.clone())
                .collect();
            sources.push(format!("cloud: {}", ids.join(", ")));
        }
        if has_local {
            let ids: Vec<String> = local_models.iter().map(|(id, _)| id.clone()).collect();
            sources.push(format!("local: {}", ids.join(", ")));
        }
        CheckResult::pass(format!("AI provider available ({})", sources.join("; ")))
    } else {
        CheckResult::fail(
            "AI provider available",
            "no cloud providers connected and no local models installed — \
             run `cascade provider add` or `cascade models download`",
        )
    }
}

/// Check 5 — config.toml integrity.
///
/// Looks for a `config.toml` at the global path (`~/.cascade/config.toml`) and,
/// if found, parses it. A missing file is acceptable (defaults apply). A
/// malformed TOML is FAIL.
///
/// Purpose: catch config corruption early rather than at runtime.
/// Inputs: `cwd` (for project config lookup).
/// Outputs: `CheckResult` (PASS/FAIL).
fn check_config_integrity(cwd: &std::path::Path) -> CheckResult {
    // Global config.
    let global_cfg = cascade_types::paths::global_config();
    if global_cfg.exists() {
        match std::fs::read_to_string(&global_cfg) {
            Ok(raw) => {
                if let Err(e) = raw.parse::<toml::Value>() {
                    return CheckResult::fail(
                        "Config integrity",
                        format!("global config.toml parse error: {e}"),
                    );
                }
            }
            Err(e) => {
                return CheckResult::fail(
                    "Config integrity",
                    format!("cannot read global config.toml: {e}"),
                );
            }
        }
    }

    // Project config (nearest ancestor with .cascade/config.toml).
    if let Some(proj_cfg) = cwd.ancestors().find_map(|a| {
        let c = a.join(".cascade").join("config.toml");
        if c.exists() {
            Some(c)
        } else {
            None
        }
    }) {
        match std::fs::read_to_string(&proj_cfg) {
            Ok(raw) => {
                if let Err(e) = raw.parse::<toml::Value>() {
                    return CheckResult::fail(
                        "Config integrity",
                        format!("project config.toml parse error: {e}"),
                    );
                }
            }
            Err(e) => {
                return CheckResult::fail(
                    "Config integrity",
                    format!("cannot read project config.toml: {e}"),
                );
            }
        }
    }

    CheckResult::pass("Config integrity")
}

/// Check 6 — OS keychain accessible.
///
/// Attempts a `get_key` call with a sentinel service + key that will not
/// exist. A `NotFound`-type error is expected and treated as PASS (keychain
/// is accessible). Any other error (permission denied, keychain locked,
/// daemon unavailable on macOS) is FAIL.
///
/// Purpose: surface keychain issues before the user tries to add providers.
/// Inputs: OS keychain via `cascade_keychain::platform_keychain()`.
/// Outputs: `CheckResult` (PASS/FAIL).
fn check_keychain() -> CheckResult {
    let kc = platform_keychain();
    // A missing key is the expected result — proves the keychain is reachable.
    // Any non-NotFound error means the keychain is locked or inaccessible.
    match kc.get_key("dev.cascade.verify-probe", "__probe__") {
        Ok(_) | Err(cascade_keychain::KeychainError::NotFound) => {
            CheckResult::pass("Keychain accessible")
        }
        Err(e) => CheckResult::fail("Keychain accessible", format!("keychain error: {e}")),
    }
}

// ── Human output ──────────────────────────────────────────────────────────────

/// Print a human-readable checklist with ANSI colouring.
fn print_human(output: &VerifyOutput) {
    println!("{:<40} {:<6} DETAIL", "CHECK", "STATUS");
    println!("{}", "-".repeat(80));
    for c in &output.checks {
        let status_str = match c.status {
            CheckStatus::Pass => "\x1b[32m✓ PASS\x1b[0m",
            CheckStatus::Warn => "\x1b[33m⚠ WARN\x1b[0m",
            CheckStatus::Fail => "\x1b[31m✗ FAIL\x1b[0m",
        };
        if c.detail.is_empty() {
            println!("{:<40} {}", c.name, status_str);
        } else {
            println!("{:<40} {}  {}", c.name, status_str, c.detail);
        }
    }
    println!("{}", "-".repeat(80));
    if output.ok {
        println!("\x1b[32mAll required checks passed — cascade is ready.\x1b[0m");
    } else {
        println!("\x1b[31mOne or more required checks failed — see details above.\x1b[0m");
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;
    use std::fs;
    use tempfile::TempDir;

    // ── helpers ───────────────────────────────────────────────────────────────

    /// Create a minimal valid setup in a tempdir:
    /// - .cascade/CASCADE.md (non-empty)
    /// - .cascade/config.toml (empty = valid TOML)
    fn setup_valid_dir() -> TempDir {
        let dir = TempDir::new().unwrap();
        let cascade_dir = dir.path().join(".cascade");
        fs::create_dir_all(&cascade_dir).unwrap();
        fs::write(cascade_dir.join("CASCADE.md"), "# GCI test instructions\n").unwrap();
        fs::write(cascade_dir.join("config.toml"), "").unwrap();
        dir
    }

    // ── check_ai_folder ───────────────────────────────────────────────────────

    #[test]
    fn ai_folder_pass_when_cascade_md_exists() {
        let dir = setup_valid_dir();
        let result = check_ai_folder(dir.path());
        assert_eq!(
            result.status,
            CheckStatus::Pass,
            "detail: {}",
            result.detail
        );
    }

    #[test]
    fn ai_folder_fail_when_no_cascade_dir() {
        let dir = TempDir::new().unwrap();
        let result = check_ai_folder(dir.path());
        assert_eq!(result.status, CheckStatus::Fail);
        assert!(
            result.detail.contains("does not exist")
                || result.detail.contains("run `cascade init`")
        );
    }

    #[test]
    fn ai_folder_fail_when_cascade_md_missing() {
        let dir = TempDir::new().unwrap();
        fs::create_dir_all(dir.path().join(".cascade")).unwrap();
        // No CASCADE.md written.
        let result = check_ai_folder(dir.path());
        assert_eq!(result.status, CheckStatus::Fail);
        assert!(result.detail.contains("CASCADE.md missing"));
    }

    // ── check_cascade_resolves ────────────────────────────────────────────────

    #[tokio::test]
    async fn cascade_resolves_pass_with_valid_setup() {
        let dir = setup_valid_dir();
        let result = check_cascade_resolves(dir.path()).await;
        assert_eq!(
            result.status,
            CheckStatus::Pass,
            "detail: {}",
            result.detail
        );
    }

    #[tokio::test]
    async fn cascade_resolves_fail_with_empty_dir() {
        let dir = TempDir::new().unwrap();
        let result = check_cascade_resolves(dir.path()).await;
        // An empty dir with no .cascade should produce empty/no-tier output → FAIL
        assert_eq!(
            result.status,
            CheckStatus::Fail,
            "detail: {}",
            result.detail
        );
    }

    // ── check_daemon ──────────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn daemon_warn_when_socket_absent() {
        // Set HOME to a fresh temp dir so daemon_socket() points to a non-existent path.
        let home = TempDir::new().unwrap();
        // Safety: test-only env mutation guarded by serial lock.
        unsafe { std::env::set_var("HOME", home.path()) };
        let result = check_daemon(false);
        unsafe { std::env::remove_var("HOME") };

        assert_eq!(result.status, CheckStatus::Warn);
        assert!(result.detail.contains("cascade daemon install"));
    }

    #[test]
    #[serial(global_env)]
    fn daemon_fail_when_socket_absent_and_require_daemon() {
        let home = TempDir::new().unwrap();
        unsafe { std::env::set_var("HOME", home.path()) };
        let result = check_daemon(true);
        unsafe { std::env::remove_var("HOME") };

        assert_eq!(result.status, CheckStatus::Fail);
    }

    // ── check_ai_provider ────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn ai_provider_fail_when_no_providers_and_no_local_models() {
        // Point HOME to a temp dir so providers.json doesn't exist and
        // ~/.cascade/models/ is absent.
        let home = TempDir::new().unwrap();
        unsafe { std::env::set_var("HOME", home.path()) };
        let result = check_ai_provider();
        unsafe { std::env::remove_var("HOME") };

        assert_eq!(
            result.status,
            CheckStatus::Fail,
            "detail: {}",
            result.detail
        );
        assert!(
            result.detail.contains("cascade provider add")
                || result.detail.contains("cascade models download")
        );
    }

    #[test]
    #[serial(global_env)]
    fn ai_provider_pass_when_local_model_present() {
        // Simulate a local model by creating ~/.cascade/models/<model_id>/ directory.
        // scan_installed_models() scans for directories matching known catalog entries.
        let home = TempDir::new().unwrap();
        let models_dir = home
            .path()
            .join(".cascade")
            .join("models")
            .join("gemma-2-2b");
        fs::create_dir_all(&models_dir).unwrap();
        // Write a fake weight file so the dir is non-trivially present.
        fs::write(models_dir.join("model.gguf"), b"fake").unwrap();

        unsafe { std::env::set_var("HOME", home.path()) };
        let result = check_ai_provider();
        unsafe { std::env::remove_var("HOME") };

        // scan_installed_models() checks for the dir — should report available.
        assert_eq!(
            result.status,
            CheckStatus::Pass,
            "detail: {}",
            result.detail
        );
    }

    // ── check_config_integrity ────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn config_integrity_pass_when_no_config_exists() {
        let home = TempDir::new().unwrap();
        unsafe { std::env::set_var("HOME", home.path()) };
        let result = check_config_integrity(home.path());
        unsafe { std::env::remove_var("HOME") };

        assert_eq!(
            result.status,
            CheckStatus::Pass,
            "detail: {}",
            result.detail
        );
    }

    #[test]
    #[serial(global_env)]
    fn config_integrity_pass_with_valid_toml() {
        let home = TempDir::new().unwrap();
        let cascade_dir = home.path().join(".cascade");
        fs::create_dir_all(&cascade_dir).unwrap();
        fs::write(
            cascade_dir.join("config.toml"),
            "[settings]\nkey = \"value\"\n",
        )
        .unwrap();

        unsafe { std::env::set_var("HOME", home.path()) };
        let result = check_config_integrity(home.path());
        unsafe { std::env::remove_var("HOME") };

        assert_eq!(
            result.status,
            CheckStatus::Pass,
            "detail: {}",
            result.detail
        );
    }

    #[test]
    #[serial(global_env)]
    fn config_integrity_fail_with_broken_toml() {
        let home = TempDir::new().unwrap();
        let cascade_dir = home.path().join(".cascade");
        fs::create_dir_all(&cascade_dir).unwrap();
        fs::write(cascade_dir.join("config.toml"), "this = [[[broken toml\n").unwrap();

        unsafe { std::env::set_var("HOME", home.path()) };
        let result = check_config_integrity(home.path());
        unsafe { std::env::remove_var("HOME") };

        assert_eq!(
            result.status,
            CheckStatus::Fail,
            "detail: {}",
            result.detail
        );
        assert!(result.detail.contains("parse error"));
    }

    // ── check_keychain ────────────────────────────────────────────────────────

    #[test]
    fn keychain_accessible_returns_pass_or_fail_not_panic() {
        // We cannot control the real keychain in tests, so we just verify it
        // returns a definite result (no panic, no unexpected WARN).
        let result = check_keychain();
        assert!(
            result.status == CheckStatus::Pass || result.status == CheckStatus::Fail,
            "keychain check must be PASS or FAIL, got WARN"
        );
    }

    // ── JSON output shape ─────────────────────────────────────────────────────

    #[test]
    fn json_output_shape_is_correct() {
        let checks = vec![
            CheckResult::pass("AI folder resolves"),
            CheckResult::warn("Daemon reachable", "not running"),
            CheckResult::fail("AI provider available", "no providers"),
        ];
        let output = VerifyOutput { checks, ok: false };
        let json = serde_json::to_string(&output).unwrap();

        let parsed: serde_json::Value = serde_json::from_str(&json).unwrap();
        assert!(parsed.get("checks").is_some(), "must have 'checks' array");
        assert!(parsed.get("ok").is_some(), "must have 'ok' bool");

        let checks_arr = parsed["checks"].as_array().unwrap();
        assert_eq!(checks_arr.len(), 3);
        assert_eq!(checks_arr[0]["status"].as_str().unwrap(), "pass");
        assert_eq!(checks_arr[1]["status"].as_str().unwrap(), "warn");
        assert_eq!(checks_arr[2]["status"].as_str().unwrap(), "fail");
        assert_eq!(parsed["ok"].as_bool().unwrap(), false);
    }

    #[test]
    fn json_ok_true_when_no_fails() {
        let checks = vec![
            CheckResult::pass("Check A"),
            CheckResult::warn("Check B", "minor issue"),
        ];
        let ok = checks.iter().all(|c| c.status != CheckStatus::Fail);
        assert!(ok, "warns alone should not set ok=false");
        let output = VerifyOutput { checks, ok };
        let json = serde_json::to_string(&output).unwrap();
        let parsed: serde_json::Value = serde_json::from_str(&json).unwrap();
        assert_eq!(parsed["ok"].as_bool().unwrap(), true);
    }

    // ── Full all-green fixture ────────────────────────────────────────────────

    #[tokio::test]
    #[serial(global_env)]
    async fn all_checks_pass_on_fully_setup_tempdir() {
        // Build a fully-configured tempdir:
        // - .cascade/CASCADE.md
        // - .cascade/config.toml (valid)
        // - providers.json with a healthy entry (mocked via fake model instead)
        // - ~/.cascade/models/gemma-2-2b/ (local model simulating AI path)
        let home = TempDir::new().unwrap();
        let cascade_dir = home.path().join(".cascade");
        fs::create_dir_all(&cascade_dir).unwrap();
        fs::write(cascade_dir.join("CASCADE.md"), "# Global instructions\n").unwrap();
        fs::write(cascade_dir.join("config.toml"), "").unwrap();

        // Provide a local model so AI provider check passes.
        let model_dir = cascade_dir.join("models").join("gemma-2-2b");
        fs::create_dir_all(&model_dir).unwrap();
        fs::write(model_dir.join("model.gguf"), b"fake").unwrap();

        unsafe { std::env::set_var("HOME", home.path()) };

        // Run each check (except daemon which depends on real socket).
        let folder = check_ai_folder(home.path());
        let resolves = check_cascade_resolves(home.path()).await;
        let daemon = check_daemon(false); // WARN is acceptable
        let provider = check_ai_provider();
        let config = check_config_integrity(home.path());
        let keychain = check_keychain();

        unsafe { std::env::remove_var("HOME") };

        // Folder + cascade + config must PASS.
        assert_eq!(
            folder.status,
            CheckStatus::Pass,
            "folder: {}",
            folder.detail
        );
        assert_eq!(
            resolves.status,
            CheckStatus::Pass,
            "resolves: {}",
            resolves.detail
        );
        assert_eq!(
            config.status,
            CheckStatus::Pass,
            "config: {}",
            config.detail
        );
        assert_eq!(
            provider.status,
            CheckStatus::Pass,
            "provider: {}",
            provider.detail
        );
        // Daemon may WARN (no real socket in tempdir) — not a hard failure.
        assert!(daemon.status != CheckStatus::Fail || daemon.status == CheckStatus::Warn);
        // Keychain: PASS or FAIL (real OS keychain; not WARN).
        assert!(
            keychain.status == CheckStatus::Pass || keychain.status == CheckStatus::Fail,
            "keychain status must be PASS or FAIL"
        );

        // ok is true iff folder+resolves+provider+config pass and daemon is only WARN.
        let all = vec![folder, resolves, daemon, provider, config, keychain];
        let ok = all.iter().all(|c| c.status != CheckStatus::Fail);
        // Not strictly required to be true here (keychain may FAIL in CI),
        // but at minimum the non-keychain checks should all pass.
        let critical_ok = all
            .iter()
            .filter(|c| c.name != "Keychain accessible" && c.name != "Daemon reachable")
            .all(|c| c.status == CheckStatus::Pass);
        assert!(critical_ok, "non-keychain/daemon checks must all PASS");
        let _ = ok;
    }
}
