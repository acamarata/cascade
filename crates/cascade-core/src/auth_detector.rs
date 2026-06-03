//! # cascade_core::auth_detector
//!
//! Harness credential scanner — detects existing AI coding harness accounts and
//! OAuth tokens by reading well-known app-data paths.
//!
//! ## Design principles
//!
//! - **Read-only**: never writes, never calls any network endpoint.
//! - **Best-effort**: a missing or unreadable file is silently skipped (logged at DEBUG).
//! - **No token exfiltration**: [`DetectedAccount`] stores only opaque identifiers
//!   (email addresses, partial hashes). Raw token bytes are never persisted.
//! - **Testable**: `detect_harness_accounts` accepts a `base_home: &Path` injection
//!   point so unit tests can supply a tempdir instead of the real home directory.
//!
//! ## Supported harnesses
//!
//! | Harness | Path inspected |
//! |---------|---------------|
//! | `cc` (Claude Code) | `~/.claude/settings.json` |
//! | `oc` (OpenCode) | `~/.config/opencode/config.json` |
//! | `codex` | `~/.config/codex/config.json` |
//! | `cursor` | `~/.cursor/settings.json` or `~/.config/Cursor/User/settings.json` |
//! | `gemini` | `~/.config/gcloud/application_default_credentials.json` |
//!
//! ## Usage
//!
//! ```rust,no_run
//! use cascade_core::auth_detector::detect_harness_accounts;
//! use std::path::Path;
//!
//! let home = std::env::var("HOME").map(std::path::PathBuf::from).unwrap();
//! let accounts = detect_harness_accounts(&home);
//! for acct in &accounts {
//!     println!("{}: {} ({})", acct.harness, acct.display_name, acct.account_id);
//! }
//! ```

use serde::{Deserialize, Serialize};
use std::path::{Path, PathBuf};
use tracing::debug;

// ── Public types ──────────────────────────────────────────────────────────────

/// How this account's credentials were found.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum AuthKind {
    /// An OAuth/browser-delegated token file was found.
    OAuthToken,
    /// A raw API key field was found.
    ApiKey,
    /// Credentials exist but their exact kind could not be determined.
    Unknown,
}

/// A single detected harness account.
///
/// **Security note**: `account_id` holds an opaque identifier only (e.g., an
/// email address parsed from JSON, or a hard-coded synthetic identifier such as
/// `"cc-default"`). Raw token bytes are *never* stored here.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DetectedAccount {
    /// Harness identifier: `"cc"` | `"oc"` | `"codex"` | `"cursor"` | `"gemini"`.
    pub harness: String,
    /// Opaque account identifier — email, synthetic label, or `"<unknown>"`.
    pub account_id: String,
    /// Human-readable display name (may equal `account_id`).
    pub display_name: String,
    /// How the credentials were identified.
    pub auth_kind: AuthKind,
    /// The file whose presence (or contents) triggered this detection.
    pub path_found: PathBuf,
}

// ── Public entry point ────────────────────────────────────────────────────────

/// Scan well-known harness app-data paths under `base_home` and return every
/// account that appears to have valid credentials.
///
/// Accepts `base_home` (typically `$HOME`) to allow substitution during tests.
/// All file reads are best-effort: any I/O error or JSON parse failure is
/// logged at `DEBUG` and silently skipped — the function never returns `Err`.
///
/// # Arguments
///
/// * `base_home` — path to the user's home directory; controls where all
///   harness config sub-paths are resolved.
///
/// # Returns
///
/// A `Vec<DetectedAccount>`, possibly empty if no harness config files are
/// found or readable.
pub fn detect_harness_accounts(base_home: &Path) -> Vec<DetectedAccount> {
    let mut results: Vec<DetectedAccount> = Vec::new();

    scan_cc(base_home, &mut results);
    scan_oc(base_home, &mut results);
    scan_codex(base_home, &mut results);
    scan_cursor(base_home, &mut results);
    scan_gemini(base_home, &mut results);

    results
}

// ── Private helpers ───────────────────────────────────────────────────────────

/// Minimal JSON shapes for each harness — only the fields we need.

