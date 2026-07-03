//! Cascade Continuity — daemon-owned reset-time triggers so an interactive
//! Claude Code session can resume automatically after a usage-cap window
//! resets.
//!
//! Purpose: A user hits a 5-hour (or weekly) usage cap mid-session. Rather
//! than babysitting a clock, they leave a "continuation intent" on disk. This
//! module's background task watches `~/.cascade/accounts/quota.json` for the
//! named account's utilization to drop back under 100%, then either resumes
//! the Claude Code session headlessly (`mode: "resume"`) or fires a local
//! notification (`mode: "notify"`).
//!
//! Inputs:
//!   - `~/.cascade/continuity/<id>.json` — one file per intent (see
//!     [`ContinuityIntent`]), written by `cascade continuity add`.
//!   - `~/.cascade/accounts/quota.json` — read-only; written by the fleet
//!     poller (`accounts_store::write_quota_json`). Shape:
//!     `{"accounts":[{"account","usage":{"five_hour":{"utilization","resets_at"}}}]}`.
//!
//! Outputs:
//!   - Rewrites the intent file's `fired_at` (and optionally `last_error`)
//!     in place once the intent fires.
//!   - `mode: "resume"` spawns a bounded, non-interactive
//!     `claude --resume <session_id> -p "<prompt>"` subprocess.
//!   - `mode: "notify"` fires a best-effort macOS notification (log-only on
//!     other platforms).
//!
//! Constraints:
//!   - Never panics — malformed intent files are skipped with a `warn!`.
//!   - Never double-fires: `fired_at` is written BEFORE the resume subprocess
//!     is launched (idempotency survives a crash mid-fire).
//!   - Never fires while the account is still saturated (utilization >= 100
//!     with no evidence the reset has passed).
//!   - One shot per intent — a failure sets `last_error` but does not retry;
//!     the user re-adds the intent if they want another attempt.
//!   - File ≤ 300 lines.
//!
//! SPORT: `.claude/docs/MASTER-DAEMON.md` — continuity module (E2-S1)

use std::path::{Path, PathBuf};
use std::time::Duration;

use serde::{Deserialize, Serialize};
use tokio_util::sync::CancellationToken;
use tracing::{info, warn};

/// How often the watcher scans `~/.cascade/continuity/*.json` for due intents.
pub const WATCH_INTERVAL: Duration = Duration::from_secs(60);

/// Wall-clock budget for a headless `claude --resume` launch. Chosen to be
/// generous enough for a real turn to complete while still guaranteeing the
/// watcher loop is never blocked indefinitely by a hung subprocess.
pub const RESUME_TIMEOUT: Duration = Duration::from_secs(10 * 60);

// ── Intent schema ─────────────────────────────────────────────────────────────

/// How a continuation intent is delivered once its account is no longer
/// saturated.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "lowercase")]
pub enum ContinuityMode {
    /// Headlessly resume the Claude Code session with `--resume <id> -p <prompt>`.
    #[default]
    Resume,
    /// Fire a local OS notification only; the human resumes manually.
    Notify,
}

/// One continuation intent persisted at `~/.cascade/continuity/<id>.json`.
///
/// Inputs: written by `cascade continuity add`. Outputs: consumed by the
/// watcher task in this module; `fired_at`/`last_error` are rewritten in
/// place by the watcher once the intent is processed.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ContinuityIntent {
    /// Stable identifier for this intent (also the filename stem).
    pub id: String,
    /// The Claude Code session id to resume (`claude --resume <id>`).
    pub session_id: String,
    /// `CLAUDE_CONFIG_DIR` to run the resume under. Defaults to `~/.claude`.
    #[serde(default = "default_config_dir")]
    pub config_dir: String,
    /// Prompt text passed via `-p` on resume. Defaults to `"continue"`.
    #[serde(default = "default_prompt")]
    pub prompt: String,
    /// Fleet account id to watch in `quota.json` (e.g. `"claude"`, `"claude2"`).
    #[serde(default = "default_account_id")]
    pub account_id: String,
    /// Delivery mode: headless resume or notify-only.
    #[serde(default)]
    pub mode: ContinuityMode,
    /// Unix epoch seconds when the intent was created.
    pub created_at: i64,
    /// Unix epoch seconds when the intent fired; `None` while pending.
    #[serde(default)]
    pub fired_at: Option<i64>,
    /// Free-form note set by the user (e.g. reminder of what the session was doing).
    #[serde(default)]
    pub note: Option<String>,
    /// Set when the fire attempt failed. `fired_at` is still set (one-shot —
    /// no retry storms); the user re-adds the intent if they want another try.
    #[serde(default)]
    pub last_error: Option<String>,
}

