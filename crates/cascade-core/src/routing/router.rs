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
//! | InteractiveChat | PrimaryT0 Claude (reserved, never delegated) |
//! | BulkExec | Pooled Claude → Openai → Opencode |
//! | Cheap | Free (Gfp) → local fallback |
//! | AdversarialReview | cross-family (Gfp + Google + Openai + Opencode, free-first) |
//! | FinalGate | PrimaryT0 Claude (reserved) |
//! | Sensitive | Claude-family or local ONLY (firewall via SensitivityPolicy) |
//!
//! Account selection is driven entirely by the live `AccountsRegistry` — no
//! account IDs are hardcoded in routing-decision logic.  An empty registry
//! (or one with no matching accounts) returns `AllExhausted` for delegatable
//! task classes, and `Reserved { provider_id: "claude" }` (sentinel) for
//! InteractiveChat / FinalGate.  It never panics.
//!
//! ## Inputs
//!
//! - `TaskClass` — which matrix row to consult.
//! - `payload` — the prompt string (used for sensitivity classification).
//! - `RouterConfig` — optional accounts-registry path, quota store path, timeout.
//!
//! ## Outputs
//!
//! - `RoutingDecision::Lane { provider_id, reason }` — the selected lane.
//! - `RoutingDecision::Reserved { provider_id, reason }` — PrimaryT0 Claude.
//! - `RoutingDecision::AllExhausted { tried }` — every lane checked and rejected.
//! - `RoutingDecision::FirewallDeny { reason }` — Sensitive routed to blocked provider.
//!
//! ## Constraints
//!
//! - No `unwrap()` outside `#[cfg(test)]`.
//! - No `sh -c`.
//! - No hardcoded account-id / family strings in routing-decision logic.
//! - File ≤ 300 lines (implementation; tests are excluded from line cap).
//!
//! ## TODO(fleet-01) deferred items
//!
//! - Live quota polling via QuotaCache ticker.
//! - QuotaCache integration for rate-window headroom checks.
//! - Daemon scheduler wiring.
//! - smithers PTY LiveCcDriver for CcAccLane.
//!
//! RoutingEvent bus emission — DONE (fleet-01): `RoutingEvent` struct defined;
//! `Router` holds an `Option<Arc<dyn Fn(RoutingEvent) + Send + Sync>>` observer
//! installed by the daemon's ring buffer. CLI and tests unaffected (default None).
//!
//! ## SPORT
//!
//! MASTER-CRATES.md — cascade-core: routing::router

use std::{path::PathBuf, sync::Arc, time::Duration};

use cascade_types::accounts::{Account, AccountFamily, AccountRole, AccountsRegistry};
use cascade_types::quota_store::QuotaStore;

use crate::{
    accounts_store::read_accounts_registry,
    quota_store::read_quota_store,
    routing::task_class::TaskClass,
    selection::{self, Gate, Tier},
    sensitivity::{classify_sensitivity, ContentSensitivity, SensitivityPolicy},
};

// ── RoutingEvent + observer seam ──────────────────────────────────────────────

/// A single routing decision emitted to the observer on every `Router::select` call.
///
/// ## Purpose
/// Allows the daemon (or any observer) to record / display the live stream of
/// routing decisions without creating a dependency from cascade-core on cascade-daemon.
/// cascade-core remains daemon-agnostic; the daemon installs a closure that pushes
/// events into its own ring buffer.
///
/// ## Fields
/// - `task_class`  — the `TaskClass` passed to `Router::select`.
/// - `account_id`  — the chosen `provider_id` (or `"AllExhausted"` / `"FirewallDeny"`).
/// - `model`       — reserved for future model-level granularity; currently empty.
/// - `reason`      — human-readable routing reason string.
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct RoutingEvent {
    /// Task class that triggered this decision (e.g. `"BulkExec"`).
    pub task_class: String,
    /// Account / provider selected, or `"AllExhausted"` / `"FirewallDeny"`.
    pub account_id: String,
    /// Model hint — currently empty; reserved for future model-level routing.
    pub model: String,
    /// Human-readable explanation of why this account was chosen.
    pub reason: String,
}

/// An observer closure installed on the `Router`.
///
/// When `Some`, called with a [`RoutingEvent`] on every `Router::select` call.
/// The daemon installs a closure that pushes into its in-memory ring buffer.
/// CLI paths and tests leave this `None` — zero overhead, no coupling.
pub type RoutingObserver = Arc<dyn Fn(RoutingEvent) + Send + Sync>;