#[derive(Deserialize)]
struct CcSettings {
    /// An `accounts` array may appear in newer CC settings.json formats.
    #[serde(default)]
    accounts: Vec<CcAccount>,
    /// OAuth token field used in older single-account CC formats.
    #[serde(rename = "oauthToken")]
    oauth_token: Option<String>,
}

#[derive(Deserialize)]
struct CcAccount {
    #[serde(default)]
    email_address: String,
    #[serde(rename = "emailAddress")]
    email_address_camel: Option<String>,
}

#[derive(Deserialize)]
struct OcConfig {
    #[serde(default)]
    providers: Vec<OcProvider>,
}

#[derive(Deserialize)]
struct OcProvider {
    name: Option<String>,
    id: Option<String>,
    api_key: Option<String>,
    access_token: Option<String>,
}

#[derive(Deserialize)]
struct CodexConfig {
    #[serde(rename = "apiKey")]
    api_key: Option<String>,
    #[serde(rename = "accessToken")]
    access_token: Option<String>,
    email: Option<String>,
}

#[derive(Deserialize)]
struct CursorSettings {
    #[serde(rename = "openai.apiKey")]
    openai_api_key: Option<String>,
    #[serde(rename = "cursor.apiKey")]
    cursor_api_key: Option<String>,
    #[serde(rename = "cursor.email")]
    cursor_email: Option<String>,
}

#[derive(Deserialize)]
struct GcloudAdc {
    client_id: Option<String>,
    client_email: Option<String>,
    #[serde(rename = "type")]
    credential_type: Option<String>,
}

// ── Scanner: Claude Code ──────────────────────────────────────────────────────

fn scan_cc(base_home: &Path, out: &mut Vec<DetectedAccount>) {
    // Path 1: ~/.claude/settings.json
    let settings_path = base_home.join(".claude").join("settings.json");

    if let Ok(contents) = std::fs::read_to_string(&settings_path) {
        match serde_json::from_str::<serde_json::Value>(&contents) {
            Ok(raw) => {
                // Try structured parse for accounts array
                if let Ok(parsed) = serde_json::from_value::<CcSettings>(raw.clone()) {
                    if !parsed.accounts.is_empty() {
                        for acct in &parsed.accounts {
                            let email = acct
                                .email_address_camel
                                .as_deref()
                                .unwrap_or(acct.email_address.as_str());
                            let id = if email.is_empty() {
                                "cc-account".to_string()
                            } else {
                                email.to_string()
                            };
                            out.push(DetectedAccount {
                                harness: "cc".into(),
                                account_id: id.clone(),
                                display_name: id,
                                auth_kind: AuthKind::OAuthToken,
                                path_found: settings_path.clone(),
                            });
                        }
                        return;
                    }
                    // Single-account legacy: oauthToken present
                    if parsed.oauth_token.is_some() {
                        out.push(DetectedAccount {
                            harness: "cc".into(),
                            account_id: "cc-default".into(),
                            display_name: "Claude Code (default)".into(),
                            auth_kind: AuthKind::OAuthToken,
                            path_found: settings_path.clone(),
                        });
                        return;
                    }
                }
                // Fallback: settings.json exists and is valid JSON — treat as detected
                debug!(
                    "cc: settings.json found but no recognized account fields; treating as \
                     detected (file: {:?})",
                    settings_path
                );
                out.push(DetectedAccount {
                    harness: "cc".into(),
                    account_id: "cc-default".into(),
                    display_name: "Claude Code".into(),
                    auth_kind: AuthKind::Unknown,
                    path_found: settings_path.clone(),
                });
            }
            Err(e) => {
                debug!("cc: failed to parse settings.json: {e}");
            }
        }
        return;
    }

    // Path 2: ~/.claude/.credentials.json (OAuth credential cache)
    let creds_path = base_home.join(".claude").join(".credentials.json");
    if creds_path.exists() {
        debug!("cc: .credentials.json found at {:?}", creds_path);
        out.push(DetectedAccount {
            harness: "cc".into(),
            account_id: "cc-default".into(),
            display_name: "Claude Code".into(),
            auth_kind: AuthKind::OAuthToken,
            path_found: creds_path,
        });
        return;
    }

    // Path 3: claude binary present — weak positive signal.
    // Only check when the ~/.claude directory exists under base_home, so that
    // tests with an empty tempdir do not accidentally pick up the real system binary.
    let claude_dir = base_home.join(".claude");
    if claude_dir.is_dir() {
        let binary_paths = [
            Path::new("/usr/local/bin/claude"),
            Path::new("/opt/homebrew/bin/claude"),
        ];
        for bin in &binary_paths {
            if bin.exists() {
                debug!("cc: binary found at {:?}, no credential file", bin);
                out.push(DetectedAccount {
                    harness: "cc".into(),
                    account_id: "cc-installed".into(),
                    display_name: "Claude Code (binary found, no creds)".into(),
                    auth_kind: AuthKind::Unknown,
                    path_found: bin.to_path_buf(),
                });
                return;
            }
        }
    }

    debug!("cc: no installation detected under {:?}", base_home);
}

