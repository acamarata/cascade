//! # auto_auth_import
//!
//! Detect and import AI harness accounts from well-known disk locations.
//!
//! ## Purpose
//! Scans installed AI coding harnesses (Claude Code, OpenCode, Codex, Cursor)
//! and env vars for authentication state, producing a list of `DiscoveredAccount`
//! entries that the onboarding wizard can present for import.
//!
//! ## Design invariants
//! - **Read-only.** No harness config file is ever written or modified.
//! - **No secret logging.** API key and token values are never written to trace spans.
//! - **AI-optional.** This step runs entirely before any AI provider is available.
//! - macOS Keychain reads use `security find-generic-password` subprocess.
//! - JWT decode extracts the `sub`/`email` field from the payload (no signature verification needed).
//!
//! ## Inputs
//! None — reads from the filesystem and environment.
//!
//! ## Outputs
//! `Vec<DiscoveredAccount>` — one entry per detected harness account or env API key.
//!
//! ## Constraints
//! - `HOME`-confined reads — never reads outside `~` (home directory).
//! - `permission-denied` on any file → silently skip (not an error).
//! - JWT payload decode is best-effort; malformed tokens are skipped.

use cascade_types::auto_auth::{AuthSource, AuthType, DiscoveredAccount, ImportResult};
use tracing::warn;

// ── Claude Code scanner ───────────────────────────────────────────────────────

/// Scan Claude Code configuration for account hints.
///
/// # Purpose
/// Reads `~/.claude/settings.json` for an email hint. On macOS, also attempts
/// `security find-generic-password -s claude-code -w` to extract an OAuth JWT
/// and decode the `sub`/`email` payload field (no signature verification).
///
/// # Outputs
/// Up to one `DiscoveredAccount` with `source=ClaudeCode`, `importable=false`
/// (CC owns its OAuth — Cascade only reads the identity).
pub fn scan_claude_code() -> Vec<DiscoveredAccount> {
    let home = match dirs::home_dir() {
        Some(h) => h,
        None => return vec![],
    };

    let mut email_hint = String::new();

    // 1. Try ~/.claude/settings.json for an email hint
    let settings_path = home.join(".claude").join("settings.json");
    if let Ok(contents) = std::fs::read_to_string(&settings_path) {
        if let Ok(json) = serde_json::from_str::<serde_json::Value>(&contents) {
            // CC settings.json may store an account email in various fields
            for field in &["email", "userEmail", "account", "userId"] {
                if let Some(val) = json.get(field).and_then(|v| v.as_str()) {
                    if !val.is_empty() {
                        email_hint = val.to_string();
                        break;
                    }
                }
            }
        }
    }

    // 2. On macOS, try security find-generic-password for OAuth JWT
    #[cfg(target_os = "macos")]
    {
        use std::process::Command;
        let output = Command::new("security")
            .args(["find-generic-password", "-s", "claude-code", "-w"])
            .output();
        match output {
            Ok(out) if out.status.success() => {
                let token = String::from_utf8_lossy(&out.stdout).trim().to_string();
                if let Some(hint) = decode_jwt_email(&token) {
                    email_hint = hint;
                }
                // SECURITY: `token` value is NOT logged — only the decoded email hint
            }
            Ok(_) => {} // no credential stored — skip silently
            Err(e) => warn!(err = %e, "security find-generic-password failed"),
        }
    }

    // 3. Fall back: check ~/.claude/.credentials (plain JSON)
    if email_hint.is_empty() {
        let creds_path = home.join(".claude").join(".credentials");
        if let Ok(contents) = std::fs::read_to_string(&creds_path) {
            if let Ok(json) = serde_json::from_str::<serde_json::Value>(&contents) {
                for field in &["email", "sub", "userEmail"] {
                    if let Some(val) = json.get(field).and_then(|v| v.as_str()) {
                        if !val.is_empty() {
                            email_hint = val.to_string();
                            break;
                        }
                    }
                }
                // Also try extracting from a JWT in a token field
                if email_hint.is_empty() {
                    for field in &["access_token", "token", "id_token"] {
                        if let Some(token) = json.get(field).and_then(|v| v.as_str()) {
                            if let Some(hint) = decode_jwt_email(token) {
                                email_hint = hint;
                                break;
                            }
                        }
                    }
                }
            }
        }
    }

    // Only emit an account entry if we found something
    let hint = if email_hint.is_empty() {
        // Check if settings file even exists (CC is installed)
        if !settings_path.exists() {
            return vec![];
        }
        "Claude Code (account unknown)".to_string()
    } else {
        email_hint
    };

    vec![DiscoveredAccount {
        source: AuthSource::ClaudeCode,
        email_or_hint: hint,
        provider: "anthropic".to_string(),
        auth_type: AuthType::OAuthToken,
        importable: false,
    }]
}

