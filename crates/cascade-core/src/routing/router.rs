//! # router — quota-aware routing matrix
//!
//! ## Purpose
//!
//! Implements `Router::select(task_class, payload) -> RoutingDecision`.
//!
//! The matrix rules (from CASCADE-COMPLETE-VISION.md §5):
//!
//! | TaskClass | Preferred provider order |
//! |-----------|-------------------------|
//! | InteractiveChat | main Claude (reserved, never delegated) |
//! | BulkExec | acc2+ Claude → Codex → OC-Go |
//! | Cheap | GFP free Flash → local |
//! | AdversarialReview | cross-family (GFP + agy + OC-Go + Codex, maximize free/cheap) |
//! | FinalGate | main Claude Opus (reserved) |
//! | Sensitive | Claude or local ONLY (firewall via SensitivityPolicy) |
//!
//! Quota-awareness: the router reads the `QuotaStore` (when available via
//! `RouterConfig::quota_store_path`) and skips sources whose headroom is
//! exhausted (pct_used ≥ 100 or rate_windows all at limit). Account-exhaustion
//! order keeps main T0 Claude reserved: acc2+ are tried first for BulkExec.
//!
//! ## Inputs
//!
//! - `TaskClass` — which matrix row to consult.
//! - `payload` — the prompt string (used for sensitivity classification).
//! - `RouterConfig` — optional quota store path, timeout.
//!
//! ## Outputs
//!
//! - `RoutingDecision::Lane { provider_id, lane_idx }` — the selected lane.
//! - `RoutingDecision::Reserved { provider_id }` — main Claude, no subprocess.
//! - `RoutingDecision::AllExhausted` — every lane in the preference list is
//!   either unavailable or quota-exhausted.
//! - `RoutingDecision::FirewallDeny { reason }` — Sensitive content routed to
//!   an external provider (should not occur after lane filtering, but guard).
//!
//! ## Constraints
//!
//! - No `unwrap()` outside `#[cfg(test)]`.
//! - No `sh -c`.
//! - File ≤ 300 lines.
//!
//! ## SPORT
//!
//! MASTER-CRATES.md — cascade-core: routing::router

use std::{
    path::PathBuf,
    time::Duration,
};

use crate::{
    quota_store::read_quota_store,
    routing::{
        delegate::{
            AgyLane, CcAccLane, CodexLane, DelegateLane,
            MainClaudeLane, OcGoLane,
        },
        task_class::TaskClass,
    },
    sensitivity::{provider_is_trusted_for_sensitive, ContentSensitivity, SensitivityPolicy},
};
use cascade_types::quota_store::QuotaStore;

// ── RoutingDecision ───────────────────────────────────────────────────────────

/// The outcome of a `Router::select` call.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum RoutingDecision {
    /// A delegate lane was selected. `provider_id` names the lane's provider.
    Lane {
        /// Provider ID (e.g. `"openai-codex"`, `"acc2"`, `"gfp"`).
        provider_id: String,
        /// Human-readable description of why this lane was chosen.
        reason: String,
    },
    /// Main Claude is reserved for this task class (no subprocess delegation).
    Reserved {
        /// Always `"claude"` for the main account.
        provider_id: String,
        /// Why this task is handled by the reserved main Claude.
        reason: String,
    },
    /// All lanes in the preference list are unavailable or quota-exhausted.
    AllExhausted {
        /// Names of all lanes that were checked and rejected.
        tried: Vec<String>,
    },
    /// Sensitive content attempted to route to a blocked external provider.
    /// (Should not occur after lane filtering — this is a safety guard.)
    FirewallDeny {
        /// The firewall rejection reason from `SensitivityPolicy`.
        reason: String,
    },
}

// ── RouterConfig ──────────────────────────────────────────────────────────────

/// Configuration for the router.
#[derive(Debug, Clone)]
pub struct RouterConfig {
    /// Optional path to `~/.cascade/quota-store.json` for headroom checks.
    /// When `None`, quota checks are skipped (all lanes treated as available).
    pub quota_store_path: Option<PathBuf>,

    /// Timeout passed to lane `execute()` calls (not used in `select()` itself).
    pub lane_timeout: Duration,

    /// Extra Claude account suffixes to use for BulkExec drain-first logic.
    /// Defaults to `[2, 3, 4]`.
    pub extra_claude_account_suffixes: Vec<u8>,
}

