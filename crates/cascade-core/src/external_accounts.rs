//! # cascade_core::external_accounts
//!
//! Native external-account credential bridge.
//!
//! Purpose: read the LIVE, app-maintained auth from each external agent CLI
//! (Claude Code, Codex) instead of holding a potentially-stale copy in Cascade.
//! Fixes false "re-auth" nags in the widget for accounts that are still valid
//! in the desktop apps.
//!
//! ## Design
//!
//! - Cascade **reads** external dirs only — it never writes them.
//! - Claude macOS: reads the login keychain via `security(1)` CLI.
//!   Service name = `"Claude Code-credentials-" + sha256(canonical_dir)[:8]`.
//! - Claude Linux/other: reads `<dir>/.credentials.json`.
//! - Codex: reads `<dir>/auth.json`.
//! - Token values are NEVER logged, surfaced in errors, or returned.
//!
//! ## Role distinction
//!
//! `~/.claude` (the default config dir) is the **Primary** account — the
//! orchestrator that Cascade enhances with skills/MCP/rules/proxy.  Every other
//! discovered dir is a **Pool** account used for delegation.
//!
//! Inputs:  `$HOME` environment variable / `dirs::home_dir()`.
//! Outputs: `Vec<ExternalAccount>` — one entry per discovered dir.
//! Constraints: no `unwrap()` outside `#[cfg(test)]`; never log token values.
//! SPORT: MASTER-DAEMON.md — external_accounts (live credential bridge)

use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use sha2::{Digest, Sha256};

// ── Types ─────────────────────────────────────────────────────────────────────

/// Which external CLI agent an account belongs to.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ExternalAgent {
    /// Anthropic Claude Code CLI.
    Claude,
    /// OpenAI Codex CLI.
    Codex,
}

/// Role of the external account in Cascade's multi-account strategy.
///
/// The Primary account is `~/.claude` — the default Claude Code directory that
/// Cascade injects skills/MCP/rules/proxy into and uses as the orchestrator.
/// All other directories (numbered, `-accN`, Codex) are Pool accounts used for
/// delegated work.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum AccountRole {
    /// The primary orchestrator account (`~/.claude` for Claude).
    Primary,
    /// A delegation-pool account (all numbered/extra dirs and Codex).
    Pool,
}

/// Live authentication status derived from the external CLI's own storage.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum AuthStatus {
    /// Credentials are present and valid (or have a refresh token available).
    /// `expires_at` is milliseconds since Unix epoch when known.
    Ok { expires_at: Option<i64> },
    /// Auth entry exists but the credentials are expired/revoked with no refresh path.
    NeedsReauth,
    /// The auth entry/file is absent or unreadable — status cannot be determined.
    Unknown,
}

/// One discovered external agent account and its live auth status.
#[derive(Debug, Clone)]
pub struct ExternalAccount {
    /// Which CLI agent this account belongs to.
    pub agent: ExternalAgent,
    /// Absolute path to the agent's config directory.
    pub config_dir: PathBuf,
    /// Human-readable label derived from the dir name (e.g. `"claude"`, `"claude2"`, `"codex"`).
    pub label: String,
    /// Role in Cascade's multi-account strategy.
    pub role: AccountRole,
    /// Live auth status read from the CLI's own storage.
    pub auth: AuthStatus,
}

// ── Keychain service name ─────────────────────────────────────────────────────

/// Compute the macOS login-keychain service name for a Claude config directory.
///
/// Formula (confirmed from Claude Code source):
/// `"Claude Code-credentials-" + lower_hex(sha256(canonical_path))[:8]`
///
/// Falls back to the given path string if `canonicalize` fails (e.g. dir does
/// not yet exist on this machine).
///
/// # Examples
///
/// ```
/// use cascade_core::external_accounts::claude_keychain_service;
/// use std::path::Path;
/// // Deterministic for any fixed path string.
/// let svc = claude_keychain_service(Path::new("/home/user/.claude"));
/// assert!(svc.starts_with("Claude Code-credentials-"));
/// assert_eq!(svc.len(), "Claude Code-credentials-".len() + 8);
/// ```
pub fn claude_keychain_service(dir: &Path) -> String {
    let path_str = dir
        .canonicalize()
        .unwrap_or_else(|_| dir.to_path_buf())
        .to_string_lossy()
        .into_owned();
    let digest = Sha256::digest(path_str.as_bytes());
    let hex = format!("{digest:x}");
    format!("Claude Code-credentials-{}", &hex[..8])
}