// ── RoutingDecision ───────────────────────────────────────────────────────────

/// The outcome of a `Router::select` call.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum RoutingDecision {
    /// A delegate lane was selected. `provider_id` names the lane's provider.
    Lane {
        /// Provider ID drawn from `Account::id` in the registry, or `"local"`.
        provider_id: String,
        /// Human-readable description of why this lane was chosen.
        reason: String,
    },
    /// The PrimaryT0 account is reserved for this task class (no delegation).
    Reserved {
        /// The `Account::id` of the PrimaryT0 account, or `"claude"` sentinel
        /// when the registry is absent and the task class is InteractiveChat / FinalGate.
        provider_id: String,
        /// Why this task is handled by the reserved account.
        reason: String,
    },
    /// All accounts in the preference list were unavailable or quota-exhausted.
    AllExhausted {
        /// Names of all accounts/lanes that were checked and rejected.
        tried: Vec<String>,
    },
    /// Sensitive content attempted to route to a blocked external provider.
    /// (Should not occur after lane filtering — this is a safety guard.)
    FirewallDeny {
        /// The firewall rejection reason.
        reason: String,
    },
}

// ── RouterConfig ──────────────────────────────────────────────────────────────

/// Configuration for the router.
#[derive(Debug, Clone)]
pub struct RouterConfig {
    /// Optional path to `~/.cascade/accounts/accounts.json`.
    ///
    /// When `None`, the router treats the registry as empty: delegatable task
    /// classes return `AllExhausted`; InteractiveChat / FinalGate return the
    /// `"claude"` sentinel via `Reserved`.
    pub accounts_registry_path: Option<PathBuf>,

    /// Optional path to `~/.cascade/quota-store.json` for headroom checks.
    ///
    /// When `None`, quota checks are skipped (all accounts treated as available).
    pub quota_store_path: Option<PathBuf>,

    /// Timeout passed to lane `execute()` calls (not used in `select()` itself).
    pub lane_timeout: Duration,
}

impl Default for RouterConfig {
    fn default() -> Self {
        let home = std::env::var("HOME").unwrap_or_else(|_| "/tmp".into());
        let cascade = PathBuf::from(&home).join(".cascade");
        Self {
            accounts_registry_path: Some(cascade.join("accounts").join("accounts.json")),
            quota_store_path: Some(cascade.join("quota-store.json")),
            lane_timeout: Duration::from_secs(120),
        }
    }
}

// ── Router ────────────────────────────────────────────────────────────────────

/// Quota-aware routing matrix.
///
/// Call `Router::select(task_class, payload)` to obtain a `RoutingDecision`.
/// Account selection is driven entirely by the `AccountsRegistry`; no account
/// IDs or family strings are hardcoded in the routing-decision logic.
///
/// An optional [`RoutingObserver`] closure receives every decision as a
/// [`RoutingEvent`]. Install one via [`Router::with_observer`]. The default
/// is `None` — zero overhead for CLI and test paths.
pub struct Router {
    config: RouterConfig,
    sensitivity_policy: SensitivityPolicy,
    /// Optional observer — called synchronously after each `select()`.
    observer: Option<RoutingObserver>,
}

impl Router {
    /// Create a router with default config and no observer.
    pub fn new() -> Self {
        Self {
            config: RouterConfig::default(),
            sensitivity_policy: SensitivityPolicy::new(),
            observer: None,
        }
    }

    /// Create a router with custom config and no observer.
    pub fn with_config(config: RouterConfig) -> Self {
        Self {
            config,
            sensitivity_policy: SensitivityPolicy::new(),
            observer: None,
        }
    }

    /// Install a routing observer closure.
    ///
    /// The closure is called synchronously after every `select()` with the
    /// resulting [`RoutingEvent`]. Used by the daemon to push events into its
    /// ring buffer. CLI paths never call this — observer stays `None`.
    pub fn with_observer(mut self, observer: RoutingObserver) -> Self {
        self.observer = Some(observer);
        self
    }

