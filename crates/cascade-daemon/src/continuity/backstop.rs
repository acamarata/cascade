//! All-accounts-capped backstop — Phase C (session-failover) ticket C5.
//!
//! Purpose: pure predicate answering "are ALL of these Anthropic accounts
//! currently saturated?" — used by [`super::handoff::check_current_session`]
//! to decide whether the honest backstop (design spec §4: notify + rely on
//! `ContinuityMode::Resume`'s existing wait-for-reset behavior) applies, as
//! distinct from "target still saturated, keep waiting silently" (not yet
//! all capped — no notification warranted).
//!
//! This module adds NO new resume mechanism. Per the design spec, the
//! all-capped backstop is "strictly easier" than seamless failover because
//! it reuses `ContinuityMode::Resume`'s existing reset-wait behavior
//! unchanged — the only new code needed is the "are we in that state"
//! predicate here, plus the notify wiring in `handoff::notify_backstop`.
//!
//! Design spec: `.claude/planning/10-session-failover-spec.md` §4.
//! Master plan: `.claude/planning/11-vNEXT-master-plan.md` Phase C / C5.
//!
//! Inputs:  a parsed `quota.json` document + the account ids to check.
//! Outputs: `bool`. Pure — no I/O, no side effects, fully unit-testable.
//! Constraints: an unknown/missing account is treated as "cannot confirm
//!              capped" (never guess), so it makes the overall result
//!              `false`, matching `is_eligible_to_fire`'s posture elsewhere
//!              in this module tree. File ≤ 300 lines.
//! SPORT: `.claude/docs/MASTER-DAEMON.md` — continuity::backstop (Phase C / C5)

use super::{snapshot_for_account, QuotaSnapshot};

/// `true` when every account id in `account_ids` is currently saturated
/// (five-hour utilization >= 100). Empty `account_ids` returns `false` —
/// there is nothing to be "all capped" about.
pub fn all_accounts_capped(quota_doc: &serde_json::Value, account_ids: &[&str]) -> bool {
    if account_ids.is_empty() {
        return false;
    }
    account_ids.iter().all(|id| is_capped(quota_doc, id))
}

fn is_capped(quota_doc: &serde_json::Value, account_id: &str) -> bool {
    match snapshot_for_account(quota_doc, account_id) {
        Some(QuotaSnapshot {
            five_hour_utilization: Some(u),
            ..
        }) => u >= 100.0,
        _ => false,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn doc(entries: &[(&str, f64)]) -> serde_json::Value {
        json!({
            "accounts": entries.iter().map(|(id, util)| json!({
                "account": id,
                "usage": { "five_hour": { "utilization": util } }
            })).collect::<Vec<_>>()
        })
    }

    #[test]
    fn false_when_no_accounts_given() {
        let d = doc(&[("claude", 100.0)]);
        assert!(!all_accounts_capped(&d, &[]));
    }

    #[test]
    fn true_when_every_named_account_is_saturated() {
        let d = doc(&[("claude", 100.0), ("claude2", 100.0)]);
        assert!(all_accounts_capped(&d, &["claude", "claude2"]));
    }

    #[test]
    fn false_when_one_account_has_headroom() {
        let d = doc(&[("claude", 100.0), ("claude2", 40.0)]);
        assert!(!all_accounts_capped(&d, &["claude", "claude2"]));
    }

    #[test]
    fn false_when_an_account_is_missing_from_the_doc() {
        // Never guess: a missing account cannot be confirmed capped, so the
        // overall predicate must not silently claim "all capped".
        let d = doc(&[("claude", 100.0)]);
        assert!(!all_accounts_capped(&d, &["claude", "claude2"]));
    }

    #[test]
    fn false_when_utilization_exactly_below_100() {
        let d = doc(&[("claude", 99.999)]);
        assert!(!all_accounts_capped(&d, &["claude"]));
    }

    #[test]
    fn true_for_single_capped_account() {
        let d = doc(&[("claude", 100.0)]);
        assert!(all_accounts_capped(&d, &["claude"]));
    }
}