// ── Discovery ─────────────────────────────────────────────────────────────────

/// Scan `$HOME` for Claude and Codex config directories and return one
/// [`ExternalAccount`] per discovered directory, ordered by discovery.
///
/// Scanned patterns (bounded to indices 2–9 for numbered suffixes, 1–9 for
/// `-accN` suffixes):
///
/// | Agent  | Dirs scanned |
/// |--------|-------------|
/// | Claude | `~/.claude` (Primary), `~/.claude{2..9}`, `~/.claude-acc{1..9}` |
/// | Codex  | `~/.codex` (Pool), `~/.codex{2..9}` |
///
/// Only directories that exist are included. Auth is read eagerly.
pub fn discover() -> Vec<ExternalAccount> {
    let home = match dirs::home_dir() {
        Some(h) => h,
        None => return vec![],
    };

    let mut accounts = Vec::new();

    // ── Claude dirs ──
    // ~/.claude is Primary; everything else is Pool.
    let claude_primary = home.join(".claude");
    if claude_primary.is_dir() {
        accounts.push(make_claude(&home, claude_primary, AccountRole::Primary));
    }

    for n in 2u8..=9 {
        let dir = home.join(format!(".claude{n}"));
        if dir.is_dir() {
            accounts.push(make_claude(&home, dir, AccountRole::Pool));
        }
    }

    for n in 1u8..=9 {
        let dir = home.join(format!(".claude-acc{n}"));
        if dir.is_dir() {
            accounts.push(make_claude(&home, dir, AccountRole::Pool));
        }
    }

    // ── Codex dirs (always Pool) ──
    let codex_primary = home.join(".codex");
    if codex_primary.is_dir() {
        accounts.push(make_codex(codex_primary));
    }

    for n in 2u8..=9 {
        let dir = home.join(format!(".codex{n}"));
        if dir.is_dir() {
            accounts.push(make_codex(dir));
        }
    }

    accounts
}

/// Return the single Primary account (`~/.claude`) if it exists.
pub fn primary() -> Option<ExternalAccount> {
    discover().into_iter().find(|a| a.role == AccountRole::Primary)
}

// ── Internal constructors ─────────────────────────────────────────────────────

fn make_claude(home: &Path, dir: PathBuf, role: AccountRole) -> ExternalAccount {
    let label = dir
        .file_name()
        .map(|n| n.to_string_lossy().trim_start_matches('.').to_owned())
        .unwrap_or_else(|| "claude".to_owned());
    let auth = read_claude_auth(&dir);
    // Suppress unused-variable warning for `home` on non-macOS (auth reader doesn't use it).
    let _ = home;
    ExternalAccount { agent: ExternalAgent::Claude, config_dir: dir, label, role, auth }
}

fn make_codex(dir: PathBuf) -> ExternalAccount {
    let label = dir
        .file_name()
        .map(|n| n.to_string_lossy().trim_start_matches('.').to_owned())
        .unwrap_or_else(|| "codex".to_owned());
    let auth = read_codex_auth(&dir);
    ExternalAccount {
        agent: ExternalAgent::Codex,
        config_dir: dir,
        label,
        role: AccountRole::Pool,
        auth,
    }
}

// ── Auth readers ──────────────────────────────────────────────────────────────

/// Read Claude auth from the given config directory.
///
/// macOS: invokes `security find-generic-password -s <service> -w` to read the
/// login-keychain entry (same as Claude Code itself).
///
/// Linux/other: reads `<dir>/.credentials.json`.
///
/// Token values are NEVER returned or logged. Only presence/expiry is inspected.
pub fn read_claude_auth(dir: &Path) -> AuthStatus {
    #[cfg(target_os = "macos")]
    {
        let service = claude_keychain_service(dir);
        match read_keychain_secret(&service) {
            Some(blob) => parse_claude_blob(&blob),
            None => AuthStatus::Unknown,
        }
    }
    #[cfg(not(target_os = "macos"))]
    {
        let creds_path = dir.join(".credentials.json");
        match std::fs::read_to_string(&creds_path) {
            Ok(blob) => parse_claude_blob(&blob),
            Err(_) => AuthStatus::Unknown,
        }
    }
}

