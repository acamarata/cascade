//! # commands::accounts
//!
//! Tauri IPC commands for the Accounts page (Epic 1, T1.1).
//!
//! ## Purpose
//! Surface live per-account quota data and account-roster management to the
//! React frontend. Unlike `commands::usage`, these read the quota file
//! directly (`~/.cascade/accounts/quota.json`) — it is a plain file written
//! by the daemon every 60 s, not a JSON-RPC endpoint.
//!
//! ## Commands
//! | Tauri command       | Action                                                  |
//! |---------------------|---------------------------------------------------------|
//! | `accounts_list`     | Read `quota.json`, return the full typed quota snapshot |
//! | `account_reauth`    | Spawn `cascade-reauth <id>` / `cascade-agy-auth`        |
//! | `account_remove`    | Remove a roster entry from `accounts.json` (no tokens)  |
//! | `accounts_refresh`  | Fire-and-forget `cascade-refresh`                       |
//!
//! ## Constraints
//! - Paths resolved from `$HOME` via `cascade_types::paths` — never hardcoded.
//! - Subprocess spawns use discrete args (never `bash -c`) to avoid injection.
//! - Blocking subprocess calls run inside `spawn_blocking` so the async runtime
//!   is never blocked.
//! - `account_remove` only edits the roster — it never deletes auth tokens.
//!
//! ## SPORT
//! MASTER-COMMANDS.md — accounts IPC commands (T-E01-01)

use serde::{Deserialize, Serialize};
use tauri::State;
use tracing::debug;

use cascade_types::paths::global_cascade_dir;

use crate::error::CascadeError;
use crate::state::AppState;

// ── Wire types (mirror quota.json; serde passthrough for opaque sub-objects) ──

/// One rolling-window utilization figure (5-hour, 7-day, etc.).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct QuotaWindow {
    /// Unix epoch (seconds, may be fractional) when this window resets.
    #[serde(default)]
    pub resets_at: Option<f64>,
    /// Human-readable countdown, e.g. "4h 05m".
    #[serde(default)]
    pub resets_in: Option<String>,
    /// Percent of the window consumed (0–100).
    #[serde(default)]
    pub utilization: Option<f64>,
}

/// Monthly extra-usage (credit) block. Fields are nullable in the source file.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ExtraUsage {
    #[serde(default)]
    pub currency: Option<String>,
    #[serde(default)]
    pub monthly_limit: Option<f64>,
    #[serde(default)]
    pub used_credits: Option<f64>,
    #[serde(default)]
    pub utilization: Option<f64>,
    #[serde(default)]
    pub is_enabled: Option<bool>,
    #[serde(default)]
    pub disabled_reason: Option<String>,
    /// Daily breakdown (provider-specific shape) — passed through opaque.
    #[serde(default)]
    pub daily: Option<serde_json::Value>,
    /// Weekly breakdown (provider-specific shape) — passed through opaque.
    #[serde(default)]
    pub weekly: Option<serde_json::Value>,
    #[serde(default)]
    pub decimal_places: Option<i64>,
}

/// Per-account usage block.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AccountUsage {
    #[serde(default)]
    pub five_hour: Option<QuotaWindow>,
    #[serde(default)]
    pub seven_day: Option<QuotaWindow>,
    #[serde(default)]
    pub seven_day_opus: Option<QuotaWindow>,
    #[serde(default)]
    pub seven_day_sonnet: Option<QuotaWindow>,
    #[serde(default)]
    pub extra_usage: Option<ExtraUsage>,
}

/// One account row in `quota.json`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AccountQuota {
    /// Stable account id, e.g. "claude-acc1".
    pub account: String,
    /// Provider family: claude / codex / gemini / opencode / gfp.
    pub provider: String,
    /// Display email or label (may be a freeform note).
    #[serde(default)]
    pub email: Option<String>,
    /// Liveness status: "ok" / "auth_dead" / "unknown".
    #[serde(default)]
    pub status: Option<String>,
    /// Unix epoch (seconds) of the last successful quota pull.
    #[serde(default)]
    pub last_pull_at: Option<f64>,
    /// True when the provider hides exact numbers (opaque quota).
    #[serde(default)]
    pub quota_opaque: Option<bool>,
    /// Number of keys in a GFP pool account (`gfp` provider only).
    #[serde(default)]
    pub key_count: Option<u32>,
    /// True when the daemon holds valid credentials for this account. `None`
    /// (field absent) is treated as authenticated by the frontend so the UI
    /// degrades gracefully during the daemon rollout of this field.
    #[serde(default)]
    pub authenticated: Option<bool>,
    /// The usage breakdown for this account.
    #[serde(default)]
    pub usage: Option<AccountUsage>,
}

/// Top-level shape of `quota.json`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AccountsQuota {
    #[serde(default)]
    pub accounts: Vec<AccountQuota>,
}

// ── Path helpers ──────────────────────────────────────────────────────────────

/// `~/.cascade/accounts/quota.json`.
fn quota_path() -> std::path::PathBuf {
    global_cascade_dir().join("accounts").join("quota.json")
}

/// `~/.cascade/accounts/accounts.json`.
fn roster_path() -> std::path::PathBuf {
    global_cascade_dir().join("accounts").join("accounts.json")
}