    /// Select the best available account for the given task class and payload.
    ///
    /// Applies, in order:
    /// 1. Matrix rules (role/family-driven preference list per task class).
    /// 2. Sensitivity firewall (Sensitive → only Claude-family or local).
    /// 3. Quota headroom check (skips accounts at ≥100% pct_used).
    /// 4. CLI availability flag (`Account::cli_available`).
    ///
    /// After determining the decision, emits a [`RoutingEvent`] to the installed
    /// observer (if any). The observer call is synchronous and non-blocking.
    pub fn select(&self, task_class: TaskClass, payload: &str) -> RoutingDecision {
        let sensitivity = classify_sensitivity(payload);
        let registry = self.load_registry();
        let quota = self.load_quota();

        // E1-S6: the six TaskClass semantics resolve through the shared
        // selection module — TaskClass → Tier mapping + the shared spill/
        // gating engine (`selection::spill_select`). Sensitivity gating is
        // applied here BEFORE any account reaches the shared spill logic.
        let tier = selection::tier_for_task_class(task_class);

        let decision = match task_class {
            // ── InteractiveChat: PrimaryT0 Claude, never delegated ────────────
            TaskClass::InteractiveChat => RoutingDecision::Reserved {
                provider_id: primary_t0_id(&registry),
                reason: "InteractiveChat is always handled by the PrimaryT0 Claude account".into(),
            },

            // ── FinalGate: PrimaryT0 Claude (Opus tier) ───────────────────────
            TaskClass::FinalGate => RoutingDecision::Reserved {
                provider_id: primary_t0_id(&registry),
                reason: "FinalGate requires PrimaryT0 Claude for highest-quality correctness"
                    .into(),
            },

            // ── BulkExec: Pooled Claude → Openai → Opencode ──────────────────
            TaskClass::BulkExec => {
                let mut tried: Vec<String> = Vec::new();

                // Pooled Claude first (drain before PrimaryT0).
                if let Some(accs) = &registry {
                    let pooled = sorted_by_priority(accs.accounts.iter().filter(|a| {
                        a.family == AccountFamily::Claude && a.role == AccountRole::Pooled
                    }));
                    if let Some(acc) = selection::spill_select(
                        pooled.iter().copied(),
                        |a| self.gate_account(a, sensitivity, &quota),
                        |a| a.id.clone(),
                        &mut tried,
                    ) {
                        let d = lane_decision(acc, tier, "BulkExec → Pooled Claude (drain first)");
                        self.emit_event(task_class, &d);
                        return d;
                    }
                }

                // Non-sensitive only: Openai (Codex) → Opencode (OC-Go) → Zai (GLM).
                if sensitivity == ContentSensitivity::Public {
                    if let Some(accs) = &registry {
                        for family in &[
                            AccountFamily::Openai,
                            AccountFamily::Opencode,
                            AccountFamily::Zai,
                        ] {
                            let cands = sorted_by_priority(
                                accs.accounts.iter().filter(|a| &a.family == family),
                            );
                            if let Some(acc) = selection::spill_select(
                                cands.iter().copied(),
                                |a| self.gate_account(a, sensitivity, &quota),
                                |a| a.id.clone(),
                                &mut tried,
                            ) {
                                let d = lane_decision(acc, tier, "BulkExec → external overflow");
                                self.emit_event(task_class, &d);
                                return d;
                            }
                        }
                    }
                } else {
                    tried.push("openai-family (firewall: sensitive)".into());
                    tried.push("opencode-family (firewall: sensitive)".into());
                    tried.push("zai-family (firewall: sensitive)".into());
                }

                RoutingDecision::AllExhausted { tried }
            }

            // ── Cheap: Gfp (Free) → local ────────────────────────────────────
            TaskClass::Cheap => {
                let mut tried: Vec<String> = Vec::new();

                if sensitivity == ContentSensitivity::Public {
                    if let Some(accs) = &registry {
                        let cands = sorted_by_priority(
                            accs.accounts
                                .iter()
                                .filter(|a| a.family == AccountFamily::Gfp),
                        );
                        if let Some(acc) = selection::spill_select(
                            cands.iter().copied(),
                            |a| self.gate_account(a, sensitivity, &quota),
                            |a| a.id.clone(),
                            &mut tried,
                        ) {
                            let d = lane_decision(acc, tier, "Cheap → Gfp free Flash (max it)");
                            self.emit_event(task_class, &d);
                            return d;
                        }
                    }
                } else {
                    tried.push("gfp-family (firewall: sensitive)".into());
                }

                // Local LLM fallback — always trusted, always available.
                RoutingDecision::Lane {
                    provider_id: "local".into(),
                    reason: "Cheap → local LLM fallback".into(),
                }
            }

            // ── AdversarialReview: cross-family, free-first ───────────────────
            TaskClass::AdversarialReview => {
                if sensitivity == ContentSensitivity::Sensitive {
                    let d = RoutingDecision::FirewallDeny {
                        reason: "AdversarialReview with Sensitive content cannot use \
                                 cross-family external providers. Route to Claude or local."
                            .into(),
                    };
                    self.emit_event(task_class, &d);
                    return d;
                }

                let mut tried: Vec<String> = Vec::new();

                if let Some(accs) = &registry {
                    // Gfp → Google → Openai → Opencode → Zai (cost-ascending order).
                    for family in &[
                        AccountFamily::Gfp,
                        AccountFamily::Google,
                        AccountFamily::Openai,
                        AccountFamily::Opencode,
                        AccountFamily::Zai,
                    ] {
                        let cands = sorted_by_priority(
                            accs.accounts.iter().filter(|a| &a.family == family),
                        );
                        if let Some(acc) = selection::spill_select(
                            cands.iter().copied(),
                            |a| self.gate_account(a, sensitivity, &quota),
                            |a| a.id.clone(),
                            &mut tried,
                        ) {
                            let d = lane_decision(acc, tier, "AdversarialReview → cross-family");
                            self.emit_event(task_class, &d);
                            return d;
                        }
                    }
                }

                RoutingDecision::AllExhausted { tried }
            }

            // ── Sensitive: Claude-family or local ONLY ────────────────────────
            TaskClass::Sensitive => {
                let mut tried: Vec<String> = Vec::new();

                if let Some(accs) = &registry {
                    // Pooled Claude first (drain before PrimaryT0), then
                    // PrimaryT0 as last trusted option before local. Both
                    // groups are gated with forced Sensitive classification —
                    // the firewall applies BEFORE the shared spill logic.
                    let groups: [(AccountRole, &str); 2] = [
                        (
                            AccountRole::Pooled,
                            "Sensitive → Pooled Claude (trusted, drain first)",
                        ),
                        (
                            AccountRole::PrimaryT0,
                            "Sensitive → PrimaryT0 Claude (trusted)",
                        ),
                    ];
                    for (role, prefix) in groups {
                        let cands = sorted_by_priority(
                            accs.accounts
                                .iter()
                                .filter(|a| a.family == AccountFamily::Claude && a.role == role),
                        );
                        if let Some(acc) = selection::spill_select(
                            cands.iter().copied(),
                            |a| self.gate_account(a, ContentSensitivity::Sensitive, &quota),
                            |a| a.id.clone(),
                            &mut tried,
                        ) {
                            let d = lane_decision(acc, tier, prefix);
                            self.emit_event(task_class, &d);
                            return d;
                        }
                    }
                }

                // Local LLM — always trusted, always available.
                RoutingDecision::Lane {
                    provider_id: "local".into(),
                    reason: "Sensitive → local LLM (trusted, last resort)".into(),
                }
            }
        };

        self.emit_event(task_class, &decision);
        decision
    }