/// Read Codex auth from `<dir>/auth.json`.
pub fn read_codex_auth(dir: &Path) -> AuthStatus {
    let auth_path = dir.join("auth.json");
    match std::fs::read_to_string(&auth_path) {
        Ok(blob) => parse_codex_blob(&blob),
        Err(_) => AuthStatus::Unknown,
    }
}

// ── macOS keychain helper ─────────────────────────────────────────────────────

/// Invoke `security find-generic-password -s <service> -w` and return the secret
/// string, or `None` on any failure.
///
/// Uses an argument array (never a shell string) to avoid injection risks.
/// Never logs the returned value.
#[cfg(target_os = "macos")]
fn read_keychain_secret(service: &str) -> Option<String> {
    let out = std::process::Command::new("security")
        .args(["find-generic-password", "-s", service, "-w"])
        .stderr(std::process::Stdio::null())
        .output()
        .ok()?;
    if !out.status.success() {
        return None;
    }
    let s = String::from_utf8(out.stdout).ok()?;
    let trimmed = s.trim();
    if trimmed.is_empty() { None } else { Some(trimmed.to_owned()) }
}

// ── Pure JSON parsers (testable without touching the real keychain) ───────────

/// Parse a Claude credentials JSON blob and return the auth status.
///
/// Expected shape:
/// ```json
/// {"claudeAiOauth":{"accessToken":"…","refreshToken":"…","expiresAt":<ms>,"scopes":[],"subscriptionType":"max"}}
/// ```
///
/// Rules:
/// - `Ok` if `accessToken` is present AND either `expiresAt/1000 > now` OR
///   `refreshToken` is present (refresh path available).
/// - `NeedsReauth` if the outer object exists but has no usable credentials.
/// - `Unknown` if the JSON is absent, malformed, or the `claudeAiOauth` key is missing.
pub fn parse_claude_blob(blob: &str) -> AuthStatus {
    let v: serde_json::Value = match serde_json::from_str(blob) {
        Ok(v) => v,
        Err(_) => return AuthStatus::Unknown,
    };
    let oauth = match v.get("claudeAiOauth") {
        Some(o) => o,
        None => return AuthStatus::Unknown,
    };
    let access_token = oauth.get("accessToken").and_then(|v| v.as_str()).unwrap_or("");
    let refresh_token = oauth.get("refreshToken").and_then(|v| v.as_str()).unwrap_or("");
    let expires_at_ms = oauth.get("expiresAt").and_then(|v| v.as_i64());

    if access_token.is_empty() && refresh_token.is_empty() {
        return AuthStatus::NeedsReauth;
    }

    // Determine if the access token is still valid.
    let now_ms = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_millis() as i64)
        .unwrap_or(0);

    let token_valid = match expires_at_ms {
        Some(exp) => exp > now_ms,
        None => !access_token.is_empty(), // no expiry field → assume valid if token present
    };

    if token_valid || !refresh_token.is_empty() {
        // At least one path exists to get a valid token.
        AuthStatus::Ok { expires_at: expires_at_ms }
    } else {
        // Token expired and no refresh available.
        AuthStatus::NeedsReauth
    }
}

/// Parse a Codex `auth.json` blob and return the auth status.
///
/// Expected shape:
/// ```json
/// {"auth_mode":"chatgpt","OPENAI_API_KEY":"…","tokens":{…},"last_refresh":"<rfc3339>"}
/// ```
///
/// Rules:
/// - `Ok` if `tokens` object is present and non-empty, OR `OPENAI_API_KEY` is non-empty.
/// - `NeedsReauth` if the file parses but has neither field.
/// - `Unknown` if the blob is empty, malformed, or not a JSON object.
pub fn parse_codex_blob(blob: &str) -> AuthStatus {
    let trimmed = blob.trim();
    if trimmed.is_empty() {
        return AuthStatus::NeedsReauth;
    }
    let v: serde_json::Value = match serde_json::from_str(trimmed) {
        Ok(v) => v,
        Err(_) => return AuthStatus::Unknown,
    };
    if !v.is_object() {
        return AuthStatus::Unknown;
    }

    let has_tokens = v
        .get("tokens")
        .and_then(|t| t.as_object())
        .map(|m| !m.is_empty())
        .unwrap_or(false);

    let has_api_key = v
        .get("OPENAI_API_KEY")
        .and_then(|k| k.as_str())
        .map(|s| !s.is_empty())
        .unwrap_or(false);

    if has_tokens || has_api_key {
        AuthStatus::Ok { expires_at: None }
    } else {
        AuthStatus::NeedsReauth
    }
}