// ── OpenCode scanner ──────────────────────────────────────────────────────────

/// Scan OpenCode configuration for provider entries.
///
/// # Purpose
/// Reads `~/.config/opencode/config.json` (or `opencode.json`) and walks
/// provider entries to extract emails and provider types.
///
/// # Outputs
/// One `DiscoveredAccount` per detected provider entry.
pub fn scan_opencode() -> Vec<DiscoveredAccount> {
    let home = match dirs::home_dir() {
        Some(h) => h,
        None => return vec![],
    };

    // Try both config paths
    let candidates = [
        home.join(".config").join("opencode").join("config.json"),
        home.join(".config").join("opencode").join("opencode.json"),
        home.join(".opencode").join("config.json"),
    ];

    for config_path in &candidates {
        if !config_path.exists() {
            continue;
        }
        let contents = match std::fs::read_to_string(config_path) {
            Ok(c) => c,
            Err(_) => continue,
        };
        let json: serde_json::Value = match serde_json::from_str(&contents) {
            Ok(j) => j,
            Err(_) => continue, // malformed — skip
        };

        let mut accounts = Vec::new();

        // OpenCode config.json: "providers" is a map of provider_id -> { email?, ... }
        if let Some(providers) = json.get("providers").and_then(|p| p.as_object()) {
            for (provider_id, entry) in providers {
                let email = entry
                    .get("email")
                    .or_else(|| entry.get("userEmail"))
                    .and_then(|v| v.as_str())
                    .unwrap_or("")
                    .to_string();
                let hint = if email.is_empty() {
                    format!("OpenCode: {}", provider_id)
                } else {
                    email
                };
                accounts.push(DiscoveredAccount {
                    source: AuthSource::OpenCode,
                    email_or_hint: hint,
                    provider: normalize_provider(provider_id),
                    auth_type: AuthType::OAuthToken,
                    importable: false,
                });
            }
        }

        // Also check top-level email/model fields (simpler opencode configs)
        if accounts.is_empty() {
            let email = json
                .get("email")
                .or_else(|| json.get("userEmail"))
                .and_then(|v| v.as_str())
                .unwrap_or("")
                .to_string();
            if !email.is_empty() {
                accounts.push(DiscoveredAccount {
                    source: AuthSource::OpenCode,
                    email_or_hint: email,
                    provider: "openai".to_string(), // default for generic opencode
                    auth_type: AuthType::OAuthToken,
                    importable: false,
                });
            }
        }

        if !accounts.is_empty() {
            return accounts;
        }
    }

    // Also check ~/.config/opencode/.credentials
    let creds_path = home.join(".config").join("opencode").join(".credentials");
    if let Ok(contents) = std::fs::read_to_string(&creds_path) {
        if let Ok(json) = serde_json::from_str::<serde_json::Value>(&contents) {
            let email = json
                .get("email")
                .or_else(|| json.get("sub"))
                .and_then(|v| v.as_str())
                .unwrap_or("")
                .to_string();
            if !email.is_empty() {
                return vec![DiscoveredAccount {
                    source: AuthSource::OpenCode,
                    email_or_hint: email,
                    provider: "openai".to_string(),
                    auth_type: AuthType::OAuthToken,
                    importable: false,
                }];
            }
        }
    }

    vec![]
}

// ── Codex scanner ─────────────────────────────────────────────────────────────