fn default_config_dir() -> String {
    dirs::home_dir()
        .map(|h| h.join(".claude").to_string_lossy().into_owned())
        .unwrap_or_else(|| ".claude".to_owned())
}

fn default_prompt() -> String {
    "continue".to_owned()
}

fn default_account_id() -> String {
    "claude".to_owned()
}

impl ContinuityIntent {
    /// `true` once a fire attempt has been made (success or failure) —
    /// the core idempotency guard.
    pub fn is_fired(&self) -> bool {
        self.fired_at.is_some()
    }
}

// ── Paths ─────────────────────────────────────────────────────────────────────

/// Returns `~/.cascade/continuity/` (may not exist on a fresh install).
pub fn continuity_dir() -> PathBuf {
    let home = std::env::var_os("HOME")
        .map(PathBuf::from)
        .or_else(dirs::home_dir)
        .unwrap_or_else(|| PathBuf::from("~"));
    home.join(".cascade").join("continuity")
}

/// Returns `~/.cascade/accounts/quota.json` (read-only from this module).
fn quota_json_path() -> PathBuf {
    let home = std::env::var_os("HOME")
        .map(PathBuf::from)
        .or_else(dirs::home_dir)
        .unwrap_or_else(|| PathBuf::from("~"));
    home.join(".cascade").join("accounts").join("quota.json")
}

fn intent_path(dir: &Path, id: &str) -> PathBuf {
    dir.join(format!("{id}.json"))
}

// ── I/O ───────────────────────────────────────────────────────────────────────

/// Write `intent` atomically (tmp → rename) to `<dir>/<intent.id>.json`,
/// 0600 on unix.
///
/// Constraints: creates `dir` if absent. POSIX-atomic when tmp and dst share
/// a filesystem (same directory — always true here).
pub fn write_intent(dir: &Path, intent: &ContinuityIntent) -> std::io::Result<()> {
    std::fs::create_dir_all(dir)?;
    let path = intent_path(dir, &intent.id);
    let bytes = serde_json::to_vec_pretty(intent)?;
    let tmp = path.with_extension("json.tmp");
    std::fs::write(&tmp, &bytes)?;
    #[cfg(unix)]
    {
        use std::fs::Permissions;
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&tmp, Permissions::from_mode(0o600))?;
    }
    std::fs::rename(&tmp, &path)?;
    Ok(())
}

/// Read one intent file. Returns `None` (with a `warn!`) on malformed JSON —
/// callers must never let a bad file crash the watch loop.
pub fn read_intent(path: &Path) -> Option<ContinuityIntent> {
    let bytes = std::fs::read(path).ok()?;
    match serde_json::from_slice(&bytes) {
        Ok(intent) => Some(intent),
        Err(e) => {
            warn!(path = %path.display(), error = %e, "continuity: skipping malformed intent file");
            None
        }
    }
}

/// List every intent currently on disk (fired or pending), skipping
/// malformed files. Never fails — an unreadable directory yields an empty list.
pub fn list_intents(dir: &Path) -> Vec<ContinuityIntent> {
    let entries = match std::fs::read_dir(dir) {
        Ok(e) => e,
        Err(_) => return Vec::new(),
    };
    entries
        .filter_map(|e| e.ok())
        .map(|e| e.path())
        .filter(|p| p.extension().and_then(|s| s.to_str()) == Some("json"))
        .filter_map(|p| read_intent(&p))
        .collect()
}