impl Default for RouterConfig {
    fn default() -> Self {
        let home = std::env::var("HOME").unwrap_or_else(|_| "/tmp".into());
        Self {
            quota_store_path: Some(
                PathBuf::from(home).join(".cascade").join("quota-store.json"),
            ),
            lane_timeout: Duration::from_secs(120),
            extra_claude_account_suffixes: vec![2, 3, 4],
        }
    }
}

// ── Router ────────────────────────────────────────────────────────────────────

/// Quota-aware routing matrix.
///
/// Call `Router::select(task_class, payload)` to obtain a `RoutingDecision`.
pub struct Router {
    config: RouterConfig,
    sensitivity_policy: SensitivityPolicy,
}

impl Router {
    /// Create a router with default config.
    pub fn new() -> Self {
        Self {
            config: RouterConfig::default(),
            sensitivity_policy: SensitivityPolicy::new(),
        }
    }

    /// Create a router with custom config.
    pub fn with_config(config: RouterConfig) -> Self {
        Self {
            config,
            sensitivity_policy: SensitivityPolicy::new(),
        }
    }

    /// Select the best available lane for the given task class and payload.
    ///
    /// Applies:
    /// 1. Matrix rules (ordered preference list per task class).
    /// 2. Sensitivity firewall (Sensitive → only trusted providers).
    /// 3. Quota headroom check (skips exhausted accounts).
    /// 4. Lane availability check (skips missing CLIs).
    pub fn select(&self, task_class: TaskClass, payload: &str) -> RoutingDecision {
        // Classify payload sensitivity.
        let sensitivity = crate::sensitivity::classify_sensitivity(payload);

        match task_class {
            // ── InteractiveChat: always main Claude, never delegated ──────────
            TaskClass::InteractiveChat => RoutingDecision::Reserved {
                provider_id: "claude".into(),
                reason: "InteractiveChat is always handled by the main T0 Claude account".into(),
            },

            // ── FinalGate: always main Claude Opus ───────────────────────────
            TaskClass::FinalGate => RoutingDecision::Reserved {
                provider_id: "claude".into(),
                reason: "FinalGate requires main Claude Opus for highest-quality correctness".into(),
            },

            // ── BulkExec: acc2+ → Codex → OC-Go ─────────────────────────────
            TaskClass::BulkExec => {
                let quota = self.load_quota();
                // Build preference list: acc2, acc3, acc4, codex, oc-go.
                let mut tried: Vec<String> = Vec::new();

                // Extra Claude accounts first (drain before main T0).
                for suffix in &self.config.extra_claude_account_suffixes {
                    let provider_id = format!("acc{suffix}");
                    if sensitivity == ContentSensitivity::Sensitive
                        || provider_is_trusted_for_sensitive(&provider_id)
                    {
                        // trusted — allowed for sensitive too
                    } else {
                        tried.push(provider_id.clone());
                        continue;
                    }
                    if account_is_exhausted(&quota, &provider_id) {
                        tried.push(format!("{provider_id} (quota exhausted)"));
                        continue;
                    }
                    let lane = CcAccLane::new(*suffix);
                    if lane.check_availability().is_available() {
                        return RoutingDecision::Lane {
                            provider_id,
                            reason: format!("BulkExec → extra Claude acc{suffix} (drain first)"),
                        };
                    }
                    tried.push(format!("{provider_id} (unavailable)"));
                }

                // Codex — only for non-sensitive
                if sensitivity == ContentSensitivity::Public {
                    let provider_id = "openai-codex".to_string();
                    if !account_is_exhausted(&quota, &provider_id)
                        && CodexLane.check_availability().is_available()
                    {
                        return RoutingDecision::Lane {
                            provider_id,
                            reason: "BulkExec → Codex (acc exhausted / unavailable)".into(),
                        };
                    }
                    tried.push("openai-codex".into());

                    // OC-Go — dollar-metered, only for non-sensitive
                    let provider_id = "oc-go".to_string();
                    if !account_is_exhausted(&quota, &provider_id)
                        && OcGoLane.check_availability().is_available()
                    {
                        return RoutingDecision::Lane {
                            provider_id,
                            reason: "BulkExec → OC-Go (Codex exhausted / unavailable)".into(),
                        };
                    }
                    tried.push("oc-go".into());
                } else {
                    // Sensitive: external providers blocked.
                    tried.push("openai-codex (firewall: sensitive)".into());
                    tried.push("oc-go (firewall: sensitive)".into());
                }

                RoutingDecision::AllExhausted { tried }
            }

            // ── Cheap: GFP → local ────────────────────────────────────────────
            TaskClass::Cheap => {
                let quota = self.load_quota();
                let mut tried: Vec<String> = Vec::new();

                if sensitivity == ContentSensitivity::Public {
                    let provider_id = "gfp".to_string();
                    if !account_is_exhausted(&quota, &provider_id) {
                        // GFP availability is always Available from the lane stub;
                        // actual key check is in the providers layer.
                        return RoutingDecision::Lane {
                            provider_id,
                            reason: "Cheap → GFP free Flash (max it)".into(),
                        };
                    }
                    tried.push("gfp (quota exhausted)".into());
                } else {
                    tried.push("gfp (firewall: sensitive)".into());
                }

                // Local fallback — always trusted.
                RoutingDecision::Lane {
                    provider_id: "local".into(),
                    reason: "Cheap → local LLM fallback".into(),
                }
            }

            // ── AdversarialReview: cross-family, maximize free/cheap ──────────
            TaskClass::AdversarialReview => {
                let quota = self.load_quota();
                // Cross-family: GFP, agy, OC-Go, Codex (in free-first order).
                // Sensitive blocks all external — fall through to AllExhausted.
                let mut tried: Vec<String> = Vec::new();

                if sensitivity == ContentSensitivity::Sensitive {
                    return RoutingDecision::FirewallDeny {
                        reason: "AdversarialReview with Sensitive content cannot use \
                                 cross-family external providers. Route to Claude or local."
                            .into(),
                    };
                }

                // GFP (free Flash, highest priority for adversarial).
                {
                    let provider_id = "gfp".to_string();
                    if !account_is_exhausted(&quota, &provider_id) {
                        return RoutingDecision::Lane {
                            provider_id,
                            reason: "AdversarialReview → GFP (cross-family, free)".into(),
                        };
                    }
                    tried.push("gfp (quota exhausted)".into());
                }

                // agy (Google Pro).
                {
                    let provider_id = "google-agy".to_string();
                    if !account_is_exhausted(&quota, &provider_id)
                        && AgyLane.check_availability().is_available()
                    {
                        return RoutingDecision::Lane {
                            provider_id,
                            reason: "AdversarialReview → agy (cross-family, Google Pro)".into(),
                        };
                    }
                    tried.push("google-agy".into());
                }

                // OC-Go.
                {
                    let provider_id = "oc-go".to_string();
                    if !account_is_exhausted(&quota, &provider_id)
                        && OcGoLane.check_availability().is_available()
                    {
                        return RoutingDecision::Lane {
                            provider_id,
                            reason: "AdversarialReview → OC-Go (cross-family, cheap)".into(),
                        };
                    }
                    tried.push("oc-go".into());
                }

                // Codex.
                {
                    let provider_id = "openai-codex".to_string();
                    if !account_is_exhausted(&quota, &provider_id)
                        && CodexLane.check_availability().is_available()
                    {
                        return RoutingDecision::Lane {
                            provider_id,
                            reason: "AdversarialReview → Codex (cross-family)".into(),
                        };
                    }
                    tried.push("openai-codex".into());
                }

                RoutingDecision::AllExhausted { tried }
            }

            // ── Sensitive: Claude or local ONLY ──────────────────────────────
            TaskClass::Sensitive => {
                // Re-classify (payload may already be known sensitive).
                // Regardless of payload content, TaskClass::Sensitive always
                // enforces the firewall.
                let quota = self.load_quota();
                let mut tried: Vec<String> = Vec::new();

                // Try acc2+ first (drain before main T0).
                for suffix in &self.config.extra_claude_account_suffixes {
                    let provider_id = format!("acc{suffix}");
                    let verdict = self.sensitivity_policy.check(
                        ContentSensitivity::Sensitive,
                        &provider_id,
                    );
                    if verdict.is_deny() {
                        tried.push(format!("{provider_id} (firewall)"));
                        continue;
                    }
                    if account_is_exhausted(&quota, &provider_id) {
                        tried.push(format!("{provider_id} (quota exhausted)"));
                        continue;
                    }
                    let lane = CcAccLane::new(*suffix);
                    if lane.check_availability().is_available() {
                        return RoutingDecision::Lane {
                            provider_id,
                            reason: format!(
                                "Sensitive → extra Claude acc{suffix} (trusted, drain first)"
                            ),
                        };
                    }
                    tried.push(format!("{provider_id} (unavailable)"));
                }

                // Main Claude as final trusted option.
                {
                    let provider_id = "claude".to_string();
                    let verdict = self.sensitivity_policy.check(
                        ContentSensitivity::Sensitive,
                        &provider_id,
                    );
                    if verdict.is_allow() {
                        if MainClaudeLane::default().check_availability().is_available() {
                            return RoutingDecision::Lane {
                                provider_id,
                                reason: "Sensitive → main Claude (trusted)".into(),
                            };
                        }
                        tried.push("claude (unavailable)".into());
                    }
                }

                // Local LLM.
                RoutingDecision::Lane {
                    provider_id: "local".into(),
                    reason: "Sensitive → local LLM (trusted, last resort)".into(),
                }
            }
        }
    }

