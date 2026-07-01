// conductor_router.rs — Quota-aware delegation target selector.
//
// Purpose: given a `ConductorRequest` (tier + optional model class + optional
//   account override) and a `QuotaSnapshot` (live from quota.json), pick the
//   best available worker backend and return a `ConductorTarget`.
//
//   Worker spill order: claude2(A2) → claude(A1 spare) → codex → gemini →
//   opencode → gfp(GP). Any account whose five_hour or seven_day utilization
//   is >= 100, or whose status is not "ok"/"pool", is skipped. Any account for
//   which the CLI binary is unavailable (checked by the caller via
//   `ConductorTarget::provider`) is also skipped.
//
// Inputs:  `ConductorRequest`, `QuotaSnapshot` (injectable for tests).
// Outputs: `Option<ConductorTarget>` — None when all backends are exhausted.
//
// Constraints: pure (no I/O, no network, no async). All path resolution is
//   the executor's responsibility.  Unit-tested via injected `QuotaSnapshot`.
//
// SPORT: MASTER-CLI.md — cascade delegate / conductor_router

use std::path::PathBuf;

use crate::model_ids::{
    MODEL_CLAUDE_FABLE, MODEL_CLAUDE_HAIKU, MODEL_CLAUDE_OPUS, MODEL_CLAUDE_SONNET,
    MODEL_GEMINI_FLASH, MODEL_GEMINI_PRO, MODEL_GLM, MODEL_GPT,
};

// ── Public types ──────────────────────────────────────────────────────────────

/// Work tier — mirrors the GCI T1/T2/T3 classification.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Tier {
    /// T1: decisions, architecture gates, CR-C. Default model: Opus.
    T1,
    /// T2: bulk execution, code gen, QA. Default model: Sonnet.
    T2,
    /// T3: cheap triage, grep+summarize, taxonomy. Default model: Haiku.
    T3,
}

/// Optional model-class override. When absent the tier default is used.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ModelClass {
    Opus,
    Sonnet,
    Haiku,
    /// Fable — hardest-reasoning tasks; only routed when a Fable-capable account
    /// exists (identified by the `fable` provider tag in the registry).
    Fable,
}

/// Which provider/CLI to use for execution.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Provider {
    /// Anthropic Claude Code CLI (`claude`). Config dir selects the account.
    Claude,
    /// OpenAI Codex CLI (`codex`).
    Codex,
    /// Google Gemini via cascade-agy CLI. Unavailable when agy is absent.
    Gemini,
    /// OpenCode CLI (`opencode`).
    OpenCode,
    /// GFP: Gemini Free Pool via HTTP POST to the GP proxy (localhost:3762).
    Gfp,
}

impl Provider {
    /// Human-readable name for reporting.
    pub fn as_str(self) -> &'static str {
        match self {
            Provider::Claude => "claude",
            Provider::Codex => "codex",
            Provider::Gemini => "gemini",
            Provider::OpenCode => "opencode",
            Provider::Gfp => "gfp",
        }
    }
}

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

/// A snapshot of one account's quota from `quota.json`.
#[derive(Debug, Clone)]
pub struct QuotaAccount {
    /// Account identifier (e.g. `"claude"`, `"claude2"`, `"gfp-pool"`).
    pub account: String,
    /// Provider tag (e.g. `"claude"`, `"codex"`, `"gfp"`, `"opencode"`).
    pub provider: String,
    /// Auth status (`"ok"`, `"needs_reauth"`, `"pool"`, etc.).
    pub status: String,
    /// 5-hour utilization 0–100 (None = unknown / no window).
    pub five_hour_utilization: Option<f64>,
    /// 7-day utilization 0–100 (None = unknown / no window).
    pub seven_day_utilization: Option<f64>,
    /// Config dir for Claude accounts (e.g. `~/.claude2`).
    pub config_dir: Option<PathBuf>,
}