/// Delete an intent file by id. `Ok(false)` if it did not exist.
pub fn remove_intent(dir: &Path, id: &str) -> std::io::Result<bool> {
    let path = intent_path(dir, id);
    if !path.exists() {
        return Ok(false);
    }
    std::fs::remove_file(&path)?;
    Ok(true)
}

// ── Fire-eligibility (pure — unit-testable without any live account) ─────────

/// Minimal quota snapshot needed to decide fire-eligibility. Deliberately
/// narrow (not the full `quota.json` `Value`) so the predicate stays a pure
/// function over plain data — no JSON parsing inside the decision logic.
#[derive(Debug, Clone, Copy, Default)]
pub struct QuotaSnapshot {
    /// `usage.five_hour.utilization`, 0.0-100.0. `None` when unknown/absent.
    pub five_hour_utilization: Option<f64>,
    /// `usage.seven_day.utilization`, 0.0-100.0. `None` when unknown/absent
    /// (some accounts carry no weekly window — that must not block firing).
    pub seven_day_utilization: Option<f64>,
}

/// `true` when `intent` should fire against the given account snapshot.
///
/// Rules (in order):
///   1. Already fired → never eligible again (idempotency).
///   2. No matching account / no five-hour utilization data → not eligible
///      (never guess; a missing account must not be treated as "reset").
///   3. Five-hour `utilization >= 100.0` → not eligible (still saturated).
///   4. Seven-day `utilization >= 100.0` (when present) → not eligible —
///      a weekly-capped account would immediately re-hit the cap and burn
///      the one-shot intent without resuming anything. A missing seven-day
///      window does NOT block (rule 2 already anchors on real 5h data).
///   5. Otherwise → eligible (both known windows have headroom).
pub fn is_eligible_to_fire(intent: &ContinuityIntent, snapshot: Option<QuotaSnapshot>) -> bool {
    if intent.is_fired() {
        return false;
    }
    let Some(snap) = snapshot else { return false };
    let Some(five_hour) = snap.five_hour_utilization else {
        return false;
    };
    if five_hour >= 100.0 {
        return false;
    }
    if let Some(seven_day) = snap.seven_day_utilization {
        if seven_day >= 100.0 {
            return false;
        }
    }
    true
}

/// Extract a [`QuotaSnapshot`] for `account_id` from a parsed `quota.json`
/// document. Returns `None` when the account is absent or has no usage block
/// — callers treat that as "cannot verify reset, do not fire".
pub fn snapshot_for_account(quota_doc: &serde_json::Value, account_id: &str) -> Option<QuotaSnapshot> {
    let accounts = quota_doc.get("accounts")?.as_array()?;
    let entry = accounts
        .iter()
        .find(|a| a.get("account").and_then(|v| v.as_str()) == Some(account_id))?;
    let window_util = |window: &str| {
        entry
            .get("usage")
            .and_then(|u| u.get(window))
            .and_then(|w| w.get("utilization"))
            .and_then(|v| v.as_f64())
    };
    Some(QuotaSnapshot {
        five_hour_utilization: window_util("five_hour"),
        seven_day_utilization: window_util("seven_day"),
    })
}

// ── Watcher task ──────────────────────────────────────────────────────────────

/// Spawn the continuity watcher as a background Tokio task.
///
/// Ticks every [`WATCH_INTERVAL`]; on each tick reads every intent under
/// `~/.cascade/continuity/`, checks eligibility against a freshly re-read
/// `quota.json`, and fires due intents one at a time. Exits cleanly when
/// `shutdown` is cancelled. Never panics.
pub fn spawn(shutdown: CancellationToken) -> tokio::task::JoinHandle<()> {
    tokio::spawn(async move {
        info!("continuity: watcher task starting (interval={:?})", WATCH_INTERVAL);
        loop {
            tokio::select! {
                _ = tokio::time::sleep(WATCH_INTERVAL) => {}
                _ = shutdown.cancelled() => {
                    info!("continuity: watcher shutting down");
                    break;
                }
            }
            run_tick().await;
        }
    })
}