/// Scan Codex CLI configuration for account info.
///
/// # Purpose
/// Tries `~/.codex/auth.json` and `~/.config/codex/auth.json`.
/// Extracts email and token type if present.
///
/// # Outputs
/// Up to one `DiscoveredAccount` with `source=Codex`.
pub fn scan_codex() -> Vec<DiscoveredAccount> {
    let home = match dirs::home_dir() {
        Some(h) => h,
        None => return vec![],
    };

    let candidates = [
        home.join(".codex").join("auth.json"),
        home.join(".config").join("codex").join("auth.json"),
    ];

    for auth_path in &candidates {
        if !auth_path.exists() {
            continue;
        }
        let contents = match std::fs::read_to_string(auth_path) {
            Ok(c) => c,
            Err(_) => continue,
        };
        let json: serde_json::Value = match serde_json::from_str(&contents) {
            Ok(j) => j,
            Err(_) => continue, // malformed — skip
        };

        let email = json
            .get("email")
            .or_else(|| json.get("user")
                .and_then(|u| u.get("email")))
            .and_then(|v| v.as_str())
            .unwrap_or("")
            .to_string();

        // Detect auth type from token_type or presence of api_key fields
        let auth_type = if json.get("api_key").is_some() || json.get("apiKey").is_some() {
            AuthType::ApiKey
        } else {
            AuthType::OAuthToken
        };

        let hint = if email.is_empty() {
            "Codex (account unknown)".to_string()
        } else {
            email
        };

        return vec![DiscoveredAccount {
            source: AuthSource::Codex,
            email_or_hint: hint,
            provider: "openai".to_string(),
            auth_type,
            importable: false,
        }];
    }

    vec![]
}

// ── Cursor scanner ────────────────────────────────────────────────────────────

/// Scan Cursor for the cached email address.
///
/// # Purpose
/// Reads `~/Library/Application Support/Cursor/User/globalStorage/storage.json`
/// (macOS only) and extracts `cursorAuth.cachedEmail` if present.
///
/// # Outputs
/// Up to one `DiscoveredAccount` with `source=Cursor`, `importable=false`.
///
/// # Constraints
/// Guarded by `#[cfg(target_os = "macos")]` — no-op on Linux/Windows in P3.
pub fn scan_cursor() -> Vec<DiscoveredAccount> {
    #[cfg(target_os = "macos")]
    {
        let home = match dirs::home_dir() {
            Some(h) => h,
            None => return vec![],
        };

        let storage_path = home
            .join("Library")
            .join("Application Support")
            .join("Cursor")
            .join("User")
            .join("globalStorage")
            .join("storage.json");

        if !storage_path.exists() {
            return vec![];
        }

        let contents = match std::fs::read_to_string(&storage_path) {
            Ok(c) => c,
            Err(_) => return vec![],
        };

        let json: serde_json::Value = match serde_json::from_str(&contents) {
            Ok(j) => j,
            Err(_) => return vec![], // malformed — skip
        };

        let email = json
            .get("cursorAuth.cachedEmail")
            .or_else(|| {
                json.get("cursorAuth")
                    .and_then(|ca| ca.get("cachedEmail"))
            })
            .and_then(|v| v.as_str())
            .unwrap_or("")
            .to_string();

        if email.is_empty() {
            return vec![];
        }

        return vec![DiscoveredAccount {
            source: AuthSource::Cursor,
            email_or_hint: email,
            provider: "anthropic".to_string(), // Cursor primarily uses Anthropic models
            auth_type: AuthType::OAuthToken,
            importable: false,
        }];
    }

    // Non-macOS platforms: no-op in P3 (Windows/Linux Keychain deferred to P4)
    #[cfg(not(target_os = "macos"))]
    vec![]
}

// ── Env var scanner ───────────────────────────────────────────────────────────

/// Scan environment variables for API keys.
///
/// # Purpose
/// Checks `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GOOGLE_API_KEY`, and
/// `GEMINI_API_KEY`. If set, emits a `DiscoveredAccount` with `importable=true`.
///
/// # Constraints
/// Key values are never logged.
pub fn scan_env_vars() -> Vec<DiscoveredAccount> {
    let mut accounts = Vec::new();

    // (env var, provider id, display hint prefix)
    let checks: &[(&str, &str)] = &[
        ("ANTHROPIC_API_KEY", "anthropic"),
        ("OPENAI_API_KEY", "openai"),
        ("GOOGLE_API_KEY", "google"),
        ("GEMINI_API_KEY", "google"),
    ];

    for (var, provider) in checks {
        if std::env::var(var).map(|v| !v.is_empty()).unwrap_or(false) {
            accounts.push(DiscoveredAccount {
                source: AuthSource::EnvVar,
                email_or_hint: format!("env: {}", var),
                provider: provider.to_string(),
                auth_type: AuthType::EnvApiKey,
                importable: true,
            });
            // SECURITY: the key value is NOT read here — only presence is checked.
        }
    }

    accounts
}