    /// Emit a [`RoutingEvent`] to the installed observer (if any).
    ///
    /// Purpose: decoupled seam so cascade-core never imports cascade-daemon.
    /// The observer closure (set by the daemon via `with_observer`) captures
    /// an `Arc<RwLock<VecDeque<RoutingEvent>>>` and pushes events there.
    /// When `observer` is `None` (CLI, tests), this is a no-op.
    fn emit_event(&self, task_class: TaskClass, decision: &RoutingDecision) {
        let Some(obs) = &self.observer else { return };
        let (account_id, reason) = match decision {
            RoutingDecision::Lane {
                provider_id,
                reason,
            } => (provider_id.clone(), reason.clone()),
            RoutingDecision::Reserved {
                provider_id,
                reason,
            } => (provider_id.clone(), reason.clone()),
            RoutingDecision::AllExhausted { tried } => (
                "AllExhausted".into(),
                format!("tried: {}", tried.join(", ")),
            ),
            RoutingDecision::FirewallDeny { reason } => ("FirewallDeny".into(), reason.clone()),
        };
        obs(RoutingEvent {
            task_class: task_class.to_string(),
            account_id,
            model: String::new(),
            reason,
        });
    }

    /// Gate a single account for the shared spill engine (E1-S6).
    ///
    /// Applies, in order: sensitivity firewall (BEFORE selection — see
    /// `selection::tier_for_task_class` docs), quota exhaustion (shared
    /// saturation predicate `selection::pcts_saturated`), and CLI
    /// availability. Skip reasons match the pre-unification `tried` strings.
    fn gate_account(
        &self,
        acc: &Account,
        sensitivity: ContentSensitivity,
        quota: &Option<QuotaStore>,
    ) -> Gate {
        if self
            .sensitivity_policy
            .check(sensitivity, &acc.id)
            .is_deny()
        {
            return Gate::Skip("firewall: sensitive".into());
        }
        if account_is_exhausted(quota, &acc.id) {
            return Gate::Skip("quota exhausted".into());
        }
        // GFP uses key-pool access; no CLI binary to check.
        if !acc.cli_available && acc.family != AccountFamily::Gfp {
            return Gate::Skip("cli unavailable".into());
        }
        Gate::Pass
    }