// ── Scanner: OpenCode ─────────────────────────────────────────────────────────

fn scan_oc(base_home: &Path, out: &mut Vec<DetectedAccount>) {
    let candidates = [
        base_home
            .join(".config")
            .join("opencode")
            .join("config.json"),
        base_home
            .join(".config")
            .join("opencode")
            .join("settings.json"),
    ];

    for config_path in &candidates {
        let Ok(contents) = std::fs::read_to_string(config_path) else {
            continue;
        };
        match serde_json::from_str::<OcConfig>(&contents) {
            Ok(parsed) => {
                for provider in &parsed.providers {
                    let id = provider
                        .name
                        .as_deref()
                        .or(provider.id.as_deref())
                        .unwrap_or("oc-provider")
                        .to_string();
                    let auth_kind = if provider.access_token.is_some() {
                        AuthKind::OAuthToken
                    } else if provider.api_key.is_some() {
                        AuthKind::ApiKey
                    } else {
                        AuthKind::Unknown
                    };
                    out.push(DetectedAccount {
                        harness: "oc".into(),
                        account_id: id.clone(),
                        display_name: id,
                        auth_kind,
                        path_found: config_path.clone(),
                    });
                }
                if parsed.providers.is_empty() {
                    // File exists but no providers — still a signal
                    out.push(DetectedAccount {
                        harness: "oc".into(),
                        account_id: "oc-default".into(),
                        display_name: "OpenCode".into(),
                        auth_kind: AuthKind::Unknown,
                        path_found: config_path.clone(),
                    });
                }
                return;
            }
            Err(e) => {
                debug!("oc: failed to parse {:?}: {e}", config_path);
            }
        }
    }

    debug!("oc: no config found under {:?}", base_home);
}

// ── Scanner: Codex ────────────────────────────────────────────────────────────

fn scan_codex(base_home: &Path, out: &mut Vec<DetectedAccount>) {
    let config_path = base_home.join(".config").join("codex").join("config.json");

    let Ok(contents) = std::fs::read_to_string(&config_path) else {
        debug!("codex: config not found at {:?}", config_path);
        return;
    };

    match serde_json::from_str::<CodexConfig>(&contents) {
        Ok(parsed) => {
            let id = parsed
                .email
                .as_deref()
                .unwrap_or("codex-default")
                .to_string();
            let auth_kind = if parsed.access_token.is_some() {
                AuthKind::OAuthToken
            } else if parsed.api_key.is_some() {
                AuthKind::ApiKey
            } else {
                AuthKind::Unknown
            };
            out.push(DetectedAccount {
                harness: "codex".into(),
                account_id: id.clone(),
                display_name: id,
                auth_kind,
                path_found: config_path,
            });
        }
        Err(e) => {
            debug!("codex: failed to parse config.json: {e}");
        }
    }
}

// ── Scanner: Cursor ───────────────────────────────────────────────────────────