/// One watcher pass: scan intents, fire any that are eligible. Isolated from
/// `spawn` so tests can drive a single tick deterministically if needed.
async fn run_tick() {
    let dir = continuity_dir();
    let intents = list_intents(&dir);
    if intents.is_empty() {
        return;
    }

    let quota_doc: Option<serde_json::Value> = std::fs::read(quota_json_path())
        .ok()
        .and_then(|b| serde_json::from_slice(&b).ok());

    for intent in intents {
        if intent.is_fired() {
            continue;
        }
        let snapshot = quota_doc
            .as_ref()
            .and_then(|doc| snapshot_for_account(doc, &intent.account_id));
        if !is_eligible_to_fire(&intent, snapshot) {
            continue;
        }
        fire_intent(&dir, intent).await;
    }
}

/// Mark `intent` fired (BEFORE launching anything — idempotency guard against
/// a crash mid-fire), then dispatch it per `mode`. Any failure is recorded in
/// `last_error`; `fired_at` stays set regardless (one shot, no retries).
async fn fire_intent(dir: &Path, mut intent: ContinuityIntent) {
    let now = chrono::Utc::now().timestamp();
    intent.fired_at = Some(now);
    if let Err(e) = write_intent(dir, &intent) {
        warn!(id = %intent.id, error = %e, "continuity: failed to persist fired_at — skipping fire to avoid double-launch risk");
        return;
    }

    let result = match intent.mode {
        ContinuityMode::Resume => fire_resume(&intent).await,
        ContinuityMode::Notify => fire_notify(&intent),
    };

    if let Err(e) = result {
        warn!(id = %intent.id, mode = ?intent.mode, error = %e, "continuity: fire attempt failed (one-shot — not retrying)");
        intent.last_error = Some(e);
        if let Err(e2) = write_intent(dir, &intent) {
            warn!(id = %intent.id, error = %e2, "continuity: failed to persist last_error");
        }
    } else {
        info!(id = %intent.id, mode = ?intent.mode, "continuity: intent fired");
    }
}

/// Launch a bounded, non-interactive `claude --resume <session_id> -p <prompt>`
/// subprocess under `intent.config_dir`. MCP is stripped and no user settings
/// are loaded so the resume is fully self-contained and does not depend on
/// the caller's shell environment.
async fn fire_resume(intent: &ContinuityIntent) -> Result<(), String> {
    let bin = resolve_claude_bin();
    run_resume_command(
        &bin,
        &intent.config_dir,
        &intent.session_id,
        &intent.prompt,
        RESUME_TIMEOUT,
    )
    .await
}

/// Run the headless resume subprocess with a HARD wall-clock bound.
///
/// Uses `tokio::process::Command` with `kill_on_drop(true)`: when the timeout
/// fires, the `output()` future is dropped, the child receives SIGKILL, and
/// tokio's orphan reaper collects it — no orphaned headless `claude` session
/// keeps consuming account quota, and no blocking-pool thread is leaked.
/// (The previous `spawn_blocking(std Command::output)` version could not be
/// aborted: dropping the timeout future left the child running forever.)
async fn run_resume_command(
    bin: &Path,
    config_dir: &str,
    session_id: &str,
    prompt: &str,
    timeout: Duration,
) -> Result<(), String> {
    let mut cmd = tokio::process::Command::new(bin);
    cmd.env("CLAUDE_CONFIG_DIR", config_dir)
        .arg("--resume")
        .arg(session_id)
        .arg("-p")
        .arg(prompt)
        .arg("--strict-mcp-config")
        .arg("--mcp-config")
        .arg("{\"mcpServers\":{}}")
        .arg("--setting-sources")
        .arg("")
        .stdin(std::process::Stdio::null())
        .stdout(std::process::Stdio::piped())
        .stderr(std::process::Stdio::piped())
        .kill_on_drop(true);

    match tokio::time::timeout(timeout, cmd.output()).await {
        Err(_) => Err(format!(
            "resume timed out after {timeout:?} (subprocess killed)"
        )),
        Ok(Err(e)) => Err(format!("failed to spawn claude: {e}")),
        Ok(Ok(output)) => {
            if output.status.success() {
                Ok(())
            } else {
                Err(format!(
                    "claude --resume exited with {}: {}",
                    output.status,
                    String::from_utf8_lossy(&output.stderr)
                ))
            }
        }
    }
}

