//! Current-session handoff — Phase C (session-failover) ticket C3 (+ C5
//! backstop wiring).
//!
//! Purpose: extend the continuity watcher (`super::run_tick`) to ALSO watch
//! the account backing the CURRENT interactive T0 session — not just
//! intents added via `cascade continuity add` — and, once that account's
//! utilization crosses [`FAILOVER_UTILIZATION_THRESHOLD`] AND a designated
//! failover target account currently has headroom, checkpoint the session
//! (copy its JSONL via `crate::failover::session_copy`, ticket C2) and queue
//! a normal `Resume` intent against the target account. The existing
//! watcher loop then fires it on its next tick via the already-shipped
//! `fire_resume` path — no new `ContinuityMode` variant is needed (design
//! spec §1.4: a healthy target fires "immediately" simply because
//! `is_eligible_to_fire` never gates on wall-clock delay, only on
//! utilization).
//!
//! When the target is NOT healthy either, this falls through to the C5
//! backstop: if `super::backstop::all_accounts_capped` confirms every
//! watched account is saturated, fire one (idempotent) `Notify`-mode
//! notification via the existing `fire_notify` path and rely on
//! `ContinuityMode::Resume`'s pre-existing wait-for-reset behavior — no new
//! resume mechanism.
//!
//! Design spec: `.claude/planning/10-session-failover-spec.md` §1.4, §4.
//! Master plan: `.claude/planning/11-vNEXT-master-plan.md` Phase C / C3, C5.
//!
//! Opt-in only: gated on `CASCADE_SESSION_FAILOVER=1` (env), default OFF.
//! Until a genuinely separate second Anthropic account is logged into
//! `~/.claude2` (see master plan Phase C spike note — today both config dirs
//! resolve to the SAME account), this must never disrupt a live session —
//! the flag keeps the whole check inert by default.
//!
//! Inputs:  `~/.cascade/continuity/current-session.json` ([`CurrentSessionPointer`]),
//!          written externally by a hook (not built in this ticket — see
//!          master plan Phase C's open follow-up) once one exists, plus the
//!          same `quota.json` snapshot `run_tick` already reads.
//! Outputs: on trigger, a `session_copy` side effect + a new
//!          `ContinuityIntent` written via the existing `write_intent`, OR a
//!          best-effort OS notification via the existing `fire_notify`.
//! Constraints: one-shot per pointer — `handed_off_at`/`backstop_notified_at`
//!              guard re-fires exactly like `ContinuityIntent::fired_at`.
//!              File ≤ 300 lines.
//! SPORT: `.claude/docs/MASTER-DAEMON.md` — continuity::handoff (Phase C / C3, C5)

use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};
use tracing::{info, warn};

use super::{snapshot_for_account, write_intent, ContinuityIntent, ContinuityMode, QuotaSnapshot};
use crate::failover::session_copy;

/// Utilization percentage (0-100) at which the current session is
/// considered "approaching the cap" and a handoff is prepared.
pub const FAILOVER_UTILIZATION_THRESHOLD: f64 = 90.0;

/// Opt-in env var name. Default OFF — see module docs.
pub const SESSION_FAILOVER_ENV: &str = "CASCADE_SESSION_FAILOVER";

/// `true` when the operator has explicitly opted in to Phase C's live
/// current-session handoff this run.
pub fn session_failover_enabled() -> bool {
    std::env::var(SESSION_FAILOVER_ENV)
        .map(|v| v == "1" || v.eq_ignore_ascii_case("true"))
        .unwrap_or(false)
}

/// Pointer to the live interactive T0 session, written externally (e.g. a
/// `SessionStart`/`UserPromptSubmit` hook — not built in this ticket) so this
/// module can watch its account without a `ContinuityIntent` ever having
/// been added for it via the CLI.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct CurrentSessionPointer {
    /// The live `claude` session id (`claude --resume <id>`).
    pub session_id: String,
    /// Project slug — the directory name under `projects/` Claude Code
    /// derives from the escaped cwd path.
    pub project_slug: String,
    /// Account id currently in use (matches `quota.json`'s `"account"`).
    pub account_id: String,
    /// `CLAUDE_CONFIG_DIR` the live session is running under.
    pub config_dir: String,
    /// Failover target account id to watch/hand off to.
    pub target_account_id: String,
    /// `CLAUDE_CONFIG_DIR` for the target account.
    pub target_config_dir: String,
    /// Set once a handoff has been prepared (idempotency guard).
    #[serde(default)]
    pub handed_off_at: Option<i64>,
    /// Set once the C5 backstop notification has fired (idempotency guard —
    /// prevents renotifying every 60s tick while both accounts stay capped).
    #[serde(default)]
    pub backstop_notified_at: Option<i64>,
}