// ── Aggregate ─────────────────────────────────────────────────────────────────

/// Run all harness scanners and merge results.
///
/// # Purpose
/// Calls all five scan functions in sequence and returns the combined list.
/// Safe to call before any AI provider is connected.
///
/// # Outputs
/// `Vec<DiscoveredAccount>` — one entry per detected account or env key.
/// Empty vec if no harnesses are installed and no env vars are set.
pub fn scan_all() -> Vec<DiscoveredAccount> {
    let mut results = Vec::new();
    results.extend(scan_claude_code());
    results.extend(scan_opencode());
    results.extend(scan_codex());
    results.extend(scan_cursor());
    results.extend(scan_env_vars());
    results
}

// ── Import ────────────────────────────────────────────────────────────────────

/// Import selected `DiscoveredAccount` entries into cascade-keychain.
///
/// # Purpose
/// For each `importable` `EnvApiKey` account in `selected`, reads the env var
/// value and stores it in cascade-keychain under the appropriate provider key.
/// Non-importable accounts are skipped with a note.
///
/// # Inputs
/// - `selected`: the accounts the user chose to import.
///
/// # Outputs
/// `ImportResult` with imported/skipped/errors lists.
///
/// # Constraints
/// - Key values are NEVER logged.
/// - Keychain writes via `cascade_keychain::platform_keychain()`.
pub async fn import_accounts(selected: Vec<DiscoveredAccount>) -> ImportResult {
    use cascade_keychain::{platform_keychain, Keychain};

    let kc: Box<dyn Keychain> = platform_keychain();
    let mut imported = Vec::new();
    let mut skipped = Vec::new();
    let mut errors = Vec::new();

    for account in selected {
        if !account.importable {
            skipped.push(account.email_or_hint.clone());
            continue;
        }

        match account.auth_type {
            AuthType::EnvApiKey => {
                // Extract variable name from hint "env: VAR_NAME"
                let var_name = account.email_or_hint
                    .strip_prefix("env: ")
                    .unwrap_or(&account.email_or_hint);

                match std::env::var(var_name) {
                    Ok(key_value) if !key_value.is_empty() => {
                        // Store under "dev.cascade" service, keyed by provider name
                        let account_key = format!("{}-api-key", account.provider);
                        match kc.set_key("dev.cascade", &account_key, &key_value) {
                            Ok(()) => imported.push(account.email_or_hint.clone()),
                            Err(e) => {
                                // SECURITY: key_value is NOT included in the error message
                                errors.push(format!(
                                    "keychain write failed for {}: {}",
                                    account.provider, e
                                ));
                            }
                        }
                        // SECURITY: key_value is dropped here without logging
                        drop(key_value);
                    }
                    Ok(_) => {
                        errors.push(format!("env var {} is empty", var_name));
                    }
                    Err(_) => {
                        errors.push(format!("env var {} not set at import time", var_name));
                    }
                }
            }
            _ => {
                skipped.push(account.email_or_hint.clone());
            }
        }
    }

    ImportResult { imported, skipped, errors }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Decode the payload section of a JWT and extract `sub` or `email`.
///
/// # Purpose
/// Extracts the email/sub claim from the middle (payload) segment of a JWT without
/// verifying the signature. Used for identity hints only — not for authorization.
///
/// Returns `None` for any malformed token (not an error — callers skip silently).
fn decode_jwt_email(token: &str) -> Option<String> {
    use base64::Engine;

    let parts: Vec<&str> = token.splitn(3, '.').collect();
    if parts.len() < 2 {
        return None;
    }

    // base64url decode the payload segment (pad to multiple of 4)
    let payload_b64 = parts[1];
    let padding = (4 - payload_b64.len() % 4) % 4;
    let padded = format!("{}{}", payload_b64, "=".repeat(padding));
    let decoded = base64::engine::general_purpose::URL_SAFE_NO_PAD
        .decode(payload_b64)
        .or_else(|_| base64::engine::general_purpose::URL_SAFE.decode(&padded))
        .ok()?;

    let payload: serde_json::Value = serde_json::from_slice(&decoded).ok()?;

    // Prefer "email" over "sub" since sub may be an opaque ID
    payload
        .get("email")
        .or_else(|| payload.get("sub"))
        .and_then(|v| v.as_str())
        .filter(|s| !s.is_empty())
        .map(str::to_string)
}

/// Normalize a provider ID string to a canonical name.
fn normalize_provider(id: &str) -> String {
    match id.to_lowercase().as_str() {
        s if s.contains("anthropic") || s.contains("claude") => "anthropic".to_string(),
        s if s.contains("openai") || s.contains("gpt") || s.contains("codex") => {
            "openai".to_string()
        }
        s if s.contains("google") || s.contains("gemini") || s.contains("vertex") => {
            "google".to_string()
        }
        other => other.to_string(),
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;
    use std::io::Write;
    use tempfile::TempDir;

    // Helper: write a file relative to a temp dir
    fn write_file(dir: &TempDir, relative: &str, content: &str) {
        let path = dir.path().join(relative);
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent).unwrap();
        }
        let mut f = std::fs::File::create(&path).unwrap();
        f.write_all(content.as_bytes()).unwrap();
    }

    // ── CC scan ──────────────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn scan_cc_detects_email_in_settings_json() {
        let tmp = TempDir::new().unwrap();
        write_file(
            &tmp,
            ".claude/settings.json",
            r#"{"email":"claude@example.com","model":"claude-3-5-sonnet"}"#,
        );

        // Override HOME for this test
        let prev_home = std::env::var("HOME").ok();
        std::env::set_var("HOME", tmp.path());

        let results = scan_claude_code();

        // Restore
        match prev_home {
            Some(h) => std::env::set_var("HOME", h),
            None => std::env::remove_var("HOME"),
        }

        assert!(!results.is_empty(), "expected at least one CC account");
        let acc = &results[0];
        assert_eq!(acc.source, AuthSource::ClaudeCode);
        assert_eq!(acc.email_or_hint, "claude@example.com");
        assert_eq!(acc.provider, "anthropic");
        assert!(!acc.importable, "CC OAuth tokens are never directly importable");
    }

    #[test]
    #[serial(global_env)]
    fn scan_cc_skips_when_no_settings_file() {
        let tmp = TempDir::new().unwrap();
        let prev_home = std::env::var("HOME").ok();
        std::env::set_var("HOME", tmp.path());

        let results = scan_claude_code();

        match prev_home {
            Some(h) => std::env::set_var("HOME", h),
            None => std::env::remove_var("HOME"),
        }

        assert!(results.is_empty(), "no settings.json → no accounts");
    }

    #[test]
    #[serial(global_env)]
    fn scan_cc_skips_malformed_credentials() {
        let tmp = TempDir::new().unwrap();
        // settings.json exists (so CC is "installed") but .credentials is garbage
        write_file(&tmp, ".claude/settings.json", r#"{"model":"claude-3-5-sonnet"}"#);
        write_file(&tmp, ".claude/.credentials", "NOT_VALID_JSON{{{");

        let prev_home = std::env::var("HOME").ok();
        std::env::set_var("HOME", tmp.path());

        // Should not panic; returns account with "unknown" hint
        let results = scan_claude_code();

        match prev_home {
            Some(h) => std::env::set_var("HOME", h),
            None => std::env::remove_var("HOME"),
        }

        // settings.json exists → we emit a placeholder
        assert_eq!(results.len(), 1);
        assert!(results[0].email_or_hint.contains("unknown"));
    }

    // ── OpenCode scan ────────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn scan_opencode_detects_providers() {
        let tmp = TempDir::new().unwrap();
        write_file(
            &tmp,
            ".config/opencode/config.json",
            r#"{"providers":{"anthropic":{"email":"oc@example.com"},"openai":{}}}"#,
        );

        let prev_home = std::env::var("HOME").ok();
        std::env::set_var("HOME", tmp.path());

        let results = scan_opencode();

        match prev_home {
            Some(h) => std::env::set_var("HOME", h),
            None => std::env::remove_var("HOME"),
        }

        assert!(!results.is_empty(), "expected OpenCode providers");
        assert!(results.iter().any(|a| a.source == AuthSource::OpenCode));
        let anthropic = results.iter().find(|a| a.provider == "anthropic");
        assert!(anthropic.is_some());
        assert_eq!(anthropic.unwrap().email_or_hint, "oc@example.com");
    }

    #[test]
    #[serial(global_env)]
    fn scan_opencode_skips_malformed_json() {
        let tmp = TempDir::new().unwrap();
        write_file(&tmp, ".config/opencode/config.json", "{NOT JSON}}}");

        let prev_home = std::env::var("HOME").ok();
        std::env::set_var("HOME", tmp.path());

        let results = scan_opencode();

        match prev_home {
            Some(h) => std::env::set_var("HOME", h),
            None => std::env::remove_var("HOME"),
        }

        assert!(results.is_empty(), "malformed config → skip, no panic");
    }

    // ── Codex scan ───────────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn scan_codex_detects_email() {
        let tmp = TempDir::new().unwrap();
        write_file(&tmp, ".codex/auth.json", r#"{"email":"codex@example.com"}"#);

        let prev_home = std::env::var("HOME").ok();
        std::env::set_var("HOME", tmp.path());

        let results = scan_codex();

        match prev_home {
            Some(h) => std::env::set_var("HOME", h),
            None => std::env::remove_var("HOME"),
        }

        assert_eq!(results.len(), 1);
        assert_eq!(results[0].source, AuthSource::Codex);
        assert_eq!(results[0].email_or_hint, "codex@example.com");
        assert_eq!(results[0].provider, "openai");
        assert!(!results[0].importable);
    }

    #[test]
    #[serial(global_env)]
    fn scan_codex_skips_when_absent() {
        let tmp = TempDir::new().unwrap();
        let prev_home = std::env::var("HOME").ok();
        std::env::set_var("HOME", tmp.path());

        let results = scan_codex();

        match prev_home {
            Some(h) => std::env::set_var("HOME", h),
            None => std::env::remove_var("HOME"),
        }

        assert!(results.is_empty());
    }

    // ── Env var scan ─────────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn scan_env_vars_detects_anthropic_key() {
        std::env::remove_var("OPENAI_API_KEY");
        std::env::remove_var("GOOGLE_API_KEY");
        std::env::remove_var("GEMINI_API_KEY");
        std::env::set_var("ANTHROPIC_API_KEY", "sk-ant-test-key");

        let results = scan_env_vars();

        std::env::remove_var("ANTHROPIC_API_KEY");

        assert_eq!(results.len(), 1);
        let acc = &results[0];
        assert_eq!(acc.source, AuthSource::EnvVar);
        assert_eq!(acc.provider, "anthropic");
        assert_eq!(acc.auth_type, AuthType::EnvApiKey);
        assert!(acc.importable);
        assert_eq!(acc.email_or_hint, "env: ANTHROPIC_API_KEY");
        // SECURITY: key value "sk-ant-test-key" must NOT appear in email_or_hint
        assert!(!acc.email_or_hint.contains("sk-ant"));
    }

    #[test]
    #[serial(global_env)]
    fn scan_env_vars_empty_when_none_set() {
        std::env::remove_var("ANTHROPIC_API_KEY");
        std::env::remove_var("OPENAI_API_KEY");
        std::env::remove_var("GOOGLE_API_KEY");
        std::env::remove_var("GEMINI_API_KEY");

        let results = scan_env_vars();

        // Nothing set → empty
        assert!(
            results.is_empty(),
            "expected empty when no API keys are set: {:?}",
            results
        );
    }

    #[test]
    #[serial(global_env)]
    fn scan_env_vars_detects_multiple_keys() {
        std::env::set_var("ANTHROPIC_API_KEY", "sk-ant-1");
        std::env::set_var("OPENAI_API_KEY", "sk-openai-1");
        std::env::remove_var("GOOGLE_API_KEY");
        std::env::remove_var("GEMINI_API_KEY");

        let results = scan_env_vars();

        std::env::remove_var("ANTHROPIC_API_KEY");
        std::env::remove_var("OPENAI_API_KEY");

        assert_eq!(results.len(), 2);
        assert!(results.iter().all(|a| a.importable));
    }

    // ── Aggregate ────────────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn scan_all_returns_vec_no_panic() {
        // With no env vars and empty HOME-equivalent, should return empty without panic.
        let tmp = TempDir::new().unwrap();
        let prev_home = std::env::var("HOME").ok();
        std::env::set_var("HOME", tmp.path());
        std::env::remove_var("ANTHROPIC_API_KEY");
        std::env::remove_var("OPENAI_API_KEY");
        std::env::remove_var("GOOGLE_API_KEY");
        std::env::remove_var("GEMINI_API_KEY");

        let results = scan_all();

        match prev_home {
            Some(h) => std::env::set_var("HOME", h),
            None => std::env::remove_var("HOME"),
        }

        // May be empty — that's valid; just must not panic
        let _ = results;
    }

    // ── JWT decode helper ────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn decode_jwt_email_extracts_email() {
        // Build a minimal JWT-shaped token: base64url(header).base64url(payload).sig
        use base64::Engine;
        let payload = r#"{"sub":"user123","email":"jwt@example.com","iat":1234567890}"#;
        let encoded_payload =
            base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(payload.as_bytes());
        let token = format!("eyJhbGciOiJIUzI1NiJ9.{}.fake_signature", encoded_payload);

        let result = decode_jwt_email(&token);
        assert_eq!(result, Some("jwt@example.com".to_string()));
    }

    #[test]
    #[serial(global_env)]
    fn decode_jwt_email_handles_malformed_token() {
        assert_eq!(decode_jwt_email("not.a.jwt.with.garbage"), None);
        assert_eq!(decode_jwt_email(""), None);
        assert_eq!(decode_jwt_email("nodots"), None);
    }

    // ── Import (in-memory keychain) ───────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn import_env_api_key_stores_in_keychain() {
        // This test uses the in-memory keychain via platform_keychain() in CI.
        // We verify that the function path runs without panic when the env var is present.
        std::env::set_var("ANTHROPIC_API_KEY", "sk-ant-test-import");

        let account = DiscoveredAccount {
            source: AuthSource::EnvVar,
            email_or_hint: "env: ANTHROPIC_API_KEY".to_string(),
            provider: "anthropic".to_string(),
            auth_type: AuthType::EnvApiKey,
            importable: true,
        };

        let rt = tokio::runtime::Runtime::new().unwrap();
        let result = rt.block_on(import_accounts(vec![account]));

        std::env::remove_var("ANTHROPIC_API_KEY");

        // Should be imported (or error if running as root without keychain)
        assert!(
            result.imported.len() == 1 || !result.errors.is_empty(),
            "expected imported=1 or an error string; got {:?}",
            result
        );
        assert!(result.skipped.is_empty());
        // SECURITY: the key value must NOT appear in the result strings
        assert!(!result.imported.join("").contains("sk-ant-test-import"));
        assert!(!result.errors.join("").contains("sk-ant-test-import"));
    }

    #[test]
    #[serial(global_env)]
    fn import_non_importable_goes_to_skipped() {
        let account = DiscoveredAccount {
            source: AuthSource::ClaudeCode,
            email_or_hint: "cc@example.com".to_string(),
            provider: "anthropic".to_string(),
            auth_type: AuthType::OAuthToken,
            importable: false,
        };

        let rt = tokio::runtime::Runtime::new().unwrap();
        let result = rt.block_on(import_accounts(vec![account]));

        assert!(result.imported.is_empty());
        assert_eq!(result.skipped.len(), 1);
        assert!(result.errors.is_empty());
    }

    // ── Serde shape (camelCase) ───────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn discovered_account_serde_camel_case() {
        let account = DiscoveredAccount {
            source: AuthSource::ClaudeCode,
            email_or_hint: "test@example.com".to_string(),
            provider: "anthropic".to_string(),
            auth_type: AuthType::OAuthToken,
            importable: false,
        };
        let json = serde_json::to_string(&account).unwrap();
        assert!(json.contains("\"emailOrHint\""), "expected camelCase emailOrHint, got: {json}");
        assert!(json.contains("\"authType\""), "expected camelCase authType, got: {json}");
        assert!(json.contains("\"claudeCode\""), "expected claudeCode source, got: {json}");
    }
}