fn scan_cursor(base_home: &Path, out: &mut Vec<DetectedAccount>) {
    let candidates = [
        base_home.join(".cursor").join("settings.json"),
        base_home
            .join(".config")
            .join("Cursor")
            .join("User")
            .join("settings.json"),
    ];

    for settings_path in &candidates {
        let Ok(contents) = std::fs::read_to_string(settings_path) else {
            continue;
        };
        match serde_json::from_str::<CursorSettings>(&contents) {
            Ok(parsed) => {
                let has_key = parsed.openai_api_key.is_some() || parsed.cursor_api_key.is_some();
                if has_key || parsed.cursor_email.is_some() || !contents.is_empty() {
                    let id = parsed
                        .cursor_email
                        .as_deref()
                        .unwrap_or("cursor-default")
                        .to_string();
                    let auth_kind = if has_key {
                        AuthKind::ApiKey
                    } else {
                        AuthKind::Unknown
                    };
                    out.push(DetectedAccount {
                        harness: "cursor".into(),
                        account_id: id.clone(),
                        display_name: id,
                        auth_kind,
                        path_found: settings_path.clone(),
                    });
                    return;
                }
            }
            Err(e) => {
                debug!("cursor: failed to parse {:?}: {e}", settings_path);
            }
        }
    }

    debug!("cursor: no settings found under {:?}", base_home);
}

// ── Scanner: Google / Gemini ──────────────────────────────────────────────────