/// `~/.cascade/continuity/current-session.json` — lives alongside per-intent
/// files but is filtered out of `list_intents` by its fixed, non-`.json`-uuid
/// name never matching a real intent id in practice, and by callers only
/// ever reading it through [`read_pointer`], never `list_intents`.
pub fn pointer_path(continuity_dir: &Path) -> PathBuf {
    continuity_dir.join("current-session.json")
}

/// Read the current-session pointer, if any. `None` on missing/malformed
/// file — mirrors `read_intent`'s "never crash the watch loop" posture.
pub fn read_pointer(path: &Path) -> Option<CurrentSessionPointer> {
    let bytes = std::fs::read(path).ok()?;
    match serde_json::from_slice(&bytes) {
        Ok(p) => Some(p),
        Err(e) => {
            warn!(path = %path.display(), error = %e, "continuity::handoff: skipping malformed pointer file");
            None
        }
    }
}

/// Write the pointer atomically (tmp → rename), mirroring
/// `super::write_intent`'s pattern.
pub fn write_pointer(path: &Path, pointer: &CurrentSessionPointer) -> std::io::Result<()> {
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent)?;
    }
    let bytes = serde_json::to_vec_pretty(pointer)?;
    let tmp = path.with_extension("json.tmp");
    std::fs::write(&tmp, &bytes)?;
    std::fs::rename(&tmp, path)?;
    Ok(())
}

/// Outcome of one [`check_current_session`] pass — exposed for tests/logging.
#[derive(Debug, Clone, PartialEq)]
pub enum HandoffOutcome {
    /// `CASCADE_SESSION_FAILOVER` is not set — no-op by design.
    Disabled,
    /// No pointer file present — nothing to watch.
    NoPointer,
    /// The pointer already recorded a completed handoff — one-shot, skip.
    AlreadyHandledOff,
    /// Neither a handoff nor a backstop notification applies yet.
    TargetNotHealthyYet,
    /// C5: all watched accounts are capped — notified once.
    BackstopNotified,
    /// C3: session copied + `Resume` intent queued against the target.
    HandoffPrepared,
    /// Something failed (copy or intent write) — logged, one-shot, no retry.
    Error(String),
}

/// Pure decision: should a handoff fire right now, given the current
/// account's utilization and the target account's snapshot?
///
/// Mirrors `is_eligible_to_fire`'s "never guess" posture: an unknown target
/// snapshot is treated as not-yet-healthy, never as an implicit go-ahead.
pub fn should_handoff(current_util: Option<f64>, target: Option<QuotaSnapshot>) -> bool {
    let Some(cur) = current_util else { return false };
    if cur < FAILOVER_UTILIZATION_THRESHOLD {
        return false;
    }
    match target {
        Some(snap) => {
            let five_hour_ok = matches!(snap.five_hour_utilization, Some(u) if u < 100.0);
            let seven_day_ok = snap.seven_day_utilization.is_none_or(|sd| sd < 100.0);
            five_hour_ok && seven_day_ok
        }
        None => false,
    }
}