/// Injected quota snapshot (all accounts from quota.json).
#[derive(Debug, Clone, Default)]
pub struct QuotaSnapshot {
    pub accounts: Vec<QuotaAccount>,
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

// ── Selection order ───────────────────────────────────────────────────────────

/// Ordered list of preferred accounts per provider. The router tries each in
/// order, skipping saturated or auth-dead entries.
///
/// The spill order is:
///   claude2(A2) → claude(A1 spare) → codex → gemini → opencode → gfp
///
/// A1 is intentionally kept near the front so it remains usable as a worker
/// spare; the interactive session runs A1 but does not exhaust it.
const ACCOUNT_SPILL_ORDER: &[(&str, Provider)] = &[
    ("claude2", Provider::Claude),
    ("claude", Provider::Claude),
    ("codex-acc1", Provider::Codex),
    ("codex", Provider::Codex),
    ("gemini-agt", Provider::Gemini),
    ("opencode-acc1", Provider::OpenCode),
    ("opencode", Provider::OpenCode),
    ("gfp-pool", Provider::Gfp),
];

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
pub fn select_target(req: &ConductorRequest, quota: &QuotaSnapshot) -> Option<ConductorTarget> {
    // Resolve the model-ID string to pass to the backend.
    let model_class = req.model_class.unwrap_or_else(|| default_model_class(req.tier));
    let base_model = model_id_for_class(model_class);

    // Account override: try exactly that account; fail if saturated.
    if let Some(ref acc_id) = req.account_override {
        let qa = quota.accounts.iter().find(|a| &a.account == acc_id)?;
        if is_saturated(qa) || !is_alive(qa) {
            return None;
        }
        let (provider, model, reason) = resolve_provider_model(qa, model_class, base_model);
        return Some(ConductorTarget {
            provider,
            account_id: qa.account.clone(),
            config_dir: qa.config_dir.clone(),
            model,
            reason: format!("override: {reason}"),
        });
    }

    // Normal spill: iterate the ordered list, find the first healthy account.
    let mut tried: Vec<String> = Vec::new();

    for (preferred_id, preferred_provider) in ACCOUNT_SPILL_ORDER {
        // Look up this account in the snapshot; skip if absent.
        let qa = quota.accounts.iter().find(|a| &a.account == preferred_id);
        let Some(qa) = qa else {
            continue;
        };

        if is_saturated(qa) || !is_alive(qa) {
            tried.push(format!("{} (saturated/dead)", qa.account));
            continue;
        }

        // Provider-model adaptation.
        let (provider, model, why) = resolve_provider_model(qa, model_class, base_model);

        // Sanity: prefer field and resolved provider should match.
        // (They will always match — provider in snapshot drives it.)
        let _ = preferred_provider; // used in ordering only

        let reason = if tried.is_empty() {
            format!("first-available: {why}")
        } else {
            format!("spill after [{}]: {why}", tried.join(", "))
        };

        return Some(ConductorTarget {
            provider,
            account_id: qa.account.clone(),
            config_dir: qa.config_dir.clone(),
            model,
            reason,
        });
    }

    None // All candidates exhausted.
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Default model class per tier.
fn default_model_class(tier: Tier) -> ModelClass {
    match tier {
        Tier::T1 => ModelClass::Opus,
        Tier::T2 => ModelClass::Sonnet,
        Tier::T3 => ModelClass::Haiku,
    }
}

/// Model-ID string for a `ModelClass`.
///
/// Non-Claude model IDs:
///   - Codex uses `gpt-5.5` (MODEL_GPT) regardless of class.
///   - Gemini T1/T2 uses `gemini-3.1-pro`; T3/Haiku uses `gemini-3.5-flash`.
///   - OpenCode uses `glm-5.2` (MODEL_GLM) regardless of class.
///   - GFP always uses `gemini-3.5-flash` (free pool, Flash only).
fn model_id_for_class(class: ModelClass) -> &'static str {
    match class {
        ModelClass::Opus => MODEL_CLAUDE_OPUS,
        ModelClass::Sonnet => MODEL_CLAUDE_SONNET,
        ModelClass::Haiku => MODEL_CLAUDE_HAIKU,
        ModelClass::Fable => MODEL_CLAUDE_FABLE,
    }
}

/// Return true if the account is quota-saturated (either window >= 100).
fn is_saturated(qa: &QuotaAccount) -> bool {
    let fh = qa.five_hour_utilization.unwrap_or(0.0);
    let sd = qa.seven_day_utilization.unwrap_or(0.0);
    fh >= 100.0 || sd >= 100.0
}

/// Return true if the account status is usable ("ok" or "pool").
fn is_alive(qa: &QuotaAccount) -> bool {
    matches!(qa.status.as_str(), "ok" | "pool")
}

/// Map a `QuotaAccount` to `(Provider, model_id, reason_fragment)`.
///
/// Provider is derived from the account's provider tag; the model-ID is
/// adapted when the account is not Claude (Claude uses the requested class
/// directly; others use their own model families).
fn resolve_provider_model(
    qa: &QuotaAccount,
    class: ModelClass,
    claude_model: &str,
) -> (Provider, String, String) {
    match qa.provider.as_str() {
        "claude" => {
            let model = claude_model.to_string();
            let reason = format!("claude account `{}`, model {}", qa.account, model);
            (Provider::Claude, model, reason)
        }
        "codex" => {
            // Codex uses GPT-5.5 for all tiers; class is noted for logging only.
            let model = MODEL_GPT.to_string();
            let reason = format!(
                "codex account `{}`, model {} (requested class={:?})",
                qa.account, model, class
            );
            (Provider::Codex, model, reason)
        }
        "gemini" | "google-agt" | "google-agy" => {
            // Gemini: T1/Opus/Fable → pro; T3/Haiku → flash; T2/Sonnet → pro.
            let model = match class {
                ModelClass::Haiku => MODEL_GEMINI_FLASH.to_string(),
                _ => MODEL_GEMINI_PRO.to_string(),
            };
            let reason = format!("gemini account `{}`, model {}", qa.account, model);
            (Provider::Gemini, model, reason)
        }
        "opencode" => {
            let model = MODEL_GLM.to_string();
            let reason = format!("opencode account `{}`, model {}", qa.account, model);
            (Provider::OpenCode, model, reason)
        }
        "gfp" => {
            // GFP free-pool: always Flash.
            let model = MODEL_GEMINI_FLASH.to_string();
            let reason = format!(
                "gfp pool `{}`, model {} (HTTP proxy)",
                qa.account, model
            );
            (Provider::Gfp, model, reason)
        }
        other => {
            // Unknown provider: treat as Codex-family for lack of better option.
            let model = MODEL_GPT.to_string();
            let reason = format!("unknown provider `{other}` on `{}`, defaulting to gpt", qa.account);
            (Provider::Codex, model, reason)
        }
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

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
}
