// conductor_router.rs — Quota-aware delegation target selector (thin wrapper).
//
// Purpose: public API for the CLI conductor path. Since E1-S6 (router
//   unification) the spill order + saturation/auth gating logic lives in
//   `crate::selection` — the single shared selection module — and this module
//   is a thin wrapper that preserves the original public API exactly:
//   `select_target(&ConductorRequest, &QuotaSnapshot) -> Option<ConductorTarget>`.
//
//   Worker spill order is delegated to `crate::selection`: gfp(GP, T3/Haiku
//   only) → zai → opencode → gemini → codex → claude2(A2) → claude(A1) LAST.
//   Any account whose five_hour or seven_day utilization is >= 100, or whose
//   status is not "ok"/"pool", is skipped.
//
// Inputs:  `ConductorRequest`, `QuotaSnapshot` (injectable for tests).
// Outputs: `Option<ConductorTarget>` — None when all backends are exhausted.
//
// Constraints: pure (no I/O, no network, no async). All path resolution is
//   the executor's responsibility. The wrapper passes a default (no-signal)
//   `GpHealthSnapshot`. Callers that have a live GP health signal should use
//   `select_target_with_gp` so healthy T3 GP preference is reflected in the
//   reason string.
//
// SPORT: MASTER-CLI.md — cascade delegate / conductor_router

use crate::selection::{self, SelectionRequest};
use crate::sensitivity::{classify_sensitivity, provider_is_trusted_for_sensitive, ContentSensitivity};

// Canonical type definitions moved to `crate::selection` (E1-S6); re-exported
// here so existing consumers (`cascade-cli` conductor) compile unchanged.
pub use crate::selection::{
    GpHealthSnapshot, ModelClass, Provider, QuotaAccount, QuotaSnapshot, Tier,
};

use std::path::PathBuf;

// ── Public types ──────────────────────────────────────────────────────────────

/// A routing request — what kind of work and which account (if forced).
#[derive(Debug, Clone)]
pub struct ConductorRequest {
    /// Work tier (T1/T2/T3).
    pub tier: Tier,
    /// Optional model-class override. When None, the tier default is used.
    pub model_class: Option<ModelClass>,
    /// Force a specific account by account-id (e.g. `"claude2"`). When None
    /// the router applies the normal spill order.
    pub account_override: Option<String>,
}

/// The resolved dispatch target returned by `select_target`.
#[derive(Debug, Clone)]
pub struct ConductorTarget {
    /// Which provider/CLI to use.
    pub provider: Provider,
    /// Account identifier selected.
    pub account_id: String,
    /// Config dir for Claude accounts; None for non-Claude providers.
    pub config_dir: Option<PathBuf>,
    /// Exact model-ID string to pass to the backend.
    pub model: String,
    /// One-line human-readable explanation of why this target was chosen.
    pub reason: String,
}

// ── Public API ────────────────────────────────────────────────────────────────

/// Select the best available delegation target for `req` given live `quota`.
///
/// Inputs:  `req` — tier + optional model class + optional account override.
///          `quota` — injectable snapshot (tests pass a synthetic one; production
///          code reads from `quota.json` and constructs a `QuotaSnapshot`).
/// Outputs: `Some(ConductorTarget)` on success; `None` if all candidates are
///          saturated, auth-dead, or unavailable.
///
/// The provider field of the returned target tells the executor which CLI/
/// adapter to invoke.  Whether the CLI binary is actually present on disk is
/// the executor's responsibility — it should re-fall on `Unavailable` errors.
///
    /// Thin wrapper over [`selection::select_account`] with a default (no-signal)
    /// GP health snapshot. Callers with a LIVE GP health signal (the CLI
    /// conductor probes `:3761/health`) must use [`select_target_with_gp`] so
    /// the T3-GP preference can be reflected in the reason string.
pub fn select_target(req: &ConductorRequest, quota: &QuotaSnapshot) -> Option<ConductorTarget> {
    select_target_with_gp(req, quota, &GpHealthSnapshot::default())
}