    /// Load the accounts registry from disk. Returns `None` on error or absent.
    fn load_registry(&self) -> Option<AccountsRegistry> {
        let path = self.config.accounts_registry_path.as_deref()?;
        read_accounts_registry(path).ok()
    }

    /// Load the quota store from disk. Returns `None` on error or absent.
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

// ── Internal helpers ──────────────────────────────────────────────────────────

/// Return the `Account::id` of the PrimaryT0 Claude account, or the `"claude"`
/// sentinel when the registry is absent or contains no PrimaryT0 entry.
fn primary_t0_id(registry: &Option<AccountsRegistry>) -> String {
    registry
        .as_ref()
        .and_then(|r| {
            r.accounts
                .iter()
                .find(|a| a.family == AccountFamily::Claude && a.role == AccountRole::PrimaryT0)
                .map(|a| a.id.clone())
        })
        .unwrap_or_else(|| "claude".into())
}

/// Return accounts sorted by `exhaustion_priority` ascending (lower = drain first).
fn sorted_by_priority<'a, I>(iter: I) -> Vec<&'a Account>
where
    I: Iterator<Item = &'a Account>,
{
    let mut v: Vec<&'a Account> = iter.collect();
    v.sort_by_key(|a| a.exhaustion_priority);
    v
}

/// Build a `Lane` decision for a gated-in account, tagging the tier resolved
/// via `selection::tier_for_task_class` so routing reasons show the shared
/// tier resolution.
fn lane_decision(acc: &Account, tier: Tier, reason_prefix: &str) -> RoutingDecision {
    RoutingDecision::Lane {
        provider_id: acc.id.clone(),
        reason: format!("{} [{:?}] — {}", reason_prefix, tier, acc.id),
    }
}

/// Returns `true` when the account is at or above 100% usage in the quota store.
///
/// When no quota store is available, or the account is not tracked, returns
/// `false` (conservative: assume headroom exists). The threshold check uses
/// the shared saturation predicate `selection::pcts_saturated` (E1-S6).
fn account_is_exhausted(quota: &Option<QuotaStore>, account_id: &str) -> bool {
    let Some(qs) = quota else {
        return false;
    };
    for entry in &qs.accounts {
        if entry.account_id == account_id || entry.provider == account_id {
            let pcts = entry
                .models
                .values()
                .filter_map(|m| m.pct_used.map(f64::from));
            if selection::pcts_saturated(pcts) {
                return true;
            }
        }
    }
    false
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::accounts::{
        AccessMethod, Account, AccountFamily, AccountRole, AccountsRegistry,
        ACCOUNTS_SCHEMA_VERSION,
    };
    use cascade_types::quota_store::{AccountEntry, ModelUsage, QuotaStore};
    use std::collections::HashMap;

    // ── Test registry builders ─────────────────────────────────────────────

    fn make_account(
        id: &str,
        family: AccountFamily,
        role: AccountRole,
        priority: u8,
        cli_available: bool,
    ) -> Account {
        Account {
            id: id.to_string(),
            family,
            subscription: "test".into(),
            access_methods: vec![AccessMethod::NativeCc],
            role,
            exhaustion_priority: priority,
            models: vec![],
            cli_available,
            key_count: 0,
            quota_account_id: None,
            notes: None,
        }
    }

    /// Small synthetic 7-account registry covering all routing families.
    fn small_registry() -> AccountsRegistry {
        AccountsRegistry {
            schema_version: ACCOUNTS_SCHEMA_VERSION,
            updated_at: "2026-06-27T00:00:00Z".into(),
            accounts: vec![
                make_account(
                    "claude-primary",
                    AccountFamily::Claude,
                    AccountRole::PrimaryT0,
                    255,
                    true,
                ),
                make_account(
                    "claude-pooled-1",
                    AccountFamily::Claude,
                    AccountRole::Pooled,
                    10,
                    true,
                ),
                make_account(
                    "claude-pooled-2",
                    AccountFamily::Claude,
                    AccountRole::Pooled,
                    20,
                    true,
                ),
                make_account(
                    "openai-codex",
                    AccountFamily::Openai,
                    AccountRole::Pooled,
                    50,
                    true,
                ),
                make_account("gfp-pool", AccountFamily::Gfp, AccountRole::Free, 1, true),
                make_account(
                    "google-agy",
                    AccountFamily::Google,
                    AccountRole::Pooled,
                    30,
                    true,
                ),
                make_account(
                    "oc-go",
                    AccountFamily::Opencode,
                    AccountRole::Pooled,
                    60,
                    true,
                ),
            ],
            model_matrix: vec![],
        }
    }

    fn router_with_registry(registry: AccountsRegistry) -> (Router, tempfile::TempDir) {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("accounts.json");
        std::fs::write(&path, serde_json::to_vec(&registry).unwrap()).unwrap();
        let r = Router::with_config(RouterConfig {
            accounts_registry_path: Some(path),
            quota_store_path: None,
            lane_timeout: Duration::from_secs(10),
        });
        (r, dir)
    }

    fn router_no_registry() -> Router {
        Router::with_config(RouterConfig {
            accounts_registry_path: None,
            quota_store_path: None,
            lane_timeout: Duration::from_secs(10),
        })
    }

    // ── Empty-registry behavior ────────────────────────────────────────────

    #[test]
    fn empty_registry_interactive_chat_still_reserved() {
        let r = router_no_registry();
        let d = r.select(TaskClass::InteractiveChat, "hello");
        assert!(
            matches!(d, RoutingDecision::Reserved { ref provider_id, .. } if provider_id == "claude"),
            "Empty registry: InteractiveChat must return Reserved/claude sentinel, got: {d:?}"
        );
    }

    #[test]
    fn empty_registry_final_gate_still_reserved() {
        let r = router_no_registry();
        let d = r.select(TaskClass::FinalGate, "review");
        assert!(
            matches!(d, RoutingDecision::Reserved { ref provider_id, .. } if provider_id == "claude"),
            "Empty registry: FinalGate must return Reserved/claude sentinel, got: {d:?}"
        );
    }

    #[test]
    fn empty_registry_bulk_exec_returns_all_exhausted() {
        let r = router_no_registry();
        let d = r.select(TaskClass::BulkExec, "draft a doc");
        assert!(
            matches!(d, RoutingDecision::AllExhausted { .. }),
            "Empty registry: BulkExec must return AllExhausted, got: {d:?}"
        );
    }

    #[test]
    fn empty_registry_adversarial_returns_all_exhausted_for_public() {
        let r = router_no_registry();
        let d = r.select(TaskClass::AdversarialReview, "review this code");
        assert!(
            matches!(d, RoutingDecision::AllExhausted { .. }),
            "Empty registry: AdversarialReview/public must return AllExhausted, got: {d:?}"
        );
    }

    #[test]
    fn empty_registry_cheap_falls_back_to_local() {
        let r = router_no_registry();
        let d = r.select(TaskClass::Cheap, "classify a tag");
        assert!(
            matches!(d, RoutingDecision::Lane { ref provider_id, .. } if provider_id == "local"),
            "Empty registry: Cheap must fall back to local, got: {d:?}"
        );
    }

    #[test]
    fn empty_registry_sensitive_falls_back_to_local() {
        let r = router_no_registry();
        let d = r.select(TaskClass::Sensitive, "my ssn is 123-45-6789");
        assert!(
            matches!(d, RoutingDecision::Lane { ref provider_id, .. } if provider_id == "local"),
            "Empty registry: Sensitive must fall back to local, got: {d:?}"
        );
    }

    // ── Role-based selection ───────────────────────────────────────────────

    #[test]
    fn interactive_chat_selects_primary_t0_by_role() {
        let (r, _dir) = router_with_registry(small_registry());
        let d = r.select(TaskClass::InteractiveChat, "hello");
        assert!(
            matches!(d, RoutingDecision::Reserved { ref provider_id, .. } if provider_id == "claude-primary"),
            "InteractiveChat must select PrimaryT0 by role, got: {d:?}"
        );
    }

    #[test]
    fn final_gate_selects_primary_t0_by_role() {
        let (r, _dir) = router_with_registry(small_registry());
        let d = r.select(TaskClass::FinalGate, "review");
        assert!(
            matches!(d, RoutingDecision::Reserved { ref provider_id, .. } if provider_id == "claude-primary"),
            "FinalGate must select PrimaryT0 by role, got: {d:?}"
        );
    }

    #[test]
    fn bulk_exec_drains_pooled_claude_first_by_priority() {
        let (r, _dir) = router_with_registry(small_registry());
        let d = r.select(TaskClass::BulkExec, "draft a long doc");
        // pooled-1 has priority 10 (lowest = drain first).
        assert!(
            matches!(d, RoutingDecision::Lane { ref provider_id, .. } if provider_id == "claude-pooled-1"),
            "BulkExec must drain lowest-priority-number Pooled Claude first, got: {d:?}"
        );
    }

    #[test]
    fn cheap_selects_gfp_family_for_public() {
        let (r, _dir) = router_with_registry(small_registry());
        let d = r.select(TaskClass::Cheap, "classify a tag");
        assert!(
            matches!(d, RoutingDecision::Lane { ref provider_id, .. } if provider_id == "gfp-pool"),
            "Cheap/public must select Gfp account, got: {d:?}"
        );
    }

    #[test]
    fn adversarial_review_selects_gfp_first_for_public() {
        let (r, _dir) = router_with_registry(small_registry());
        let d = r.select(TaskClass::AdversarialReview, "review this code for bugs");
        assert!(
            matches!(d, RoutingDecision::Lane { ref provider_id, .. } if provider_id == "gfp-pool"),
            "AdversarialReview/public must select Gfp first, got: {d:?}"
        );
    }

    // ── Sensitivity firewall ───────────────────────────────────────────────

    #[test]
    fn sensitive_task_class_selects_pooled_claude_not_external() {
        let (r, _dir) = router_with_registry(small_registry());
        let d = r.select(TaskClass::Sensitive, "my ssn is 123-45-6789");
        match &d {
            RoutingDecision::Lane { provider_id, .. } => {
                assert!(
                    provider_id.contains("claude") || provider_id == "local",
                    "Sensitive must only route to claude-family or local, got: {provider_id}"
                );
            }
            other => panic!("unexpected: {other:?}"),
        }
    }

    #[test]
    fn sensitive_payload_in_bulk_exec_skips_external_providers() {
        let (r, _dir) = router_with_registry(small_registry());
        let d = r.select(TaskClass::BulkExec, "my disability rating is 70%");
        match &d {
            RoutingDecision::Lane { provider_id, .. } => {
                assert!(
                    provider_id.contains("claude") || provider_id == "local",
                    "BulkExec/sensitive must not use external providers, got: {provider_id}"
                );
            }
            RoutingDecision::AllExhausted { .. } => { /* acceptable */ }
            other => panic!("unexpected: {other:?}"),
        }
    }

    #[test]
    fn adversarial_review_sensitive_payload_returns_firewall_deny() {
        let (r, _dir) = router_with_registry(small_registry());
        let d = r.select(
            TaskClass::AdversarialReview,
            "my custody arrangement details",
        );
        assert!(
            matches!(d, RoutingDecision::FirewallDeny { .. }),
            "AdversarialReview/sensitive must return FirewallDeny, got: {d:?}"
        );
    }

    // ── Quota exhaustion ───────────────────────────────────────────────────

    #[test]
    fn exhausted_account_is_skipped_in_favor_of_next_pooled() {
        let reg = small_registry();
        let dir = tempfile::tempdir().unwrap();
        let accounts_path = dir.path().join("accounts.json");
        std::fs::write(&accounts_path, serde_json::to_vec(&reg).unwrap()).unwrap();

        let mut models = HashMap::new();
        models.insert(
            "claude-sonnet".into(),
            ModelUsage {
                used: 100,
                limit: Some(100),
                reset_at: None,
                pct_used: Some(100.0),
                cost_usd: None,
            },
        );
        let qs = QuotaStore {
            schema_version: 2,
            updated_at: "2026-06-27T00:00:00Z".into(),
            accounts: vec![AccountEntry {
                account_id: "claude-pooled-1".into(),
                harness: "cc".into(),
                provider: "claude-max".into(),
                models,
                week_total_used: 100,
                month_total_used: 100,
                last_polled: "2026-06-27T00:00:00Z".into(),
                rate_windows: vec![],
            }],
            week_totals: HashMap::new(),
            month_totals: HashMap::new(),
            rolling_history: vec![],
        };
        let quota_path = dir.path().join("quota-store.json");
        std::fs::write(&quota_path, serde_json::to_vec(&qs).unwrap()).unwrap();

        let r = Router::with_config(RouterConfig {
            accounts_registry_path: Some(accounts_path),
            quota_store_path: Some(quota_path),
            lane_timeout: Duration::from_secs(10),
        });

        let d = r.select(TaskClass::BulkExec, "task");
        assert!(
            matches!(d, RoutingDecision::Lane { ref provider_id, .. } if provider_id == "claude-pooled-2"),
            "Exhausted pooled-1 must be skipped; should select pooled-2, got: {d:?}"
        );
    }

    #[test]
    fn no_quota_store_means_no_exhaustion() {
        let quota: Option<QuotaStore> = None;
        assert!(!account_is_exhausted(&quota, "any-account"));
    }

    // ── CLI unavailability ─────────────────────────────────────────────────

    #[test]
    fn cli_unavailable_account_is_skipped_in_favor_of_next() {
        let mut reg = small_registry();
        for acc in &mut reg.accounts {
            if acc.id == "claude-pooled-1" {
                acc.cli_available = false;
            }
        }
        let (r, _dir) = router_with_registry(reg);
        let d = r.select(TaskClass::BulkExec, "task");
        assert!(
            matches!(d, RoutingDecision::Lane { ref provider_id, .. } if provider_id == "claude-pooled-2"),
            "CLI-unavailable pooled-1 must be skipped; should select pooled-2, got: {d:?}"
        );
    }

    // ── RoutingObserver seam ───────────────────────────────────────────────

    #[test]
    fn none_observer_path_is_unaffected() {
        // Router with no observer must behave identically to the pre-fleet-01 path.
        let r = router_no_registry();
        let d = r.select(TaskClass::Cheap, "classify this");
        assert!(
            matches!(d, RoutingDecision::Lane { ref provider_id, .. } if provider_id == "local"),
            "No-observer Cheap must still fall back to local, got: {d:?}"
        );
    }

    #[test]
    fn observer_receives_event_on_select() {
        use std::sync::{Arc, Mutex};

        let captured: Arc<Mutex<Vec<RoutingEvent>>> = Arc::new(Mutex::new(Vec::new()));
        let captured_clone = Arc::clone(&captured);

        let observer: RoutingObserver = Arc::new(move |ev: RoutingEvent| {
            captured_clone.lock().unwrap().push(ev);
        });

        let (r, _dir) = router_with_registry(small_registry());
        let r = r.with_observer(observer);

        let _ = r.select(TaskClass::BulkExec, "draft a doc");

        let events = captured.lock().unwrap();
        assert_eq!(
            events.len(),
            1,
            "observer must receive exactly one event per select()"
        );
        assert_eq!(events[0].task_class, "BulkExec");
        assert!(
            !events[0].account_id.is_empty(),
            "account_id must be populated"
        );
    }

    #[test]
    fn observer_receives_event_for_reserved_class() {
        use std::sync::{Arc, Mutex};

        let captured: Arc<Mutex<Vec<RoutingEvent>>> = Arc::new(Mutex::new(Vec::new()));
        let captured_clone = Arc::clone(&captured);

        let observer: RoutingObserver = Arc::new(move |ev: RoutingEvent| {
            captured_clone.lock().unwrap().push(ev);
        });

        let (r, _dir) = router_with_registry(small_registry());
        let r = r.with_observer(observer);

        let _ = r.select(TaskClass::InteractiveChat, "hello");

        let events = captured.lock().unwrap();
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].task_class, "InteractiveChat");
        assert_eq!(events[0].account_id, "claude-primary");
    }

    #[test]
    fn observer_receives_firewall_deny_event() {
        use std::sync::{Arc, Mutex};

        let captured: Arc<Mutex<Vec<RoutingEvent>>> = Arc::new(Mutex::new(Vec::new()));
        let captured_clone = Arc::clone(&captured);

        let observer: RoutingObserver = Arc::new(move |ev: RoutingEvent| {
            captured_clone.lock().unwrap().push(ev);
        });

        let (r, _dir) = router_with_registry(small_registry());
        let r = r.with_observer(observer);

        // Sensitive payload + AdversarialReview → FirewallDeny.
        let _ = r.select(
            TaskClass::AdversarialReview,
            "my custody arrangement details",
        );

        let events = captured.lock().unwrap();
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].account_id, "FirewallDeny");
    }
}