fn scan_gemini(base_home: &Path, out: &mut Vec<DetectedAccount>) {
    let adc_path = base_home
        .join(".config")
        .join("gcloud")
        .join("application_default_credentials.json");

    let Ok(contents) = std::fs::read_to_string(&adc_path) else {
        debug!("gemini: ADC file not found at {:?}", adc_path);
        return;
    };

    match serde_json::from_str::<GcloudAdc>(&contents) {
        Ok(parsed) => {
            // Use client_email (service account) or client_id (OAuth user) as ID
            let id = parsed
                .client_email
                .as_deref()
                .or(parsed.client_id.as_deref())
                .unwrap_or("gemini-default")
                .to_string();
            let auth_kind = match parsed.credential_type.as_deref() {
                Some("service_account") => AuthKind::ApiKey,
                _ => AuthKind::OAuthToken,
            };
            out.push(DetectedAccount {
                harness: "gemini".into(),
                account_id: id.clone(),
                display_name: id,
                auth_kind,
                path_found: adc_path,
            });
        }
        Err(e) => {
            debug!("gemini: failed to parse ADC file: {e}");
        }
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    // ── Helpers ───────────────────────────────────────────────────────────────

    fn write(base: &Path, rel: &str, content: &str) {
        let target = base.join(rel);
        if let Some(parent) = target.parent() {
            fs::create_dir_all(parent).expect("mkdir");
        }
        fs::write(&target, content).expect("write fixture");
    }

    // ── fixture_cc ────────────────────────────────────────────────────────────

    #[test]
    fn fixture_cc_accounts_array() {
        let dir = TempDir::new().unwrap();
        let home = dir.path();

        write(
            home,
            ".claude/settings.json",
            r#"{"accounts": [{"emailAddress": "alice@example.com"}]}"#,
        );

        let accounts = detect_harness_accounts(home);
        let cc: Vec<_> = accounts.iter().filter(|a| a.harness == "cc").collect();

        assert!(!cc.is_empty(), "expected at least one CC account");
        assert_eq!(cc[0].account_id, "alice@example.com");
        assert_eq!(cc[0].auth_kind, AuthKind::OAuthToken);
        assert!(cc[0].path_found.ends_with(".claude/settings.json"));
    }

    #[test]
    fn fixture_cc_oauth_token_field() {
        let dir = TempDir::new().unwrap();
        let home = dir.path();

        write(
            home,
            ".claude/settings.json",
            r#"{"oauthToken": "tok_redacted_for_test"}"#,
        );

        let accounts = detect_harness_accounts(home);
        let cc: Vec<_> = accounts.iter().filter(|a| a.harness == "cc").collect();

        assert!(!cc.is_empty(), "expected CC account via oauthToken field");
        assert_eq!(cc[0].auth_kind, AuthKind::OAuthToken);
    }

    // ── fixture_gemini ────────────────────────────────────────────────────────

    #[test]
    fn fixture_gemini() {
        let dir = TempDir::new().unwrap();
        let home = dir.path();

        write(
            home,
            ".config/gcloud/application_default_credentials.json",
            r#"{"client_id": "123456.apps.googleusercontent.com", "type": "authorized_user"}"#,
        );

        let accounts = detect_harness_accounts(home);
        let gemini: Vec<_> = accounts.iter().filter(|a| a.harness == "gemini").collect();

        assert!(!gemini.is_empty(), "expected gemini account");
        assert_eq!(gemini[0].account_id, "123456.apps.googleusercontent.com");
        assert_eq!(gemini[0].auth_kind, AuthKind::OAuthToken);
        assert!(gemini[0]
            .path_found
            .ends_with("application_default_credentials.json"));
    }

    #[test]
    fn fixture_gemini_service_account() {
        let dir = TempDir::new().unwrap();
        let home = dir.path();

        write(
            home,
            ".config/gcloud/application_default_credentials.json",
            r#"{"client_email": "sa@project.iam.gserviceaccount.com", "type": "service_account"}"#,
        );

        let accounts = detect_harness_accounts(home);
        let gemini: Vec<_> = accounts.iter().filter(|a| a.harness == "gemini").collect();

        assert!(!gemini.is_empty());
        assert_eq!(gemini[0].account_id, "sa@project.iam.gserviceaccount.com");
        assert_eq!(gemini[0].auth_kind, AuthKind::ApiKey);
    }

    // ── empty_home ────────────────────────────────────────────────────────────

    #[test]
    fn empty_home() {
        let dir = TempDir::new().unwrap();
        let accounts = detect_harness_accounts(dir.path());
        assert!(
            accounts.is_empty(),
            "empty home should yield no accounts, got: {accounts:?}"
        );
    }

    // ── malformed_json ────────────────────────────────────────────────────────

    #[test]
    fn malformed_json_cc() {
        let dir = TempDir::new().unwrap();
        let home = dir.path();

        write(home, ".claude/settings.json", "{ this is not json {{{{");

        // Must not panic and must return empty
        let accounts = detect_harness_accounts(home);
        // CC fallback: malformed JSON → empty because from_str fails
        let cc: Vec<_> = accounts.iter().filter(|a| a.harness == "cc").collect();
        assert!(
            cc.is_empty(),
            "malformed JSON should yield no CC accounts, got: {cc:?}"
        );
    }

    #[test]
    fn malformed_json_gemini() {
        let dir = TempDir::new().unwrap();
        let home = dir.path();

        write(
            home,
            ".config/gcloud/application_default_credentials.json",
            "not-json-at-all",
        );

        let accounts = detect_harness_accounts(home);
        let gemini: Vec<_> = accounts.iter().filter(|a| a.harness == "gemini").collect();
        assert!(
            gemini.is_empty(),
            "malformed gemini JSON should yield no accounts"
        );
    }

    // ── DetectedAccount fields populated ─────────────────────────────────────

    #[test]
    fn detected_account_fields_populated() {
        let dir = TempDir::new().unwrap();
        let home = dir.path();

        write(
            home,
            ".claude/settings.json",
            r#"{"accounts": [{"emailAddress": "bob@test.com"}]}"#,
        );
        write(
            home,
            ".config/gcloud/application_default_credentials.json",
            r#"{"client_id": "999.apps.googleusercontent.com", "type": "authorized_user"}"#,
        );

        let accounts = detect_harness_accounts(home);

        for acct in &accounts {
            assert!(!acct.harness.is_empty(), "harness must be non-empty");
            assert!(!acct.account_id.is_empty(), "account_id must be non-empty");
            assert!(
                !acct.display_name.is_empty(),
                "display_name must be non-empty"
            );
            assert!(
                acct.path_found.is_absolute(),
                "path_found must be absolute: {:?}",
                acct.path_found
            );
        }

        let has_cc = accounts.iter().any(|a| a.harness == "cc");
        let has_gemini = accounts.iter().any(|a| a.harness == "gemini");
        assert!(has_cc, "expected CC entry");
        assert!(has_gemini, "expected gemini entry");
    }

    // ── No network call verification (compile-time) ───────────────────────────
    // The absence of reqwest/hyper/tokio::net imports is verified by the build:
    // this module's Cargo.toml has no such dependencies, so any accidental
    // network-call import would fail to compile. This test documents the intent.
    #[test]
    fn no_network_calls_compile_check() {
        // If this file compiled, there are no network import failures.
        // The assertion is that detect_harness_accounts is a pure filesystem scanner.
        let dir = TempDir::new().unwrap();
        let result = detect_harness_accounts(dir.path());
        // Any type assertion is fine here — we just need a non-trivial use.
        let _: Vec<DetectedAccount> = result;
    }
}