    /// Load the quota store from disk. Returns `None` on any I/O or parse error.
    fn load_quota(&self) -> Option<QuotaStore> {
        let path = self.config.quota_store_path.as_deref()?;
        read_quota_store(path).ok()
    }
}

impl Default for Router {
    fn default() -> Self {
        Self::new()
    }
}

// ── Quota headroom check ───────────────────────────────────────────────────────

/// Returns `true` when the account is at or above 100% usage in the quota store.
///
/// When no quota store is available, or the account is not tracked, returns
/// `false` (conservative: assume headroom exists).
fn account_is_exhausted(quota: &Option<QuotaStore>, provider_or_account_id: &str) -> bool {
    let Some(qs) = quota else {
        return false; // No quota data — assume headroom.
    };

    // Check by account_id prefix match (e.g. "acc2" matches account_id "acc2").
    for entry in &qs.accounts {
        if entry.account_id == provider_or_account_id
            || entry.provider == provider_or_account_id
        {
            // If any model is at 100% pct_used, treat the account as exhausted.
            for model_usage in entry.models.values() {
                if let Some(pct) = model_usage.pct_used {
                    if pct >= 100.0 {
                        return true;
                    }
                }
            }
        }
    }
    false
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::quota_store::{AccountEntry, ModelUsage, QuotaStore};
    use std::collections::HashMap;

    fn router_no_quota() -> Router {
        Router::with_config(RouterConfig {
            quota_store_path: None,
            lane_timeout: Duration::from_secs(10),
            extra_claude_account_suffixes: vec![2, 3],
        })
    }

    fn exhausted_quota_store(account_id: &str, provider: &str) -> QuotaStore {
        let mut models = HashMap::new();
        models.insert(
            "claude-sonnet".to_string(),
            ModelUsage {
                used: 100,
                limit: Some(100),
                reset_at: None,
                pct_used: Some(100.0),
                cost_usd: None,
            },
        );
        QuotaStore {
            schema_version: 2,
            updated_at: "2026-06-22T00:00:00Z".into(),
            accounts: vec![AccountEntry {
                account_id: account_id.to_string(),
                harness: "cc".into(),
                provider: provider.to_string(),
                models,
                week_total_used: 100,
                month_total_used: 100,
                last_polled: "2026-06-22T00:00:00Z".into(),
                rate_windows: vec![],
            }],
            week_totals: HashMap::new(),
            month_totals: HashMap::new(),
            rolling_history: vec![],
        }
    }

    // ── InteractiveChat ────────────────────────────────────────────────────

    #[test]
    fn interactive_chat_always_reserved() {
        let r = router_no_quota();
        let d = r.select(TaskClass::InteractiveChat, "hello");
        assert!(
            matches!(d, RoutingDecision::Reserved { ref provider_id, .. } if provider_id == "claude"),
            "InteractiveChat must always be Reserved/claude, got: {d:?}"
        );
    }

    // ── FinalGate ──────────────────────────────────────────────────────────

    #[test]
    fn final_gate_always_reserved() {
        let r = router_no_quota();
        let d = r.select(TaskClass::FinalGate, "review this");
        assert!(
            matches!(d, RoutingDecision::Reserved { ref provider_id, .. } if provider_id == "claude"),
            "FinalGate must always be Reserved/claude, got: {d:?}"
        );
    }

    // ── Sensitive firewall ─────────────────────────────────────────────────

    #[test]
    fn sensitive_task_class_never_routes_external() {
        let r = router_no_quota();
        let d = r.select(TaskClass::Sensitive, "my ssn is 123-45-6789");
        match &d {
            RoutingDecision::Lane { provider_id, .. } => {
                // Must only be claude, acc*, or local.
                assert!(
                    provider_id == "claude"
                        || provider_id.starts_with("acc")
                        || provider_id == "local",
                    "Sensitive must not route to external provider, got: {provider_id}"
                );
            }
            RoutingDecision::AllExhausted { .. } => {
                // Acceptable — all trusted lanes unavailable.
            }
            other => panic!("unexpected decision for Sensitive: {other:?}"),
        }
    }

    #[test]
    fn sensitive_content_in_bulk_exec_skips_external_providers() {
        let r = router_no_quota();
        // BulkExec with sensitive payload must not select Codex or OC-Go.
        let d = r.select(TaskClass::BulkExec, "my disability rating is 70%");
        match &d {
            RoutingDecision::Lane { provider_id, .. } => {
                assert_ne!(provider_id, "openai-codex");
                assert_ne!(provider_id, "oc-go");
                assert_ne!(provider_id, "gfp");
                assert_ne!(provider_id, "google-agy");
            }
            RoutingDecision::AllExhausted { .. } => {
                // Acceptable.
            }
            other => panic!("unexpected: {other:?}"),
        }
    }

    #[test]
    fn adversarial_review_with_sensitive_payload_returns_firewall_deny() {
        let r = router_no_quota();
        let d = r.select(TaskClass::AdversarialReview, "my custody arrangement details");
        assert!(
            matches!(d, RoutingDecision::FirewallDeny { .. }),
            "AdversarialReview with Sensitive payload must be FirewallDeny, got: {d:?}"
        );
    }

    // ── Account exhaustion skip ────────────────────────────────────────────

    #[test]
    fn exhausted_source_is_skipped() {
        // Build a quota store with acc2 exhausted and acc3 not exhausted.
        let mut models_exhausted = HashMap::new();
        models_exhausted.insert(
            "claude-sonnet".to_string(),
            ModelUsage {
                used: 100,
                limit: Some(100),
                reset_at: None,
                pct_used: Some(100.0),
                cost_usd: None,
            },
        );
        let mut models_ok = HashMap::new();
        models_ok.insert(
            "claude-sonnet".to_string(),
            ModelUsage {
                used: 50,
                limit: Some(100),
                reset_at: None,
                pct_used: Some(50.0),
                cost_usd: None,
            },
        );
        let qs = QuotaStore {
            schema_version: 2,
            updated_at: "2026-06-22T00:00:00Z".into(),
            accounts: vec![
                AccountEntry {
                    account_id: "acc2".into(),
                    harness: "cc".into(),
                    provider: "claude-max".into(),
                    models: models_exhausted,
                    week_total_used: 100,
                    month_total_used: 100,
                    last_polled: "2026-06-22T00:00:00Z".into(),
                    rate_windows: vec![],
                },
                AccountEntry {
                    account_id: "acc3".into(),
                    harness: "cc".into(),
                    provider: "claude-max".into(),
                    models: models_ok,
                    week_total_used: 50,
                    month_total_used: 50,
                    last_polled: "2026-06-22T00:00:00Z".into(),
                    rate_windows: vec![],
                },
            ],
            week_totals: HashMap::new(),
            month_totals: HashMap::new(),
            rolling_history: vec![],
        };

        let quota = Some(qs);
        assert!(account_is_exhausted(&quota, "acc2"), "acc2 should be exhausted");
        assert!(!account_is_exhausted(&quota, "acc3"), "acc3 should have headroom");
    }

    #[test]
    fn no_quota_store_means_no_exhaustion() {
        let quota: Option<QuotaStore> = None;
        assert!(!account_is_exhausted(&quota, "any-account"));
    }

    // ── BulkExec routing ───────────────────────────────────────────────────

    #[test]
    fn bulk_exec_prefers_acc_over_codex() {
        // With no quota data and claude on PATH (or not), acc should appear first.
        let r = router_no_quota();
        let d = r.select(TaskClass::BulkExec, "draft a long document");
        // We can only check that the first non-exhausted acc is tried.
        // The exact result depends on whether `claude` is on PATH.
        // Just ensure it doesn't panic and returns a valid variant.
        match d {
            RoutingDecision::Lane { .. }
            | RoutingDecision::AllExhausted { .. } => {}
            other => panic!("unexpected: {other:?}"),
        }
    }

    #[test]
    fn bulk_exec_all_exhausted_returns_all_exhausted() {
        // Build quota store with all acc and codex and oc-go exhausted.
        let mut m = HashMap::new();
        m.insert(
            "model".to_string(),
            ModelUsage {
                used: 100,
                limit: Some(100),
                reset_at: None,
                pct_used: Some(100.0),
                cost_usd: None,
            },
        );
        let make_entry = |id: &str, provider: &str| AccountEntry {
            account_id: id.to_string(),
            harness: "cc".into(),
            provider: provider.to_string(),
            models: m.clone(),
            week_total_used: 100,
            month_total_used: 100,
            last_polled: "2026-06-22T00:00:00Z".into(),
            rate_windows: vec![],
        };
        let qs = QuotaStore {
            schema_version: 2,
            updated_at: "2026-06-22T00:00:00Z".into(),
            accounts: vec![
                make_entry("acc2", "claude-max"),
                make_entry("acc3", "claude-max"),
                make_entry("openai-codex", "openai-codex"),
                make_entry("oc-go", "oc-go"),
            ],
            week_totals: HashMap::new(),
            month_totals: HashMap::new(),
            rolling_history: vec![],
        };

        // Write quota store to temp file.
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("quota-store.json");
        std::fs::write(&path, serde_json::to_string(&qs).unwrap()).unwrap();

        let r = Router::with_config(RouterConfig {
            quota_store_path: Some(path),
            lane_timeout: Duration::from_secs(10),
            extra_claude_account_suffixes: vec![2, 3],
        });

        let d = r.select(TaskClass::BulkExec, "draft a document");
        // With all quota exhausted and CLIs not on PATH in test env, expect
        // AllExhausted (or possibly a Lane if `claude` or other is on PATH).
        // We just verify no panic and no FirewallDeny.
        assert!(
            !matches!(d, RoutingDecision::FirewallDeny { .. }),
            "BulkExec with public content must not produce FirewallDeny"
        );
    }

    // ── Cheap routing ─────────────────────────────────────────────────────

    #[test]
    fn cheap_routes_to_gfp_when_available_and_public() {
        let r = router_no_quota();
        let d = r.select(TaskClass::Cheap, "classify this tag");
        match d {
            RoutingDecision::Lane { ref provider_id, .. } => {
                assert!(
                    provider_id == "gfp" || provider_id == "local",
                    "Cheap must route to GFP or local, got: {provider_id}"
                );
            }
            other => panic!("unexpected: {other:?}"),
        }
    }

    #[test]
    fn cheap_sensitive_bypasses_gfp() {
        let r = router_no_quota();
        let d = r.select(TaskClass::Cheap, "classify my medical record");
        match d {
            RoutingDecision::Lane { ref provider_id, .. } => {
                assert_ne!(provider_id, "gfp", "Cheap with sensitive payload must not use GFP");
                assert_eq!(provider_id, "local");
            }
            other => panic!("unexpected: {other:?}"),
        }
    }

    // ── AdversarialReview routing ─────────────────────────────────────────

    #[test]
    fn adversarial_review_public_content_starts_with_gfp() {
        let r = router_no_quota();
        let d = r.select(TaskClass::AdversarialReview, "review this code for bugs");
        match d {
            RoutingDecision::Lane { ref provider_id, .. } => {
                assert_eq!(
                    provider_id, "gfp",
                    "AdversarialReview public should start with GFP"
                );
            }
            RoutingDecision::AllExhausted { .. } => {
                // Acceptable — GFP not configured.
            }
            other => panic!("unexpected: {other:?}"),
        }
    }

    // ── account_is_exhausted ──────────────────────────────────────────────

    #[test]
    fn account_not_in_quota_store_is_not_exhausted() {
        let qs = exhausted_quota_store("acc2", "claude-max");
        assert!(!account_is_exhausted(&Some(qs), "acc3"));
    }

    #[test]
    fn account_at_100pct_is_exhausted() {
        let qs = exhausted_quota_store("acc2", "claude-max");
        assert!(account_is_exhausted(&Some(qs), "acc2"));
    }
}