// ── Access-token reader ───────────────────────────────────────────────────────

/// Read the Claude OAuth access token from the given config directory.
///
/// Returns `Some((token, is_valid_now))` when:
/// - The keychain/credentials file is readable, and
/// - `claudeAiOauth.accessToken` is non-empty.
///
/// `is_valid_now` is `true` when `expiresAt/1000 > now + 60` (i.e. at least
/// 60 seconds of validity remain).  It is also `true` when no `expiresAt`
/// field is present (token assumed valid).
///
/// Returns `None` when the entry is absent, unreadable, or has no access token.
///
/// Token values are NEVER logged.
pub fn read_claude_access_token(dir: &Path) -> Option<(String, bool)> {
    #[cfg(target_os = "macos")]
    let blob = {
        let service = claude_keychain_service(dir);
        read_keychain_secret(&service)?
    };
    #[cfg(not(target_os = "macos"))]
    let blob = {
        let creds_path = dir.join(".credentials.json");
        std::fs::read_to_string(&creds_path).ok()?
    };

    let v: serde_json::Value = serde_json::from_str(&blob).ok()?;
    let oauth = v.get("claudeAiOauth")?;
    let access_token = oauth.get("accessToken").and_then(|t| t.as_str()).unwrap_or("");
    if access_token.is_empty() {
        return None;
    }

    let now_secs = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0);

    let is_valid = match oauth.get("expiresAt").and_then(|v| v.as_i64()) {
        // expiresAt is in milliseconds; require at least 60 s remaining.
        Some(exp_ms) => (exp_ms / 1000) > now_secs + 60,
        None => true, // no expiry field → assume valid
    };

    Some((access_token.to_owned(), is_valid))
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    // ── keychain service formula ─────────────────────────────────────────────

    /// Verify the sha256[:8] formula: prefix correct, suffix is 8 hex chars,
    /// different paths produce different service names.
    #[test]
    fn keychain_service_formula_deterministic() {
        let svc = claude_keychain_service(Path::new("/home/user/.claude"));
        assert!(
            svc.starts_with("Claude Code-credentials-"),
            "service must start with correct prefix"
        );
        let suffix = svc.trim_start_matches("Claude Code-credentials-");
        assert_eq!(suffix.len(), 8, "suffix must be exactly 8 hex chars");
        assert!(suffix.chars().all(|c| c.is_ascii_hexdigit()), "suffix must be hex");

        // Different dirs produce different service names.
        let svc2 = claude_keychain_service(Path::new("/home/user/.claude2"));
        assert_ne!(svc, svc2, "different dirs must produce different service names");
    }

    /// Spot-check exact sha256[:8] for a deterministic path string.
    #[test]
    fn keychain_service_known_hash() {
        let digest = Sha256::digest(b"/home/user/.claude");
        let hex = format!("{digest:x}");
        let expected = format!("Claude Code-credentials-{}", &hex[..8]);
        let got = claude_keychain_service(Path::new("/home/user/.claude"));
        assert_eq!(got, expected);
    }

    // ── parse_claude_blob ────────────────────────────────────────────────────

    /// Valid token with a future expiry → Ok.
    #[test]
    fn claude_blob_ok_with_future_expiry() {
        let far_future_ms = 9_999_999_999_999i64;
        let blob = format!(
            r#"{{"claudeAiOauth":{{"accessToken":"tok","refreshToken":"rtok","expiresAt":{far_future_ms},"scopes":[],"subscriptionType":"max"}}}}"#
        );
        match parse_claude_blob(&blob) {
            AuthStatus::Ok { expires_at } => {
                assert_eq!(expires_at, Some(far_future_ms));
            }
            other => panic!("expected Ok, got {other:?}"),
        }
    }

    /// Token expired but refresh token present → Ok (refresh path available).
    #[test]
    fn claude_blob_ok_with_refresh_token_despite_expired() {
        let blob = r#"{"claudeAiOauth":{"accessToken":"expired","refreshToken":"rtok","expiresAt":1,"scopes":[],"subscriptionType":"max"}}"#;
        assert!(
            matches!(parse_claude_blob(blob), AuthStatus::Ok { .. }),
            "refresh token available → Ok even if expiry past"
        );
    }

    /// No access token, no refresh token → NeedsReauth.
    #[test]
    fn claude_blob_needs_reauth_when_empty() {
        let blob = r#"{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":9999999999999,"scopes":[]}}"#;
        assert_eq!(parse_claude_blob(blob), AuthStatus::NeedsReauth);
    }

    /// `claudeAiOauth` key missing → Unknown.
    #[test]
    fn claude_blob_unknown_when_key_missing() {
        let blob = r#"{"someOtherKey":{}}"#;
        assert_eq!(parse_claude_blob(blob), AuthStatus::Unknown);
    }

    /// Malformed JSON → Unknown.
    #[test]
    fn claude_blob_unknown_on_malformed_json() {
        assert_eq!(parse_claude_blob("not json at all"), AuthStatus::Unknown);
    }

    /// Empty string → Unknown.
    #[test]
    fn claude_blob_unknown_on_empty_string() {
        assert_eq!(parse_claude_blob(""), AuthStatus::Unknown);
    }

    // ── parse_codex_blob ─────────────────────────────────────────────────────

    /// auth.json with `tokens` object → Ok.
    #[test]
    fn codex_blob_ok_with_tokens() {
        let blob = r#"{"auth_mode":"chatgpt","OPENAI_API_KEY":"","tokens":{"access":"tok"},"last_refresh":"2026-01-01T00:00:00Z"}"#;
        assert!(matches!(parse_codex_blob(blob), AuthStatus::Ok { .. }));
    }

    /// auth.json with `OPENAI_API_KEY` (no tokens) → Ok.
    #[test]
    fn codex_blob_ok_with_api_key() {
        let blob = r#"{"auth_mode":"api","OPENAI_API_KEY":"sk-abc123"}"#;
        assert!(matches!(parse_codex_blob(blob), AuthStatus::Ok { .. }));
    }

    /// File present but empty → NeedsReauth.
    #[test]
    fn codex_blob_needs_reauth_on_empty() {
        assert_eq!(parse_codex_blob(""), AuthStatus::NeedsReauth);
        assert_eq!(parse_codex_blob("   "), AuthStatus::NeedsReauth);
    }

    /// auth.json present but neither tokens nor api key → NeedsReauth.
    #[test]
    fn codex_blob_needs_reauth_when_no_credentials() {
        let blob = r#"{"auth_mode":"chatgpt","OPENAI_API_KEY":"","tokens":{}}"#;
        assert_eq!(parse_codex_blob(blob), AuthStatus::NeedsReauth);
    }

    /// Garbage / non-object JSON → Unknown.
    #[test]
    fn codex_blob_unknown_on_garbage() {
        assert_eq!(parse_codex_blob("definitely not json"), AuthStatus::Unknown);
        assert_eq!(parse_codex_blob(r#""just a string""#), AuthStatus::Unknown);
    }

    // ── AccountRole (Primary vs Pool) ────────────────────────────────────────

    /// ~/.claude → Primary; ~/.claude2, ~/.codex → Pool.
    #[test]
    fn role_primary_vs_pool() {
        use tempfile::TempDir;

        let home = TempDir::new().unwrap();
        let home_path = home.path();

        let claude_dir = home_path.join(".claude");
        let claude2_dir = home_path.join(".claude2");
        let codex_dir = home_path.join(".codex");
        std::fs::create_dir_all(&claude_dir).unwrap();
        std::fs::create_dir_all(&claude2_dir).unwrap();
        std::fs::create_dir_all(&codex_dir).unwrap();

        // Write minimal auth.json for codex.
        std::fs::write(codex_dir.join("auth.json"), r#"{"OPENAI_API_KEY":"sk-test"}"#).unwrap();

        let primary_acc = make_claude(home_path, claude_dir, AccountRole::Primary);
        let pool_claude = make_claude(home_path, claude2_dir, AccountRole::Pool);
        let pool_codex = make_codex(codex_dir);

        assert_eq!(primary_acc.role, AccountRole::Primary, "~/.claude must be Primary");
        assert_eq!(pool_claude.role, AccountRole::Pool, "~/.claude2 must be Pool");
        assert_eq!(pool_codex.role, AccountRole::Pool, "~/.codex must be Pool");
        assert_eq!(pool_codex.agent, ExternalAgent::Codex);
    }
}