/// One watcher-tick pass over the current-session pointer (if any). Called
/// from `super::run_tick` alongside the existing per-intent loop.
pub async fn check_current_session(
    continuity_dir: &Path,
    quota_doc: Option<&serde_json::Value>,
) -> HandoffOutcome {
    if !session_failover_enabled() {
        return HandoffOutcome::Disabled;
    }
    let path = pointer_path(continuity_dir);
    let Some(mut pointer) = read_pointer(&path) else {
        return HandoffOutcome::NoPointer;
    };
    if pointer.handed_off_at.is_some() {
        return HandoffOutcome::AlreadyHandledOff;
    }
    let Some(doc) = quota_doc else {
        return HandoffOutcome::TargetNotHealthyYet;
    };
    let current_snap = snapshot_for_account(doc, &pointer.account_id);
    let target_snap = snapshot_for_account(doc, &pointer.target_account_id);

    if should_handoff(current_snap.and_then(|s| s.five_hour_utilization), target_snap) {
        return prepare_handoff(continuity_dir, &path, &mut pointer);
    }

    let current_capped = current_snap
        .and_then(|s| s.five_hour_utilization)
        .is_some_and(|u| u >= FAILOVER_UTILIZATION_THRESHOLD);
    if current_capped
        && pointer.backstop_notified_at.is_none()
        && super::backstop::all_accounts_capped(
            doc,
            &[pointer.account_id.as_str(), pointer.target_account_id.as_str()],
        )
    {
        return notify_backstop(&path, &mut pointer);
    }

    HandoffOutcome::TargetNotHealthyYet
}

/// C3: copy the session's JSONL to the target's config dir, then queue a
/// normal `Resume` intent against it. Marks `handed_off_at` on success so
/// this never fires twice for the same pointer.
fn prepare_handoff(
    continuity_dir: &Path,
    pointer_file: &Path,
    pointer: &mut CurrentSessionPointer,
) -> HandoffOutcome {
    let src_root = PathBuf::from(&pointer.config_dir).join("projects");
    let dst_root = PathBuf::from(&pointer.target_config_dir).join("projects");
    if let Err(e) = session_copy::copy_session(
        &src_root,
        &dst_root,
        &pointer.project_slug,
        &pointer.session_id,
    ) {
        warn!(error = %e, "continuity::handoff: session copy failed");
        return HandoffOutcome::Error(e);
    }

    let now = chrono::Utc::now().timestamp();
    let intent = ContinuityIntent {
        id: format!("failover-{}", pointer.session_id),
        session_id: pointer.session_id.clone(),
        config_dir: pointer.target_config_dir.clone(),
        prompt: "continue — resumed via Cascade session failover".to_owned(),
        account_id: pointer.target_account_id.clone(),
        mode: ContinuityMode::Resume,
        created_at: now,
        fired_at: None,
        note: Some(format!(
            "failover from {} (utilization threshold reached)",
            pointer.account_id
        )),
        last_error: None,
    };
    if let Err(e) = write_intent(continuity_dir, &intent) {
        let msg = format!("continuity::handoff: failed to write handoff intent: {e}");
        warn!("{msg}");
        return HandoffOutcome::Error(msg);
    }

    pointer.handed_off_at = Some(now);
    if let Err(e) = write_pointer(pointer_file, pointer) {
        warn!(error = %e, "continuity::handoff: failed to persist handed_off_at (handoff already queued — pointer may be re-checked next tick)");
    }
    info!(session_id = %pointer.session_id, target = %pointer.target_account_id, "continuity::handoff: prepared failover intent");
    HandoffOutcome::HandoffPrepared
}