/// Fire a best-effort macOS notification. Non-macOS platforms log-only —
/// there is no cross-platform equivalent worth shelling out for here.
#[cfg(target_os = "macos")]
fn fire_notify(intent: &ContinuityIntent) -> Result<(), String> {
    let body = intent
        .note
        .clone()
        .unwrap_or_else(|| format!("session {} is ready to resume", intent.session_id));
    let script = format!(
        "display notification {:?} with title \"Cascade Continuity\"",
        body
    );
    std::process::Command::new("osascript")
        .arg("-e")
        .arg(script)
        .output()
        .map(|_| ())
        .map_err(|e| format!("osascript failed: {e}"))
}

/// Non-macOS notify fallback: log-only, always succeeds.
#[cfg(not(target_os = "macos"))]
fn fire_notify(intent: &ContinuityIntent) -> Result<(), String> {
    info!(id = %intent.id, "continuity: notify mode fired (log-only — no notification backend on this platform)");
    Ok(())
}

/// Resolve the `claude` CLI binary path. Under launchd the daemon inherits a
/// minimal PATH that usually excludes Homebrew, so probe common install
/// locations first and fall back to a bare `claude` for PATH-based lookup.
/// Mirrors `fleet_poller::resolve_claude_bin` (kept local and duplicated on
/// purpose — this module must not depend on fleet_poller's private items).
fn resolve_claude_bin() -> PathBuf {
    if let Some(home) = dirs::home_dir() {
        for c in [
            PathBuf::from("/opt/homebrew/bin/claude"),
            PathBuf::from("/usr/local/bin/claude"),
            home.join(".local/bin/claude"),
            home.join("bin/claude"),
        ] {
            if c.is_file() {
                return c;
            }
        }
    }
    PathBuf::from("claude")
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    use tempfile::TempDir;

    fn make_intent(id: &str) -> ContinuityIntent {
        ContinuityIntent {
            id: id.to_owned(),
            session_id: "sess-123".to_owned(),
            config_dir: default_config_dir(),
            prompt: default_prompt(),
            account_id: default_account_id(),
            mode: ContinuityMode::Resume,
            created_at: 1_700_000_000,
            fired_at: None,
            note: None,
            last_error: None,
        }
    }

    // ── Serde round-trip ──────────────────────────────────────────────────

    #[test]
    fn intent_round_trips_through_json() {
        let intent = make_intent("abc123");
        let json = serde_json::to_string(&intent).unwrap();
        let back: ContinuityIntent = serde_json::from_str(&json).unwrap();
        assert_eq!(intent, back);
    }

    #[test]
    fn intent_deserializes_with_defaults_when_optional_fields_absent() {
        let json = r#"{"id":"x","session_id":"s","created_at":1}"#;
        let intent: ContinuityIntent = serde_json::from_str(json).unwrap();
        assert_eq!(intent.prompt, "continue");
        assert_eq!(intent.account_id, "claude");
        assert_eq!(intent.mode, ContinuityMode::Resume);
        assert!(intent.fired_at.is_none());
    }

    // ── Atomic write + permissions ────────────────────────────────────────

    #[test]
    fn write_intent_creates_readable_file() {
        let tmp = TempDir::new().unwrap();
        let intent = make_intent("write-test");
        write_intent(tmp.path(), &intent).unwrap();
        let loaded = read_intent(&intent_path(tmp.path(), "write-test")).unwrap();
        assert_eq!(loaded, intent);
        // No leftover tmp file after rename.
        assert!(!intent_path(tmp.path(), "write-test")
            .with_extension("json.tmp")
            .exists());
    }

    #[cfg(unix)]
    #[test]
    fn write_intent_sets_0600_permissions() {
        use std::os::unix::fs::PermissionsExt;
        let tmp = TempDir::new().unwrap();
        let intent = make_intent("perm-test");
        write_intent(tmp.path(), &intent).unwrap();
        let meta = std::fs::metadata(intent_path(tmp.path(), "perm-test")).unwrap();
        assert_eq!(meta.permissions().mode() & 0o777, 0o600);
    }

    #[test]
    fn read_intent_returns_none_for_malformed_json() {
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("bad.json");
        std::fs::write(&path, b"{not valid json").unwrap();
        assert!(read_intent(&path).is_none());
    }

    #[test]
    fn list_intents_skips_malformed_files_without_crashing() {
        let tmp = TempDir::new().unwrap();
        write_intent(tmp.path(), &make_intent("good")).unwrap();
        std::fs::write(tmp.path().join("bad.json"), b"{{{").unwrap();
        let intents = list_intents(tmp.path());
        assert_eq!(intents.len(), 1);
        assert_eq!(intents[0].id, "good");
    }

    #[test]
    fn remove_intent_deletes_existing_file() {
        let tmp = TempDir::new().unwrap();
        write_intent(tmp.path(), &make_intent("rm-test")).unwrap();
        assert!(remove_intent(tmp.path(), "rm-test").unwrap());
        assert!(!intent_path(tmp.path(), "rm-test").exists());
    }

    #[test]
    fn remove_intent_returns_false_when_absent() {
        let tmp = TempDir::new().unwrap();
        assert!(!remove_intent(tmp.path(), "nonexistent").unwrap());
    }

    // ── Fire-eligibility predicate (pure) ────────────────────────────────

    #[test]
    fn eligible_when_utilization_under_100() {
        let intent = make_intent("e1");
        let snap = QuotaSnapshot { five_hour_utilization: Some(3.0), seven_day_utilization: None };
        assert!(is_eligible_to_fire(&intent, Some(snap)));
    }

    #[test]
    fn not_eligible_when_saturated() {
        let intent = make_intent("e2");
        let snap =
            QuotaSnapshot { five_hour_utilization: Some(100.0), seven_day_utilization: None };
        assert!(!is_eligible_to_fire(&intent, Some(snap)));
    }

    #[test]
    fn not_eligible_when_account_missing() {
        let intent = make_intent("e3");
        assert!(!is_eligible_to_fire(&intent, None));
    }

    #[test]
    fn not_eligible_when_utilization_unknown() {
        let intent = make_intent("e4");
        let snap = QuotaSnapshot { five_hour_utilization: None, seven_day_utilization: None };
        assert!(!is_eligible_to_fire(&intent, Some(snap)));
    }

    #[test]
    fn fired_intent_is_never_eligible_again() {
        let mut intent = make_intent("e5");
        intent.fired_at = Some(1_700_000_500);
        let snap = QuotaSnapshot { five_hour_utilization: Some(1.0), seven_day_utilization: None };
        assert!(!is_eligible_to_fire(&intent, Some(snap)));
    }

    /// Regression (E1-S6 review): an account at the WEEKLY cap with 5h
    /// headroom must NOT fire — the resume would immediately re-hit the cap
    /// and burn the one-shot intent.
    #[test]
    fn not_eligible_when_weekly_capped_despite_5h_headroom() {
        let intent = make_intent("e6");
        let snap = QuotaSnapshot {
            five_hour_utilization: Some(60.0),
            seven_day_utilization: Some(100.0),
        };
        assert!(!is_eligible_to_fire(&intent, Some(snap)));
    }

    #[test]
    fn eligible_when_both_windows_have_headroom() {
        let intent = make_intent("e7");
        let snap = QuotaSnapshot {
            five_hour_utilization: Some(60.0),
            seven_day_utilization: Some(80.0),
        };
        assert!(is_eligible_to_fire(&intent, Some(snap)));
    }

    /// A missing weekly window must not block firing (many accounts report
    /// only the 5-hour window).
    #[test]
    fn eligible_when_weekly_window_absent() {
        let intent = make_intent("e8");
        let snap = QuotaSnapshot { five_hour_utilization: Some(10.0), seven_day_utilization: None };
        assert!(is_eligible_to_fire(&intent, Some(snap)));
    }

    // ── snapshot_for_account (JSON extraction) ───────────────────────────

    #[test]
    fn snapshot_for_account_extracts_utilization() {
        let doc = json!({
            "accounts": [
                { "account": "claude", "usage": { "five_hour": { "utilization": 42.5 } } },
                { "account": "claude2", "usage": { "five_hour": { "utilization": 3.0 } } }
            ],
            "queried_at": 1234.0
        });
        let snap = snapshot_for_account(&doc, "claude2").unwrap();
        assert_eq!(snap.five_hour_utilization, Some(3.0));
        assert_eq!(snap.seven_day_utilization, None);
    }

    /// The weekly window must be extracted when present — the eligibility
    /// predicate gates on it (see `not_eligible_when_weekly_capped_*`).
    #[test]
    fn snapshot_for_account_extracts_seven_day_window() {
        let doc = json!({
            "accounts": [{
                "account": "claude",
                "usage": {
                    "five_hour": { "utilization": 60.0 },
                    "seven_day": { "utilization": 100.0 }
                }
            }]
        });
        let snap = snapshot_for_account(&doc, "claude").unwrap();
        assert_eq!(snap.five_hour_utilization, Some(60.0));
        assert_eq!(snap.seven_day_utilization, Some(100.0));
        assert!(!is_eligible_to_fire(&make_intent("sd"), Some(snap)));
    }

    #[test]
    fn snapshot_for_account_none_when_account_absent() {
        let doc = json!({ "accounts": [], "queried_at": 1234.0 });
        assert!(snapshot_for_account(&doc, "claude").is_none());
    }

    #[test]
    fn snapshot_for_account_none_when_usage_null() {
        let doc = json!({
            "accounts": [ { "account": "claude", "usage": { "five_hour": null } } ]
        });
        let snap = snapshot_for_account(&doc, "claude").unwrap();
        assert_eq!(snap.five_hour_utilization, None);
    }

    // ── Idempotency at the fire_intent level ─────────────────────────────

    #[tokio::test]
    async fn fire_intent_sets_fired_at_before_dispatch_even_on_notify_failure() {
        let tmp = TempDir::new().unwrap();
        let mut intent = make_intent("fire-1");
        intent.mode = ContinuityMode::Notify;
        write_intent(tmp.path(), &intent).unwrap();

        fire_intent(tmp.path(), intent.clone()).await;

        let reloaded = read_intent(&intent_path(tmp.path(), "fire-1")).unwrap();
        assert!(reloaded.fired_at.is_some(), "fired_at must be set after fire_intent");
    }

    #[test]
    fn resolve_claude_bin_never_panics() {
        // Just exercise the function on whatever platform runs the test.
        let _ = resolve_claude_bin();
    }

    // ── Timed-out resume must KILL the child (E1-S6 review regression) ────

    /// A hung `claude --resume` (simulated by a script that sleeps far past
    /// the timeout) must be killed when the timeout fires — never left as an
    /// orphaned headless session burning quota. The script records its PID;
    /// after the timeout the PID must be gone.
    #[cfg(unix)]
    #[tokio::test]
    async fn run_resume_command_kills_child_on_timeout() {
        use std::os::unix::fs::PermissionsExt;

        let tmp = TempDir::new().unwrap();
        let pid_file = tmp.path().join("child.pid");
        let script = tmp.path().join("fake-claude.sh");
        std::fs::write(
            &script,
            format!("#!/bin/sh\necho $$ > {}\nsleep 30\n", pid_file.display()),
        )
        .unwrap();
        std::fs::set_permissions(&script, std::fs::Permissions::from_mode(0o755)).unwrap();

        let start = std::time::Instant::now();
        let result = run_resume_command(
            &script,
            "/tmp",
            "sess-timeout",
            "continue",
            Duration::from_millis(300),
        )
        .await;

        // Must report the timeout, and quickly (not the child's 30 s sleep).
        let err = result.expect_err("hung resume must time out");
        assert!(err.contains("timed out"), "err: {err}");
        assert!(start.elapsed() < Duration::from_secs(5), "took {:?}", start.elapsed());

        // The child must be dead shortly after the drop (SIGKILL via
        // kill_on_drop). Poll for the pid file, then `kill -0 <pid>` until it
        // fails. If the pid file never appears the child was killed before it
        // could even write it — that is the desired outcome, not a failure
        // (with the old spawn_blocking bug the orphan survives 30 s, writes
        // its pid within the poll window, and this test fails on kill -0).
        let mut pid: Option<String> = None;
        for _ in 0..40 {
            match std::fs::read_to_string(&pid_file) {
                Ok(s) if !s.trim().is_empty() => {
                    pid = Some(s.trim().to_string());
                    break;
                }
                _ => tokio::time::sleep(Duration::from_millis(50)).await,
            }
        }
        if let Some(pid) = pid {
            let mut dead = false;
            for _ in 0..40 {
                let alive = std::process::Command::new("kill")
                    .arg("-0")
                    .arg(&pid)
                    .stderr(std::process::Stdio::null())
                    .status()
                    .map(|s| s.success())
                    .unwrap_or(false);
                if !alive {
                    dead = true;
                    break;
                }
                tokio::time::sleep(Duration::from_millis(50)).await;
            }
            assert!(dead, "timed-out resume child (pid {pid}) was left running");
        }
    }

    /// The happy path still works through the tokio::process rewrite: a
    /// fast-exiting success script returns Ok.
    #[cfg(unix)]
    #[tokio::test]
    async fn run_resume_command_success_path() {
        use std::os::unix::fs::PermissionsExt;

        let tmp = TempDir::new().unwrap();
        let script = tmp.path().join("fake-claude-ok.sh");
        std::fs::write(&script, "#!/bin/sh\nexit 0\n").unwrap();
        std::fs::set_permissions(&script, std::fs::Permissions::from_mode(0o755)).unwrap();

        let result =
            run_resume_command(&script, "/tmp", "sess-ok", "continue", Duration::from_secs(10))
                .await;
        assert!(result.is_ok(), "got: {result:?}");
    }

    /// A failing exit code surfaces as Err with the status (not a timeout).
    #[cfg(unix)]
    #[tokio::test]
    async fn run_resume_command_nonzero_exit_is_error() {
        use std::os::unix::fs::PermissionsExt;

        let tmp = TempDir::new().unwrap();
        let script = tmp.path().join("fake-claude-fail.sh");
        std::fs::write(&script, "#!/bin/sh\necho boom >&2\nexit 3\n").unwrap();
        std::fs::set_permissions(&script, std::fs::Permissions::from_mode(0o755)).unwrap();

        let err =
            run_resume_command(&script, "/tmp", "sess-fail", "continue", Duration::from_secs(10))
                .await
                .expect_err("non-zero exit must be an error");
        assert!(err.contains("exited with"), "err: {err}");
        assert!(err.contains("boom"), "stderr must be captured: {err}");
    }
}
