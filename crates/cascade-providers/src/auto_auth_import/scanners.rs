use cascade_types::auto_auth::{AuthSource, AuthType, DiscoveredAccount};
#[allow(unused_imports)]
use tracing::warn;

use super::helpers::{decode_jwt_email, normalize_provider};

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
            .or_else(|| json.get("user").and_then(|u| u.get("email")))
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
            .or_else(|| json.get("cursorAuth").and_then(|ca| ca.get("cachedEmail")))
            .and_then(|v| v.as_str())
            .unwrap_or("")
            .to_string();

        if email.is_empty() {
            return vec![];
        }

        vec![DiscoveredAccount {
            source: AuthSource::Cursor,
            email_or_hint: email,
            provider: "anthropic".to_string(), // Cursor primarily uses Anthropic models
            auth_type: AuthType::OAuthToken,
            importable: false,
        }]
    }

    // Non-macOS platforms: no-op in P3 (Windows/Linux Keychain deferred to P4)
    #[cfg(not(target_os = "macos"))]
    vec![]
}

// ── Antigravity scanner ───────────────────────────────────────────────────────

/// Scan Antigravity for an authenticated account.
///
/// # Purpose
/// Reads `~/.config/antigravity/config.json` (Linux/macOS cross-platform path)
/// and `~/Library/Application Support/Antigravity/config.json` (macOS native).
/// Extracts `email` / `user.email` if present.
///
/// # Outputs
/// Up to one `DiscoveredAccount` with `source=Antigravity`, `importable=false`.
///
/// # Constraints
/// - Read-only. No file is written or modified.
/// - Auth token value is never extracted or logged.
pub fn scan_antigravity() -> Vec<DiscoveredAccount> {
    let home = match dirs::home_dir() {
        Some(h) => h,
        None => return vec![],
    };

    // Mutated only under the macOS cfg block below; immutable on other targets.
    #[allow(unused_mut)]
    let mut candidates = vec![
        home.join(".config").join("antigravity").join("config.json"),
        home.join(".antigravity").join("config.json"),
    ];

    // macOS: also check Application Support
    #[cfg(target_os = "macos")]
    candidates.push(
        home.join("Library")
            .join("Application Support")
            .join("Antigravity")
            .join("config.json"),
    );

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
            Err(_) => continue,
        };

        let email = json
            .get("email")
            .or_else(|| json.get("user").and_then(|u| u.get("email")))
            .or_else(|| json.get("userEmail"))
            .and_then(|v| v.as_str())
            .unwrap_or("")
            .to_string();

        let hint = if email.is_empty() {
            "Antigravity (account unknown)".to_string()
        } else {
            email
        };

        return vec![DiscoveredAccount {
            source: AuthSource::Antigravity,
            email_or_hint: hint,
            provider: "anthropic".to_string(),
            auth_type: AuthType::OAuthToken,
            importable: false,
        }];
    }

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
    results.extend(scan_antigravity());
    results.extend(scan_env_vars());
    results
}