/// C5: fire a best-effort notify-mode `fire_notify` (never blocks the tick
/// on failure) and mark `backstop_notified_at` so it fires at most once per
/// pointer while both accounts remain capped.
fn notify_backstop(pointer_file: &Path, pointer: &mut CurrentSessionPointer) -> HandoffOutcome {
    let synthetic = ContinuityIntent {
        id: format!("backstop-{}", pointer.session_id),
        session_id: pointer.session_id.clone(),
        config_dir: pointer.config_dir.clone(),
        prompt: "continue".to_owned(),
        account_id: pointer.account_id.clone(),
        mode: ContinuityMode::Notify,
        created_at: chrono::Utc::now().timestamp(),
        fired_at: None,
        note: Some("all Anthropic accounts capped — will auto-resume on reset".to_owned()),
        last_error: None,
    };
    // Best-effort: `super::fire_notify` is private to `continuity` but
    // visible here (this module is a descendant of `continuity`).
    let _ = super::fire_notify(&synthetic);

    pointer.backstop_notified_at = Some(chrono::Utc::now().timestamp());
    if let Err(e) = write_pointer(pointer_file, pointer) {
        warn!(error = %e, "continuity::handoff: failed to persist backstop_notified_at");
    }
    HandoffOutcome::BackstopNotified
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    fn make_pointer(id: &str) -> CurrentSessionPointer {
        CurrentSessionPointer {
            session_id: id.to_owned(),
            project_slug: "-Volumes-X9-Sites-acamarata-cascade".to_owned(),
            account_id: "claude".to_owned(),
            config_dir: "/tmp/does-not-matter-a1".to_owned(),
            target_account_id: "claude2".to_owned(),
            target_config_dir: "/tmp/does-not-matter-a2".to_owned(),
            handed_off_at: None,
            backstop_notified_at: None,
        }
    }

    fn env_lock() -> std::sync::MutexGuard<'static, ()> {
        static LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());
        LOCK.lock().unwrap_or_else(|e| e.into_inner())
    }

    // ── Flag-gating ───────────────────────────────────────────────────────

    #[tokio::test]
    async fn disabled_by_default_even_with_a_pointer_present() {
        let _guard = env_lock();
        std::env::remove_var(SESSION_FAILOVER_ENV);
        let tmp = TempDir::new().unwrap();
        write_pointer(&pointer_path(tmp.path()), &make_pointer("s1")).unwrap();

        let outcome = check_current_session(tmp.path(), None).await;
        assert_eq!(outcome, HandoffOutcome::Disabled);
    }

    #[tokio::test]
    async fn no_pointer_reported_when_enabled_but_no_file_written() {
        let _guard = env_lock();
        std::env::set_var(SESSION_FAILOVER_ENV, "1");
        let tmp = TempDir::new().unwrap();

        let outcome = check_current_session(tmp.path(), None).await;
        std::env::remove_var(SESSION_FAILOVER_ENV);
        assert_eq!(outcome, HandoffOutcome::NoPointer);
    }

    // ── should_handoff (pure decision) ───────────────────────────────────

    #[test]
    fn no_handoff_below_threshold() {
        let target = QuotaSnapshot {
            five_hour_utilization: Some(1.0),
            seven_day_utilization: None,
        };
        assert!(!should_handoff(Some(50.0), Some(target)));
    }

    #[test]
    fn handoff_when_current_saturated_and_target_healthy() {
        let target = QuotaSnapshot {
            five_hour_utilization: Some(10.0),
            seven_day_utilization: None,
        };
        assert!(should_handoff(Some(95.0), Some(target)));
    }

    #[test]
    fn no_handoff_when_target_also_saturated() {
        let target = QuotaSnapshot {
            five_hour_utilization: Some(100.0),
            seven_day_utilization: None,
        };
        assert!(!should_handoff(Some(95.0), Some(target)));
    }

    #[test]
    fn no_handoff_when_target_unknown() {
        assert!(!should_handoff(Some(95.0), None));
    }

    #[test]
    fn no_handoff_when_current_unknown() {
        let target = QuotaSnapshot {
            five_hour_utilization: Some(10.0),
            seven_day_utilization: None,
        };
        assert!(!should_handoff(None, Some(target)));
    }

    #[test]
    fn no_handoff_when_target_weekly_capped() {
        let target = QuotaSnapshot {
            five_hour_utilization: Some(10.0),
            seven_day_utilization: Some(100.0),
        };
        assert!(!should_handoff(Some(95.0), Some(target)));
    }

    // ── check_current_session end-to-end decision (mocked quota + tempdir) ─

    #[tokio::test]
    async fn prepares_handoff_and_marks_pointer_when_target_is_healthy() {
        let _guard = env_lock();
        std::env::set_var(SESSION_FAILOVER_ENV, "1");

        let continuity_root = TempDir::new().unwrap();
        let src_home = TempDir::new().unwrap();
        let dst_home = TempDir::new().unwrap();

        let mut pointer = make_pointer("sess-handoff");
        pointer.config_dir = src_home.path().to_string_lossy().into_owned();
        pointer.target_config_dir = dst_home.path().to_string_lossy().into_owned();
        // Write the source session file so the copy step has something real
        // to copy (this exercises C2 + C3 together end to end on tempdirs).
        let src_projects = src_home.path().join("projects").join(&pointer.project_slug);
        std::fs::create_dir_all(&src_projects).unwrap();
        std::fs::write(
            src_projects.join("sess-handoff.jsonl"),
            "{\"turn\":1}\n",
        )
        .unwrap();
        write_pointer(&pointer_path(continuity_root.path()), &pointer).unwrap();

        let quota_doc = serde_json::json!({
            "accounts": [
                { "account": "claude", "usage": { "five_hour": { "utilization": 96.0 } } },
                { "account": "claude2", "usage": { "five_hour": { "utilization": 5.0 } } }
            ]
        });

        let outcome = check_current_session(continuity_root.path(), Some(&quota_doc)).await;
        std::env::remove_var(SESSION_FAILOVER_ENV);
        assert_eq!(outcome, HandoffOutcome::HandoffPrepared);

        // Session must now exist under the target config dir.
        let copied = dst_home
            .path()
            .join("projects")
            .join(&pointer.project_slug)
            .join("sess-handoff.jsonl");
        assert!(copied.exists());

        // Pointer must record the handoff (idempotency).
        let reloaded = read_pointer(&pointer_path(continuity_root.path())).unwrap();
        assert!(reloaded.handed_off_at.is_some());

        // A Resume intent must now exist for the CLI/watcher to pick up.
        let intents = super::super::list_intents(continuity_root.path());
        assert!(intents
            .iter()
            .any(|i| i.id == "failover-sess-handoff" && i.account_id == "claude2"));
    }

    #[tokio::test]
    async fn is_a_noop_second_tick_after_handoff_already_recorded() {
        let _guard = env_lock();
        std::env::set_var(SESSION_FAILOVER_ENV, "1");
        let tmp = TempDir::new().unwrap();
        let mut pointer = make_pointer("sess-done");
        pointer.handed_off_at = Some(1_700_000_000);
        write_pointer(&pointer_path(tmp.path()), &pointer).unwrap();

        let outcome = check_current_session(tmp.path(), None).await;
        std::env::remove_var(SESSION_FAILOVER_ENV);
        assert_eq!(outcome, HandoffOutcome::AlreadyHandledOff);
    }

    #[tokio::test]
    async fn backstop_notifies_once_when_all_accounts_capped() {
        let _guard = env_lock();
        std::env::set_var(SESSION_FAILOVER_ENV, "1");
        let tmp = TempDir::new().unwrap();
        write_pointer(&pointer_path(tmp.path()), &make_pointer("sess-cap")).unwrap();

        let quota_doc = serde_json::json!({
            "accounts": [
                { "account": "claude", "usage": { "five_hour": { "utilization": 100.0 } } },
                { "account": "claude2", "usage": { "five_hour": { "utilization": 100.0 } } }
            ]
        });

        let outcome = check_current_session(tmp.path(), Some(&quota_doc)).await;
        assert_eq!(outcome, HandoffOutcome::BackstopNotified);

        // Second tick must not re-notify (idempotency guard).
        let outcome2 = check_current_session(tmp.path(), Some(&quota_doc)).await;
        std::env::remove_var(SESSION_FAILOVER_ENV);
        assert_eq!(outcome2, HandoffOutcome::TargetNotHealthyYet);
    }

    #[tokio::test]
    async fn neither_handoff_nor_backstop_when_current_has_headroom() {
        let _guard = env_lock();
        std::env::set_var(SESSION_FAILOVER_ENV, "1");
        let tmp = TempDir::new().unwrap();
        write_pointer(&pointer_path(tmp.path()), &make_pointer("sess-fine")).unwrap();

        let quota_doc = serde_json::json!({
            "accounts": [
                { "account": "claude", "usage": { "five_hour": { "utilization": 20.0 } } },
                { "account": "claude2", "usage": { "five_hour": { "utilization": 100.0 } } }
            ]
        });

        let outcome = check_current_session(tmp.path(), Some(&quota_doc)).await;
        std::env::remove_var(SESSION_FAILOVER_ENV);
        assert_eq!(outcome, HandoffOutcome::TargetNotHealthyYet);
    }
}