/// `~/.cascade/bin/<name>`.
fn bin_path(name: &str) -> std::path::PathBuf {
    global_cascade_dir().join("bin").join(name)
}

// ── Commands ──────────────────────────────────────────────────────────────────

/// Read `~/.cascade/accounts/quota.json` and return the typed snapshot.
///
/// JS: `invoke("accounts_list")`
///
/// Returns an empty `accounts` list (not an error) when the file does not yet
/// exist — the daemon may not have written its first pull.
#[tauri::command]
pub async fn accounts_list(_state: State<'_, AppState>) -> Result<AccountsQuota, CascadeError> {
    let path = quota_path();
    debug!(?path, "accounts_list invoked");

    if !path.exists() {
        return Ok(AccountsQuota {
            accounts: Vec::new(),
        });
    }

    let raw = std::fs::read_to_string(&path)
        .map_err(|e| CascadeError::Custom(format!("failed to read quota.json: {e}")))?;

    serde_json::from_str::<AccountsQuota>(&raw)
        .map_err(|e| CascadeError::Custom(format!("failed to parse quota.json: {e}")))
}

/// Re-authenticate an account by spawning the matching auth helper.
///
/// JS: `invoke("account_reauth", { accountId })`
///
/// - gemini accounts → `~/.cascade/bin/cascade-agy-auth`
/// - all other families → `~/.cascade/bin/cascade-reauth <accountId>`
///
/// Blocks until the helper exits; returns `Err` with captured output on a
/// non-zero exit. Runs the blocking subprocess off the async runtime.
#[tauri::command]
pub async fn account_reauth(account_id: String) -> Result<(), CascadeError> {
    if account_id.is_empty() {
        return Err(CascadeError::Custom("account_id must not be empty".into()));
    }
    debug!(%account_id, "account_reauth invoked");

    let is_gemini = account_id.starts_with("gemini") || account_id.starts_with("agy");

    tauri::async_runtime::spawn_blocking(move || {
        let output = if is_gemini {
            std::process::Command::new(bin_path("cascade-agy-auth")).output()
        } else {
            std::process::Command::new(bin_path("cascade-reauth"))
                .arg(&account_id)
                .output()
        }
        .map_err(|e| CascadeError::Custom(format!("failed to launch re-auth helper: {e}")))?;

        if output.status.success() {
            Ok(())
        } else {
            let stderr = String::from_utf8_lossy(&output.stderr);
            Err(CascadeError::Custom(format!(
                "re-auth failed: {}",
                stderr.trim()
            )))
        }
    })
    .await
    .map_err(|e| CascadeError::Custom(format!("re-auth task join error: {e}")))?
}

/// Remove an account entry from `~/.cascade/accounts/accounts.json` by id.
///
/// JS: `invoke("account_remove", { accountId })`
///
/// Only removes the roster row — auth tokens / keychain entries are untouched.
/// The write is atomic (`.tmp` then rename). Returns `Err` if the id is absent.
#[tauri::command]
pub async fn account_remove(account_id: String) -> Result<(), CascadeError> {
    if account_id.is_empty() {
        return Err(CascadeError::Custom("account_id must not be empty".into()));
    }
    let path = roster_path();
    debug!(%account_id, ?path, "account_remove invoked");

    let raw = std::fs::read_to_string(&path)
        .map_err(|e| CascadeError::Custom(format!("failed to read accounts.json: {e}")))?;

    let mut root: serde_json::Value = serde_json::from_str(&raw)
        .map_err(|e| CascadeError::Custom(format!("failed to parse accounts.json: {e}")))?;

    let accounts = root
        .get_mut("accounts")
        .and_then(|v| v.as_array_mut())
        .ok_or_else(|| CascadeError::Custom("accounts.json has no 'accounts' array".into()))?;

    let before = accounts.len();
    accounts.retain(|a| a.get("id").and_then(|v| v.as_str()) != Some(account_id.as_str()));

    if accounts.len() == before {
        return Err(CascadeError::Custom(format!(
            "account '{account_id}' not found in roster"
        )));
    }

    let serialized = serde_json::to_string_pretty(&root)
        .map_err(|e| CascadeError::Custom(format!("failed to serialize accounts.json: {e}")))?;

    let tmp_path = {
        let mut p = path.as_os_str().to_owned();
        p.push(".tmp");
        std::path::PathBuf::from(p)
    };
    std::fs::write(&tmp_path, serialized)
        .map_err(|e| CascadeError::Custom(format!("failed to write accounts.json: {e}")))?;
    std::fs::rename(&tmp_path, &path)
        .map_err(|e| CascadeError::Custom(format!("failed to finalize accounts.json: {e}")))?;

    Ok(())
}

/// Force a quota refresh by spawning `~/.cascade/bin/cascade-refresh`.
///
/// JS: `invoke("accounts_refresh")`
///
/// Fire-and-forget: the child is spawned detached and the command returns
/// immediately. The UI re-polls `accounts_list` after a short delay.
#[tauri::command]
pub async fn accounts_refresh() -> Result<(), CascadeError> {
    let bin = bin_path("cascade-refresh");
    debug!(?bin, "accounts_refresh invoked");

    std::process::Command::new(&bin)
        .spawn()
        .map(|_| ())
        .map_err(|e| CascadeError::Custom(format!("failed to spawn cascade-refresh: {e}")))
}