/// [`select_target`] with an injected live GP pool-health snapshot (E1-S6).
///
/// Inputs:  `gp` — built from the GP proxy's live `/health` endpoint (see
///          `routing::gfp_http::probe_gp_health`) or from real routing-table
///          slots; pass `GpHealthSnapshot::default()` when no live signal
///          exists (disables the T3-GP preference — never guess health).
/// Outputs: identical to [`select_target`], except that a T3 request with a
///          healthy pool prefers `gfp` BEFORE the claude spill.
pub fn select_target_with_gp(
    req: &ConductorRequest,
    quota: &QuotaSnapshot,
    gp: &GpHealthSnapshot,
) -> Option<ConductorTarget> {
    let sel_req = SelectionRequest {
        tier: req.tier,
        model_class: req.model_class,
        account_override: req.account_override.clone(),
    };
    selection::select_account(&sel_req, quota, gp).map(|t| {
        ConductorTarget {
            provider: t.provider,
            account_id: t.account_id,
            config_dir: t.config_dir,
            model: t.model,
            reason: t.reason,
        }
    })
}

/// [`select_target_with_gp`] with a content-sensitivity firewall (parity with
/// `routing::router::Router::select` — see `sensitivity` module docs).
///
/// Inputs: `prompt` — the RAW prompt text about to be delegated. Conductor
///         dispatches coding/delegation tasks whose prompt body may itself
///         carry PII / VA / health / financial content (the caller's own
///         request, not just chat messages), so it must be classified before
///         any untrusted provider is allowed to receive it.
///
/// Behavior: classifies `prompt` via [`crate::sensitivity::classify_sensitivity`].
/// When `Public`, behaves identically to [`select_target_with_gp`]. When
/// `Sensitive`, every untrusted-provider account (per
/// [`crate::sensitivity::provider_is_trusted_for_sensitive`] — everything
/// except Claude/local) is excluded from the spill candidate pool BEFORE
/// selection, so the shared spill engine can only ever return a trusted
/// target. If no trusted candidate remains, returns `None` (fail closed —
/// never leak sensitive content to an untrusted lane).
///
/// This is the gated entry point conductor callers SHOULD use; the raw
/// [`select_target_with_gp`] / [`select_target`] remain available for
/// callers that have already classified `prompt` themselves (e.g. to avoid
/// re-classifying on a fallback retry loop) but MUST NOT skip the check.
pub fn select_target_for_prompt(
    req: &ConductorRequest,
    quota: &QuotaSnapshot,
    gp: &GpHealthSnapshot,
    prompt: &str,
) -> Option<ConductorTarget> {
    let sensitivity = classify_sensitivity(prompt);
    if sensitivity == ContentSensitivity::Public {
        return select_target_with_gp(req, quota, gp);
    }

    // Sensitive: only offer trusted-provider accounts to the spill engine.
    let trusted_accounts: Vec<QuotaAccount> = quota
        .accounts
        .iter()
        .filter(|a| provider_is_trusted_for_sensitive(&a.provider))
        .cloned()
        .collect();
    let trusted_snapshot = QuotaSnapshot { accounts: trusted_accounts };

    select_target_with_gp(req, &trusted_snapshot, gp)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model_ids::{
        MODEL_CLAUDE_HAIKU, MODEL_CLAUDE_OPUS, MODEL_CLAUDE_SONNET, MODEL_GEMINI_FLASH, MODEL_GPT,
    };

    // Helper: build a minimal healthy Claude account entry.
    fn claude_entry(account: &str, config_dir: &str, fh: f64, sd: f64) -> QuotaAccount {
        QuotaAccount {
            account: account.to_string(),
            provider: "claude".to_string(),
            status: "ok".to_string(),
            five_hour_utilization: Some(fh),
            seven_day_utilization: Some(sd),
            config_dir: Some(PathBuf::from(config_dir)),
        }
    }

    fn codex_entry(account: &str) -> QuotaAccount {
        QuotaAccount {
            account: account.to_string(),
            provider: "codex".to_string(),
            status: "ok".to_string(),
            five_hour_utilization: None,
            seven_day_utilization: None,
            config_dir: None,
        }
    }

    fn gfp_entry() -> QuotaAccount {
        QuotaAccount {
            account: "gfp-pool".to_string(),
            provider: "gfp".to_string(),
            status: "pool".to_string(),
            five_hour_utilization: None,
            seven_day_utilization: None,
            config_dir: None,
        }
    }

    // ── Happy path: A2 is first pick ─────────────────────────────────────────

    #[test]
    fn selects_claude2_first_when_healthy() {
        let snapshot = QuotaSnapshot {
            accounts: vec![
                claude_entry("claude2", "/home/user/.claude2", 10.0, 20.0),
                claude_entry("claude", "/home/user/.claude", 5.0, 10.0),
            ],
        };
        let req = ConductorRequest { tier: Tier::T2, model_class: None, account_override: None };
        let target = select_target(&req, &snapshot).expect("should pick claude2");
        assert_eq!(target.account_id, "claude2");
        assert_eq!(target.provider, Provider::Claude);
        assert_eq!(target.model, MODEL_CLAUDE_SONNET);
    }

    // ── A2 saturated → fall to A1 ────────────────────────────────────────────

    #[test]
    fn spills_to_claude_when_claude2_saturated_fh() {
        let snapshot = QuotaSnapshot {
            accounts: vec![
                claude_entry("claude2", "/home/user/.claude2", 100.0, 20.0),
                claude_entry("claude", "/home/user/.claude", 5.0, 10.0),
            ],
        };
        let req = ConductorRequest { tier: Tier::T2, model_class: None, account_override: None };
        let target = select_target(&req, &snapshot).expect("should pick claude");
        assert_eq!(target.account_id, "claude");
    }

    #[test]
    fn spills_to_claude_when_claude2_saturated_sd() {
        let snapshot = QuotaSnapshot {
            accounts: vec![
                claude_entry("claude2", "/home/user/.claude2", 10.0, 100.0),
                claude_entry("claude", "/home/user/.claude", 5.0, 10.0),
            ],
        };
        let req = ConductorRequest { tier: Tier::T2, model_class: None, account_override: None };
        let target = select_target(&req, &snapshot).expect("should pick claude");
        assert_eq!(target.account_id, "claude");
    }

    // ── Both Claude accounts saturated → fall to codex ───────────────────────

    #[test]
    fn spills_to_codex_when_both_claude_saturated() {
        let snapshot = QuotaSnapshot {
            accounts: vec![
                claude_entry("claude2", "/home/user/.claude2", 100.0, 50.0),
                claude_entry("claude", "/home/user/.claude", 100.0, 50.0),
                codex_entry("codex-acc1"),
            ],
        };
        let req = ConductorRequest { tier: Tier::T2, model_class: None, account_override: None };
        let target = select_target(&req, &snapshot).expect("should pick codex");
        assert_eq!(target.provider, Provider::Codex);
        assert_eq!(target.model, MODEL_GPT);
    }

    // ── All saturated → None ─────────────────────────────────────────────────

    #[test]
    fn returns_none_when_all_capped() {
        let mut c2 = claude_entry("claude2", "/home/user/.claude2", 100.0, 100.0);
        c2.status = "ok".to_string();
        let mut c1 = claude_entry("claude", "/home/user/.claude", 100.0, 100.0);
        c1.status = "ok".to_string();
        let snapshot = QuotaSnapshot { accounts: vec![c2, c1] };
        let req = ConductorRequest { tier: Tier::T2, model_class: None, account_override: None };
        assert!(select_target(&req, &snapshot).is_none());
    }

    // ── Account override ─────────────────────────────────────────────────────

    #[test]
    fn override_picks_exact_account() {
        let snapshot = QuotaSnapshot {
            accounts: vec![
                claude_entry("claude2", "/home/user/.claude2", 10.0, 20.0),
                claude_entry("claude", "/home/user/.claude", 5.0, 10.0),
            ],
        };
        let req = ConductorRequest {
            tier: Tier::T2,
            model_class: None,
            account_override: Some("claude".to_string()),
        };
        let target = select_target(&req, &snapshot).expect("should pick claude via override");
        assert_eq!(target.account_id, "claude");
        assert!(target.reason.starts_with("override:"));
    }

    #[test]
    fn override_returns_none_when_saturated() {
        let snapshot = QuotaSnapshot {
            accounts: vec![claude_entry("claude", "/home/user/.claude", 100.0, 50.0)],
        };
        let req = ConductorRequest {
            tier: Tier::T2,
            model_class: None,
            account_override: Some("claude".to_string()),
        };
        assert!(select_target(&req, &snapshot).is_none());
    }

    // ── Model class overrides ─────────────────────────────────────────────────

    #[test]
    fn t1_defaults_to_opus() {
        let snapshot = QuotaSnapshot {
            accounts: vec![claude_entry("claude2", "/home/user/.claude2", 10.0, 20.0)],
        };
        let req = ConductorRequest { tier: Tier::T1, model_class: None, account_override: None };
        let target = select_target(&req, &snapshot).expect("should pick");
        assert_eq!(target.model, MODEL_CLAUDE_OPUS);
    }

    #[test]
    fn t3_defaults_to_haiku() {
        let snapshot = QuotaSnapshot {
            accounts: vec![claude_entry("claude2", "/home/user/.claude2", 10.0, 20.0)],
        };
        let req = ConductorRequest { tier: Tier::T3, model_class: None, account_override: None };
        let target = select_target(&req, &snapshot).expect("should pick");
        assert_eq!(target.model, MODEL_CLAUDE_HAIKU);
    }

    #[test]
    fn explicit_sonnet_overrides_t1() {
        let snapshot = QuotaSnapshot {
            accounts: vec![claude_entry("claude2", "/home/user/.claude2", 10.0, 20.0)],
        };
        let req = ConductorRequest {
            tier: Tier::T1,
            model_class: Some(ModelClass::Sonnet),
            account_override: None,
        };
        let target = select_target(&req, &snapshot).expect("should pick");
        assert_eq!(target.model, MODEL_CLAUDE_SONNET);
    }

    // ── GFP pool ─────────────────────────────────────────────────────────────

    #[test]
    fn gfp_selected_when_only_option() {
        let snapshot = QuotaSnapshot { accounts: vec![gfp_entry()] };
        let req = ConductorRequest { tier: Tier::T3, model_class: None, account_override: None };
        let target = select_target(&req, &snapshot).expect("should pick gfp");
        assert_eq!(target.provider, Provider::Gfp);
        assert_eq!(target.model, MODEL_GEMINI_FLASH);
    }

    #[test]
    fn gfp_skipped_for_t2_even_when_only_option() {
        let snapshot = QuotaSnapshot { accounts: vec![gfp_entry()] };
        let req = ConductorRequest { tier: Tier::T2, model_class: None, account_override: None };
        assert!(select_target(&req, &snapshot).is_none());
    }

    // ── Auth-dead account skipped ─────────────────────────────────────────────

    #[test]
    fn skips_auth_dead_account() {
        let mut dead = claude_entry("claude2", "/home/user/.claude2", 10.0, 20.0);
        dead.status = "needs_reauth".to_string();
        let snapshot = QuotaSnapshot {
            accounts: vec![
                dead,
                claude_entry("claude", "/home/user/.claude", 5.0, 10.0),
            ],
        };
        let req = ConductorRequest { tier: Tier::T2, model_class: None, account_override: None };
        let target = select_target(&req, &snapshot).expect("should skip dead and pick claude");
        assert_eq!(target.account_id, "claude");
    }

    // ── Empty snapshot → None ─────────────────────────────────────────────────

    #[test]
    fn empty_snapshot_returns_none() {
        let snapshot = QuotaSnapshot { accounts: vec![] };
        let req = ConductorRequest { tier: Tier::T2, model_class: None, account_override: None };
        assert!(select_target(&req, &snapshot).is_none());
    }

    // ── Reason string present ─────────────────────────────────────────────────

    #[test]
    fn reason_string_is_non_empty() {
        let snapshot = QuotaSnapshot {
            accounts: vec![claude_entry("claude2", "/home/user/.claude2", 10.0, 20.0)],
        };
        let req = ConductorRequest { tier: Tier::T2, model_class: None, account_override: None };
        let target = select_target(&req, &snapshot).expect("should pick");
        assert!(!target.reason.is_empty(), "reason must be non-empty");
    }

    // ── Spill reason includes skipped accounts ────────────────────────────────

    #[test]
    fn spill_reason_mentions_skipped_accounts() {
        let snapshot = QuotaSnapshot {
            accounts: vec![
                claude_entry("claude2", "/home/user/.claude2", 100.0, 50.0),
                claude_entry("claude", "/home/user/.claude", 5.0, 10.0),
            ],
        };
        let req = ConductorRequest { tier: Tier::T2, model_class: None, account_override: None };
        let target = select_target(&req, &snapshot).expect("should pick claude");
        assert!(
            target.reason.contains("spill after"),
            "reason should mention spill: {}",
            target.reason
        );
    }

    // ── Live-GP entry point (E1-S6 review fix) ───────────────────────────────

    /// Regression: the T3-GP preference was dead code because no production
    /// caller supplied a live snapshot. `select_target_with_gp` + a healthy
    /// pool must prefer gfp over the claude spill for T3.
    #[test]
    fn select_target_with_gp_prefers_gfp_on_t3_when_pool_healthy() {
        let snapshot = QuotaSnapshot {
            accounts: vec![
                claude_entry("claude2", "/home/user/.claude2", 10.0, 20.0),
                claude_entry("claude", "/home/user/.claude", 5.0, 10.0),
                gfp_entry(),
            ],
        };
        let req = ConductorRequest { tier: Tier::T3, model_class: None, account_override: None };
        let gp = GpHealthSnapshot { healthy_slots: 5, ..Default::default() };
        let target = select_target_with_gp(&req, &snapshot, &gp).expect("target");
        assert_eq!(target.provider, Provider::Gfp);
        assert_eq!(target.account_id, "gfp-pool");
        assert!(target.reason.starts_with("t3-gp-preferred:"), "reason: {}", target.reason);
    }

    /// With no live signal (default snapshot), T3 may still use GFP because the
    /// fixed spill order allows the pool for Haiku-class work.
    #[test]
    fn select_target_with_gp_default_snapshot_allows_t3_gfp() {
        let snapshot = QuotaSnapshot {
            accounts: vec![
                claude_entry("claude2", "/home/user/.claude2", 10.0, 20.0),
                gfp_entry(),
            ],
        };
        let req = ConductorRequest { tier: Tier::T3, model_class: None, account_override: None };
        let target =
            select_target_with_gp(&req, &snapshot, &GpHealthSnapshot::default()).expect("target");
        assert_eq!(target.account_id, "gfp-pool");
        assert_eq!(target.model, MODEL_GEMINI_FLASH);
    }

    // ── Wrapper parity with selection::select_account (E1-S6) ─────────────────

    /// The wrapper must produce the exact same target as calling the shared
    /// selection module directly (default GP snapshot) — T1 and T2 cases.
    #[test]
    fn wrapper_parity_with_selection_module() {
        let snapshot = QuotaSnapshot {
            accounts: vec![
                claude_entry("claude2", "/home/user/.claude2", 100.0, 50.0),
                claude_entry("claude", "/home/user/.claude", 5.0, 10.0),
                codex_entry("codex-acc1"),
            ],
        };
        for tier in [Tier::T1, Tier::T2] {
            let req = ConductorRequest { tier, model_class: None, account_override: None };
            let wrapped = select_target(&req, &snapshot).expect("wrapper target");
            let direct = crate::selection::select_account(
                &crate::selection::SelectionRequest {
                    tier,
                    model_class: None,
                    account_override: None,
                },
                &snapshot,
                &crate::selection::GpHealthSnapshot::default(),
            )
            .expect("direct target");
            assert_eq!(wrapped.provider, direct.provider);
            assert_eq!(wrapped.account_id, direct.account_id);
            assert_eq!(wrapped.config_dir, direct.config_dir);
            assert_eq!(wrapped.model, direct.model);
            assert_eq!(wrapped.reason, direct.reason);
        }
    }

    // ── Sensitivity firewall on the conductor dispatch path ────────────────────
    //
    // Parity with `routing::router::Router::select`: a Sensitive prompt must
    // never be handed to an untrusted provider. These tests exercise
    // `select_target_for_prompt`, the gated entry point.

    fn gemini_entry(account: &str) -> QuotaAccount {
        QuotaAccount {
            account: account.to_string(),
            provider: "gemini".to_string(),
            status: "ok".to_string(),
            five_hour_utilization: None,
            seven_day_utilization: None,
            config_dir: None,
        }
    }

    /// Sensitive prompt + only untrusted lanes available (codex, gemini, gfp)
    /// → the untrusted providers must be excluded and the request refused
    /// (`None`), never silently routed to an untrusted lane.
    #[test]
    fn sensitive_prompt_excludes_untrusted_lanes_and_refuses_when_none_trusted_remain() {
        let snapshot = QuotaSnapshot {
            accounts: vec![
                codex_entry("codex-acc1"),
                gemini_entry("gemini-agt"),
                gfp_entry(),
            ],
        };
        let req = ConductorRequest { tier: Tier::T2, model_class: None, account_override: None };
        let target = select_target_for_prompt(
            &req,
            &snapshot,
            &GpHealthSnapshot::default(),
            "my ssn is 123-45-6789, please format this VA claim letter",
        );
        assert!(
            target.is_none(),
            "sensitive content must refuse rather than leak to an untrusted lane"
        );
    }

    /// Sensitive prompt + a healthy trusted (Claude) lane available →
    /// selection proceeds normally and picks the trusted account, skipping
    /// any untrusted accounts entirely (they must not even appear in `tried`
    /// diagnostics as viable candidates).
    #[test]
    fn sensitive_prompt_still_selects_trusted_claude_lane() {
        let snapshot = QuotaSnapshot {
            accounts: vec![
                codex_entry("codex-acc1"),
                claude_entry("claude", "/home/user/.claude", 5.0, 10.0),
            ],
        };
        let req = ConductorRequest { tier: Tier::T2, model_class: None, account_override: None };
        let target = select_target_for_prompt(
            &req,
            &snapshot,
            &GpHealthSnapshot::default(),
            "patient diagnosis: PTSD, medical record attached",
        )
        .expect("trusted claude lane should still be selected for sensitive content");
        assert_eq!(target.account_id, "claude");
        assert_eq!(target.provider, Provider::Claude);
    }

    /// Public (non-sensitive) prompt → normal selection applies unchanged,
    /// including external/untrusted providers when they are first in spill
    /// order.
    #[test]
    fn public_prompt_uses_normal_selection_including_untrusted_lanes() {
        let snapshot = QuotaSnapshot { accounts: vec![codex_entry("codex-acc1")] };
        let req = ConductorRequest { tier: Tier::T2, model_class: None, account_override: None };
        let target = select_target_for_prompt(
            &req,
            &snapshot,
            &GpHealthSnapshot::default(),
            "refactor the auth module to use JWT",
        )
        .expect("public content should route normally");
        assert_eq!(target.provider, Provider::Codex);
    }

    /// Trusted providers (Claude, local) are always allowed regardless of
    /// sensitivity — sanity check on `provider_is_trusted_for_sensitive`
    /// applied to the fleet vocabulary used by `QuotaAccount::provider`.
    #[test]
    fn trusted_claude_provider_always_allowed_for_sensitive_content() {
        let snapshot = QuotaSnapshot {
            accounts: vec![claude_entry("claude2", "/home/user/.claude2", 10.0, 20.0)],
        };
        let req = ConductorRequest { tier: Tier::T1, model_class: None, account_override: None };
        let target = select_target_for_prompt(
            &req,
            &snapshot,
            &GpHealthSnapshot::default(),
            "my social security number and bank account number are attached",
        )
        .expect("trusted claude account must be allowed for sensitive content");
        assert_eq!(target.account_id, "claude2");
        assert_eq!(target.provider, Provider::Claude);
    }

    /// Sensitive content must exclude gfp specifically (T3-GP preference must
    /// never override the firewall).
    #[test]
    fn sensitive_prompt_excludes_gfp_even_with_t3_gp_preference() {
        let snapshot = QuotaSnapshot {
            accounts: vec![
                gfp_entry(),
                claude_entry("claude2", "/home/user/.claude2", 10.0, 20.0),
            ],
        };
        let req = ConductorRequest { tier: Tier::T3, model_class: None, account_override: None };
        let gp = GpHealthSnapshot { healthy_slots: 5, ..Default::default() };
        let target = select_target_for_prompt(
            &req,
            &snapshot,
            &gp,
            "dob: 1980-04-15, full name: John Smith, home address: 123 Main Street",
        )
        .expect("should fall back to trusted claude, never gfp");
        assert_eq!(target.provider, Provider::Claude);
        assert_ne!(target.provider, Provider::Gfp);
    }
}
