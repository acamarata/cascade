// selection.rs — Unified account/provider selection module (E1-S6 router unification).
//
// Purpose: THE single shared selection brain consumed by every routing surface:
//   - `conductor_router::select_target` (CLI conductor) is a thin wrapper over
//     [`select_account`] — the proven quota/tier spill logic lives HERE.
//   - `routing::router::Router::select` (TaskClass matrix router) delegates its
//     account-choice step to [`spill_select`] + [`pcts_saturated`] and maps its
//     six `TaskClass` semantics onto tiers via [`tier_for_task_class`].
//   - The daemon chat path consumes [`preferred_chat_provider`] for its
//     GP-first-for-chat account choice.
//
//   Spill order (user decree 2026-07):
//   gfp(GP, T3/Haiku only) → zai → opencode → gemini → codex →
//   claude2(A2) → claude(A1) LAST.
//   Accounts with five_hour/seven_day utilization >= 100 or a status other
//   than "ok"/"pool" are skipped.
//
//   T3-GP preference: when the requested tier is T3 AND the GP pool is healthy
//   (per an injected [`GpHealthSnapshot`] built from real `RoutingTable` slot
//   state — at least one enabled gemini-free slot with `local_ok = true` that
//   is not in 429-cooldown), `gfp` is preferred BEFORE the claude spill.
//
// Inputs:  `SelectionRequest`, `QuotaSnapshot`, `GpHealthSnapshot` — all
//          injectable snapshots; no I/O, no network, no async.
// Outputs: `Option<SelectionTarget>` — None when all backends are exhausted.
//
// Constraints: pure. All path/file resolution is the caller's responsibility.
//   Unit-tested via injected snapshots only.
//
// SPORT: MASTER-CRATES.md — cascade-core: selection (E1-S6)

use std::path::PathBuf;
use std::time::Instant;

use crate::model_ids::{
    MODEL_CLAUDE_FABLE, MODEL_CLAUDE_HAIKU, MODEL_CLAUDE_OPUS, MODEL_CLAUDE_SONNET,
    MODEL_GEMINI_FLASH, MODEL_GEMINI_PRO, MODEL_GLM, MODEL_GPT,
};
use crate::routing::task_class::TaskClass;
use crate::routing_table::ProviderSlot;

// ── Public types (canonical definitions — re-exported by conductor_router) ────

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
    /// z.ai GLM Coding Plan through Claude Code CLI with endpoint env.
    Zai,
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
            Provider::Zai => "zai",
            Provider::Gfp => "gfp",
        }
    }
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

/// A selection request — what kind of work and which account (if forced).
#[derive(Debug, Clone)]
pub struct SelectionRequest {
    /// Work tier (T1/T2/T3).
    pub tier: Tier,
    /// Optional model-class override. When None, the tier default is used.
    pub model_class: Option<ModelClass>,
    /// Force a specific account by account-id (e.g. `"claude2"`). When None
    /// the normal spill order applies.
    pub account_override: Option<String>,
}

/// The resolved dispatch target returned by [`select_account`].
#[derive(Debug, Clone)]
pub struct SelectionTarget {
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

// ── GP pool health ─────────────────────────────────────────────────────────────

/// Point-in-time health snapshot of the Gemini free-pool (GP).
///
/// Built from REAL `RoutingTable` slot state via [`gp_health_from_slots`] —
/// never from guesses. `healthy_slots` counts enabled gemini-free slots
/// (`local_ok = true` by table construction) that are not in 429-cooldown.
///
/// `Default` (0 healthy slots) means "unknown / unhealthy" — callers that have
/// no live signal (e.g. the conductor wrapper, which must preserve its exact
/// pre-unification semantics) pass the default and get no GP preference.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct GpHealthSnapshot {
    /// Number of routable, non-cooldown GP slots.
    pub healthy_slots: usize,
    /// Total slots in the table (routable or not). Used to distinguish
    /// "exhausted" (slots exist, all in cooldown) from "no providers
    /// configured" (table is empty) — both report `healthy_slots == 0` but
    /// only the former is a circuit-breaker-worthy exhaustion event.
    pub total_slots: usize,
    /// Seconds until the soonest slot's 429-cooldown elapses, computed at
    /// snapshot time. `None` when the pool is healthy or the table is empty.
    pub earliest_reset_secs: Option<u64>,
}

impl GpHealthSnapshot {
    /// Pool healthy = at least one routable slot.
    pub fn is_healthy(&self) -> bool {
        self.healthy_slots > 0
    }

    /// Pool exhausted = one or more slots exist (providers ARE configured)
    /// but every one of them is currently in 429-cooldown. Distinct from an
    /// empty table (`total_slots == 0`), which means no GFP keys are
    /// configured at all — a config problem, not a transient exhaustion.
    pub fn is_exhausted(&self) -> bool {
        self.total_slots > 0 && self.healthy_slots == 0
    }
}

/// Build a [`GpHealthSnapshot`] from live `RoutingTable` slots at `Instant::now()`.
///
/// The `RoutingTable` constructor already filters to `enabled` + `harness ==
/// "gemini"` + `local_ok == true` entries, so every slot here is a gemini-free
/// slot; a slot is healthy when it is enabled OR its 429-cooldown has elapsed
/// (equivalent to what `tick_cooldowns` would restore on the next pick).
pub fn gp_health_from_slots(slots: &[ProviderSlot]) -> GpHealthSnapshot {
    gp_health_from_slots_at(slots, Instant::now())
}

/// Pure variant of [`gp_health_from_slots`] with an injectable clock — the
/// unit-testable core (no I/O, no ambient time reads beyond the caller's).
pub fn gp_health_from_slots_at(slots: &[ProviderSlot], now: Instant) -> GpHealthSnapshot {
    let healthy_slots = slots
        .iter()
        .filter(|s| s.enabled || s.re_enable_at.is_some_and(|t| t <= now))
        .count();
    let earliest_reset_secs = if healthy_slots == 0 {
        slots
            .iter()
            .filter_map(|s| s.re_enable_at)
            .map(|t| t.saturating_duration_since(now).as_secs())
            .min()
    } else {
        None
    };
    GpHealthSnapshot {
        healthy_slots,
        total_slots: slots.len(),
        earliest_reset_secs,
    }
}

// ── TaskClass → Tier mapping ───────────────────────────────────────────────────

/// Map the matrix router's six `TaskClass` semantics onto conductor tiers so
/// both routers resolve capacity through the same spill logic.
///
/// | TaskClass | Tier |
/// |---|---|
/// | InteractiveChat, FinalGate | T1 |
/// | BulkExec, AdversarialReview, Sensitive | T2 |
/// | Cheap | T3 |
///
/// `Sensitive` maps to T2 for capacity purposes ONLY — the sensitivity
/// firewall (`SensitivityPolicy`) is applied by `Router::select` BEFORE any
/// account reaches the shared spill logic, and is unaffected by this mapping.
pub fn tier_for_task_class(class: TaskClass) -> Tier {
    match class {
        TaskClass::InteractiveChat | TaskClass::FinalGate => Tier::T1,
        TaskClass::BulkExec | TaskClass::AdversarialReview | TaskClass::Sensitive => Tier::T2,
        TaskClass::Cheap => Tier::T3,
    }
}

// ── Shared saturation gate ─────────────────────────────────────────────────────

/// Return true when any utilization percentage is at or above 100.
///
/// The single saturation predicate shared by the conductor path (5-hour /
/// 7-day window utilization) and the matrix router path (per-model
/// `pct_used` from the quota store).
pub fn pcts_saturated<I: IntoIterator<Item = f64>>(pcts: I) -> bool {
    pcts.into_iter().any(|p| p >= 100.0)
}

// ── Shared spill engine ────────────────────────────────────────────────────────

/// Outcome of gating a single spill candidate.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Gate {
    /// Candidate is usable — selection stops here.
    Pass,
    /// Candidate is skipped for the given reason (recorded in `tried`).
    Skip(String),
}

/// Walk an ordered candidate list and return the first candidate that passes
/// `gate`. Every skipped candidate is appended to `tried` as
/// `"{name} ({reason})"` — the shared audit-trail format used by both routers.
pub fn spill_select<'a, T: ?Sized>(
    candidates: impl IntoIterator<Item = &'a T>,
    mut gate: impl FnMut(&T) -> Gate,
    mut name: impl FnMut(&T) -> String,
    tried: &mut Vec<String>,
) -> Option<&'a T> {
    for candidate in candidates {
        match gate(candidate) {
            Gate::Pass => return Some(candidate),
            Gate::Skip(reason) => tried.push(format!("{} ({})", name(candidate), reason)),
        }
    }
    None
}

// ── Spill order ────────────────────────────────────────────────────────────────

/// Ordered list of preferred accounts per provider.
///
/// User preference 2026-07: burn separate-quota fleet first, with Zai/OpenCode
/// before Codex, A2 second-to-last, and A1 last. GFP appears first in the list
/// but is gated to Haiku/T3-class work only.
const ACCOUNT_SPILL_ORDER: &[(&str, Provider)] = &[
    ("gfp-pool", Provider::Gfp),
    ("glm-acc1", Provider::Zai),
    ("opencode-acc1", Provider::OpenCode),
    ("opencode", Provider::OpenCode),
    ("gemini-acc1", Provider::Gemini),
    ("gemini-agt", Provider::Gemini),
    ("codex-acc1", Provider::Codex),
    ("codex", Provider::Codex),
    ("claude2", Provider::Claude),
    ("claude", Provider::Claude),
];

// ── Core selection API ─────────────────────────────────────────────────────────

/// Select the best available account for `req` given live `quota` and GP health.
///
/// Inputs:  `req` — tier + optional model class + optional account override.
///          `quota` — injectable snapshot (tests pass a synthetic one; production
///          reads `quota.json` and constructs a `QuotaSnapshot`).
///          `gp` — GP pool health snapshot; pass `GpHealthSnapshot::default()`
///          when no live signal is available (disables the T3-GP preference).
/// Outputs: `Some(SelectionTarget)` on success; `None` if all candidates are
///          saturated, auth-dead, or unavailable.
///
/// T3-GP preference: when `req.tier == T3` and `gp.is_healthy()`, the `gfp`
/// pool is evaluated BEFORE the claude spill. In every other case the fixed
/// spill order applies unchanged.
pub fn select_account(
    req: &SelectionRequest,
    quota: &QuotaSnapshot,
    gp: &GpHealthSnapshot,
) -> Option<SelectionTarget> {
    // Resolve the model-ID string to pass to the backend.
    let model_class = req
        .model_class
        .unwrap_or_else(|| default_model_class(req.tier));
    let base_model = model_id_for_class(model_class);

    // Account override: try exactly that account; fail if saturated.
    if let Some(ref acc_id) = req.account_override {
        let qa = quota.accounts.iter().find(|a| &a.account == acc_id)?;
        if is_saturated(qa) || !is_alive(qa) {
            return None;
        }
        let (provider, model, reason) = resolve_provider_model(qa, model_class, base_model);
        return Some(SelectionTarget {
            provider,
            account_id: qa.account.clone(),
            config_dir: qa.config_dir.clone(),
            model,
            reason: format!("override: {reason}"),
        });
    }

    // Candidate order: the fixed spill order, with gfp promoted to the front
    // when the tier is T3 and the GP pool is healthy (free Flash preferred).
    let gp_first = req.tier == Tier::T3 && gp.is_healthy();
    let order: Vec<&(&str, Provider)> = if gp_first {
        ACCOUNT_SPILL_ORDER
            .iter()
            .filter(|(_, p)| *p == Provider::Gfp)
            .chain(
                ACCOUNT_SPILL_ORDER
                    .iter()
                    .filter(|(_, p)| *p != Provider::Gfp),
            )
            .collect()
    } else {
        ACCOUNT_SPILL_ORDER.iter().collect()
    };

    // Resolve order entries against the snapshot (absent accounts are skipped
    // silently — identical to the pre-unification conductor semantics).
    let resolved: Vec<&QuotaAccount> = order
        .iter()
        .filter_map(|(id, _)| quota.accounts.iter().find(|a| a.account == *id))
        .collect();

    let mut tried: Vec<String> = Vec::new();
    let qa = spill_select(
        resolved.iter().copied(),
        |qa| {
            if qa.provider == "gfp" && model_class != ModelClass::Haiku {
                // T1/T2 requests: Flash is inadequate for code-gen and hard reasoning.
                Gate::Skip("not-haiku-class".into())
            } else if qa.provider == "gfp" && gp.is_exhausted() {
                // T3 + exhausted GP pool (keys configured but all in cooldown): skip gfp.
                // Default snapshot (total_slots==0) means "no probe done" — GFP is usable
                // in the spill order but not preferred. is_healthy() would also block
                // no-probe callers, which is wrong; is_exhausted() only fires when we
                // KNOW total_slots>0 yet healthy_slots==0 (all keys rate-limited).
                Gate::Skip("gp-pool-unhealthy".into())
            } else if is_saturated(qa) || !is_alive(qa) {
                Gate::Skip("saturated/dead".into())
            } else {
                Gate::Pass
            }
        },
        |qa| qa.account.clone(),
        &mut tried,
    )?;

    let (provider, model, why) = resolve_provider_model(qa, model_class, base_model);
    let reason = if !tried.is_empty() {
        format!("spill after [{}]: {why}", tried.join(", "))
    } else if gp_first && provider == Provider::Gfp {
        format!("t3-gp-preferred: {why}")
    } else {
        format!("first-available: {why}")
    };

    Some(SelectionTarget {
        provider,
        account_id: qa.account.clone(),
        config_dir: qa.config_dir.clone(),
        model,
        reason,
    })
}

// ── Chat preference (daemon chat path) ────────────────────────────────────────

/// Provider-registry id of the GP-pool-backed adapter in the daemon registry.
///
/// RESERVED id: the daemon registers a real pool-proxy `GeminiAdapter`
/// (`GeminiConfig::proxy()` → :3761 rotating pool) under this exact id at boot
/// when at least one routable gemini-free slot exists (see `main.rs`). It is
/// deliberately NOT `"gemini"` — that bare slug can collide with a user-added
/// DIRECT single-key Gemini adapter registered via IPC, which would pin every
/// provider-less chat to one key with no pool rotation or 429 fallback.
pub const GP_CHAT_PROVIDER_ID: &str = "gp-pool";

/// Account choice for the daemon chat path.
///
/// Chat is GP-preferred at ALL tiers (product decision): when the GP pool is
/// healthy, return the registry id of the GP-backed provider so the chat
/// handler prefers it; otherwise `None` (the handler's existing routing-table
/// default + health-scan fallback chain applies unchanged).
///
/// An EXPLICIT provider in the chat request must skip this preference — the
/// caller enforces that by only consulting this fn when no explicit provider
/// was supplied.
pub fn preferred_chat_provider(gp: &GpHealthSnapshot) -> Option<&'static str> {
    gp.is_healthy().then_some(GP_CHAT_PROVIDER_ID)
}

// ── Helpers (lifted from conductor_router — semantics preserved) ───────────────

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
///   - Codex uses `gpt-5.6-sol` (MODEL_GPT) regardless of class.
///   - Gemini T1/T2 uses `gemini-3.1-pro`; T3/Haiku uses `gemini-3.5-flash`.
///   - OpenCode and z.ai use `glm-5.2` (MODEL_GLM) regardless of class.
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
    pcts_saturated([
        qa.five_hour_utilization.unwrap_or(0.0),
        qa.seven_day_utilization.unwrap_or(0.0),
    ])
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
            // Codex uses GPT-5.6 Sol (MODEL_GPT) for all tiers; class is noted for logging only.
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
        "zai" | "glm" => {
            let model = MODEL_GLM.to_string();
            let reason = format!("zai account `{}`, model {}", qa.account, model);
            (Provider::Zai, model, reason)
        }
        "gfp" => {
            // GFP free-pool: always Flash.
            let model = MODEL_GEMINI_FLASH.to_string();
            let reason = format!("gfp pool `{}`, model {} (HTTP proxy)", qa.account, model);
            (Provider::Gfp, model, reason)
        }
        other => {
            // Unknown provider: treat as Codex-family for lack of better option.
            let model = MODEL_GPT.to_string();
            let reason = format!(
                "unknown provider `{other}` on `{}`, defaulting to gpt",
                qa.account
            );
            (Provider::Codex, model, reason)
        }
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::VecDeque;
    use std::time::Duration;

    // ── Snapshot builders ─────────────────────────────────────────────────────

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

    fn entry(account: &str, provider: &str, status: &str) -> QuotaAccount {
        QuotaAccount {
            account: account.to_string(),
            provider: provider.to_string(),
            status: status.to_string(),
            five_hour_utilization: None,
            seven_day_utilization: None,
            config_dir: None,
        }
    }

    fn saturated(mut qa: QuotaAccount) -> QuotaAccount {
        qa.five_hour_utilization = Some(100.0);
        qa
    }

    /// Full fleet snapshot with every spill-order account present and healthy.
    fn full_snapshot() -> QuotaSnapshot {
        QuotaSnapshot {
            accounts: vec![
                entry("gfp-pool", "gfp", "pool"),
                entry("glm-acc1", "zai", "ok"),
                entry("opencode-acc1", "opencode", "ok"),
                entry("opencode", "opencode", "ok"),
                entry("gemini-acc1", "gemini", "ok"),
                entry("codex-acc1", "codex", "ok"),
                entry("codex", "codex", "ok"),
                claude_entry("claude2", "/home/user/.claude2", 10.0, 20.0),
                claude_entry("claude", "/home/user/.claude", 5.0, 10.0),
            ],
        }
    }

    fn req(tier: Tier) -> SelectionRequest {
        SelectionRequest {
            tier,
            model_class: None,
            account_override: None,
        }
    }

    fn set_status(snap: &mut QuotaSnapshot, account: &str, status: &str) {
        snap.accounts
            .iter_mut()
            .find(|a| a.account == account)
            .unwrap_or_else(|| panic!("missing account {account}"))
            .status = status.into();
    }

    fn saturate_account(snap: &mut QuotaSnapshot, account: &str) {
        let pos = snap
            .accounts
            .iter()
            .position(|a| a.account == account)
            .unwrap_or_else(|| panic!("missing account {account}"));
        snap.accounts[pos] = saturated(snap.accounts[pos].clone());
    }

    fn slot(enabled: bool, re_enable_at: Option<Instant>) -> ProviderSlot {
        ProviderSlot {
            id: "gp-slot".into(),
            display_name: "GP Slot".into(),
            enabled,
            re_enable_at,
            request_count: 0,
            error_count: 0,
            rate_limit_count: 0,
            latency_ring: VecDeque::new(),
        }
    }

    // ── Every spill branch ────────────────────────────────────────────────────

    #[test]
    fn t2_healthy_selects_glm_first() {
        let target = select_account(
            &req(Tier::T2),
            &full_snapshot(),
            &GpHealthSnapshot::default(),
        )
        .expect("target");
        assert_eq!(target.account_id, "glm-acc1");
        assert_eq!(target.provider, Provider::Zai);
        assert_eq!(target.model, MODEL_GLM);
    }

    #[test]
    fn fleet_and_a2_capped_spills_to_a1_last() {
        let mut snap = full_snapshot();
        for account in [
            "glm-acc1",
            "opencode-acc1",
            "opencode",
            "gemini-acc1",
            "codex-acc1",
            "codex",
        ] {
            saturate_account(&mut snap, account);
        }
        saturate_account(&mut snap, "claude2");
        let target =
            select_account(&req(Tier::T2), &snap, &GpHealthSnapshot::default()).expect("target");
        assert_eq!(target.account_id, "claude");
        assert_eq!(target.provider, Provider::Claude);
        assert_eq!(target.model, MODEL_CLAUDE_SONNET);
        assert!(
            target.reason.contains("spill after"),
            "reason: {}",
            target.reason
        );
        assert!(
            target.reason.contains("gfp-pool"),
            "reason: {}",
            target.reason
        );
        assert!(
            target.reason.contains("glm-acc1"),
            "reason: {}",
            target.reason
        );
        assert!(
            target.reason.contains("opencode-acc1"),
            "reason: {}",
            target.reason
        );
        assert!(
            target.reason.contains("opencode"),
            "reason: {}",
            target.reason
        );
        assert!(
            target.reason.contains("gemini-acc1"),
            "reason: {}",
            target.reason
        );
        assert!(
            target.reason.contains("codex-acc1"),
            "reason: {}",
            target.reason
        );
        assert!(target.reason.contains("codex"), "reason: {}", target.reason);
        assert!(
            target.reason.contains("claude2"),
            "reason: {}",
            target.reason
        );
    }

    #[test]
    fn fleet_capped_spills_to_a2_before_a1() {
        let mut snap = full_snapshot();
        for account in [
            "glm-acc1",
            "opencode-acc1",
            "opencode",
            "gemini-acc1",
            "codex-acc1",
            "codex",
        ] {
            set_status(&mut snap, account, "needs_reauth");
        }
        let target =
            select_account(&req(Tier::T2), &snap, &GpHealthSnapshot::default()).expect("target");
        assert_eq!(target.account_id, "claude2");
        assert_eq!(target.provider, Provider::Claude);
        assert_eq!(target.model, MODEL_CLAUDE_SONNET);
        assert!(
            target.reason.contains("spill after"),
            "reason: {}",
            target.reason
        );
        assert!(
            target.reason.contains("gfp-pool"),
            "reason: {}",
            target.reason
        );
    }

    #[test]
    fn glm_dead_spills_to_opencode_acc1() {
        let mut snap = full_snapshot();
        set_status(&mut snap, "glm-acc1", "needs_reauth");
        let target =
            select_account(&req(Tier::T2), &snap, &GpHealthSnapshot::default()).expect("target");
        assert_eq!(target.account_id, "opencode-acc1");
        assert_eq!(target.provider, Provider::OpenCode);
        assert_eq!(target.model, MODEL_GLM);
    }

    #[test]
    fn glm_and_opencode_dead_spills_to_gemini() {
        let mut snap = full_snapshot();
        for account in ["glm-acc1", "opencode-acc1", "opencode"] {
            set_status(&mut snap, account, "needs_reauth");
        }
        let target =
            select_account(&req(Tier::T2), &snap, &GpHealthSnapshot::default()).expect("target");
        assert_eq!(target.account_id, "gemini-acc1");
        assert_eq!(target.provider, Provider::Gemini);
        assert_eq!(target.model, MODEL_GEMINI_PRO);
    }

    /// Regression test for a real bug (T-P7-E12, found 2026-08-15):
    /// ACCOUNT_SPILL_ORDER listed the Gemini account as "gemini-agt", but the
    /// real quota.json account is "gemini-acc1" — the exact-string `.find()`
    /// at the spill-order resolution step never matched, so the Gemini/agy
    /// lane was silently unreachable in the natural (non-override) spill
    /// chain even though quota.json reported it healthy at 0% utilization.
    /// Only `--account gemini-acc1` (the override path) ever selected it.
    /// This test locks the real account name into the spill order directly,
    /// independent of `full_snapshot()`'s fixture data, so a future rename of
    /// either side alone cannot silently reintroduce the mismatch.
    #[test]
    fn account_spill_order_gemini_entry_matches_real_account_name() {
        assert!(
            ACCOUNT_SPILL_ORDER
                .iter()
                .any(|(id, p)| *id == "gemini-acc1" && *p == Provider::Gemini),
            "ACCOUNT_SPILL_ORDER must contain (\"gemini-acc1\", Provider::Gemini) — \
             that is the real account id `agy` account in ~/.cascade/accounts/quota.json \
             uses in production; a mismatched id here silently removes the Gemini lane \
             from natural spill selection (see T-P7-E12 finding)."
        );
    }

    #[test]
    fn t2_fleet_saturated_spills_to_claude2_before_claude() {
        let mut snap = full_snapshot();
        for account in [
            "glm-acc1",
            "opencode-acc1",
            "opencode",
            "gemini-acc1",
            "codex-acc1",
            "codex",
        ] {
            set_status(&mut snap, account, "needs_reauth");
        }
        let target =
            select_account(&req(Tier::T2), &snap, &GpHealthSnapshot::default()).expect("target");
        assert_eq!(target.account_id, "claude2");
        assert_eq!(target.provider, Provider::Claude);
        assert_eq!(target.model, MODEL_CLAUDE_SONNET);
    }

    #[test]
    fn all_exhausted_returns_none() {
        let mut snap = full_snapshot();
        for acc in &mut snap.accounts {
            acc.status = "needs_reauth".into();
        }
        assert!(select_account(&req(Tier::T2), &snap, &GpHealthSnapshot::default()).is_none());
    }

    // ── T3-GP preference ──────────────────────────────────────────────────────

    #[test]
    fn t3_with_healthy_gp_prefers_gfp_first() {
        let gp = GpHealthSnapshot {
            healthy_slots: 3,
            ..Default::default()
        };
        let target = select_account(&req(Tier::T3), &full_snapshot(), &gp).expect("target");
        assert_eq!(target.provider, Provider::Gfp);
        assert_eq!(target.account_id, "gfp-pool");
        assert_eq!(target.model, MODEL_GEMINI_FLASH);
        assert!(
            target.reason.starts_with("t3-gp-preferred:"),
            "reason: {}",
            target.reason
        );
    }

    /// D14(b): T3 + unhealthy GP pool must fall through to glm-acc1, not gfp.
    ///
    /// WHY: Flash is free but an unhealthy pool (0 slots) means all keys are
    /// rate-limited or unregistered.  Routing to it would produce 429s or
    /// ECONNREFUSED.  The gate must skip gfp-pool and fall to the next candidate.
    #[test]
    fn t3_unhealthy_gp_falls_through_to_glm() {
        // total_slots > 0 represents "keys are configured but all rate-limited"
        // (is_exhausted() == true). The default snapshot (total_slots==0) means
        // "no probe done" and must NOT block GFP — see gate comment in select_account.
        let gp = GpHealthSnapshot {
            healthy_slots: 0,
            total_slots: 6,
            ..Default::default()
        };
        let target = select_account(&req(Tier::T3), &full_snapshot(), &gp).expect("target");
        assert_eq!(
            target.account_id, "glm-acc1",
            "unhealthy GP pool must not be selected for T3 — reason: {}",
            target.reason
        );
        assert_eq!(target.provider, Provider::Zai);
        assert_eq!(target.model, MODEL_GLM);
        // The reason should indicate that gfp-pool was skipped.
        assert!(
            target.reason.contains("gfp-pool"),
            "reason must mention skipped gfp-pool: {}",
            target.reason
        );
    }

    #[test]
    fn t2_ignores_gp_preference_even_when_healthy() {
        let gp = GpHealthSnapshot {
            healthy_slots: 5,
            ..Default::default()
        };
        let target = select_account(&req(Tier::T2), &full_snapshot(), &gp).expect("target");
        assert_eq!(
            target.account_id, "glm-acc1",
            "GP preference must be T3-only"
        );
        assert_eq!(target.provider, Provider::Zai);
        assert_eq!(target.model, MODEL_GLM);
    }

    #[test]
    fn t3_gp_preferred_but_pool_account_dead_falls_back() {
        let gp = GpHealthSnapshot {
            healthy_slots: 1,
            ..Default::default()
        };
        let mut snap = full_snapshot();
        set_status(&mut snap, "gfp-pool", "error"); // gfp-pool dead in quota.json
        let target = select_account(&req(Tier::T3), &snap, &gp).expect("target");
        assert_eq!(target.account_id, "glm-acc1");
        assert_eq!(target.provider, Provider::Zai);
        assert_eq!(target.model, MODEL_GLM);
        assert!(
            target.reason.contains("spill after"),
            "reason: {}",
            target.reason
        );
        assert!(
            target.reason.contains("gfp-pool"),
            "reason: {}",
            target.reason
        );
    }

    // ── GP health snapshot from RoutingTable slots ────────────────────────────

    #[test]
    fn gp_health_counts_enabled_slots() {
        let now = Instant::now();
        let slots = vec![slot(true, None), slot(true, None)];
        let gp = gp_health_from_slots_at(&slots, now);
        assert_eq!(gp.healthy_slots, 2);
        assert!(gp.is_healthy());
    }

    #[test]
    fn gp_health_excludes_slots_in_cooldown() {
        let now = Instant::now();
        let slots = vec![slot(false, Some(now + Duration::from_secs(60)))];
        let gp = gp_health_from_slots_at(&slots, now);
        assert_eq!(gp.healthy_slots, 0);
        assert!(!gp.is_healthy());
    }

    #[test]
    fn gp_health_counts_elapsed_cooldown_as_healthy() {
        let now = Instant::now();
        let slots = vec![slot(false, Some(now - Duration::from_secs(1)))];
        let gp = gp_health_from_slots_at(&slots, now);
        assert_eq!(
            gp.healthy_slots, 1,
            "elapsed cooldown must count as healthy"
        );
    }

    #[test]
    fn gp_health_empty_slots_unhealthy() {
        let gp = gp_health_from_slots_at(&[], Instant::now());
        assert!(!gp.is_healthy());
    }

    // ── GFP circuit breaker: exhaustion detection (Phase B, v1.13.0) ──────────

    /// All configured keys rate-limited → `is_exhausted() == true`. This is the
    /// exact state the :3762 adapter's fail-loud path checks before forwarding.
    #[test]
    fn all_keys_rate_limited_reports_exhausted() {
        let now = Instant::now();
        let slots = vec![
            slot(false, Some(now + Duration::from_secs(30))),
            slot(false, Some(now + Duration::from_secs(90))),
            slot(false, Some(now + Duration::from_secs(45))),
        ];
        let gp = gp_health_from_slots_at(&slots, now);
        assert_eq!(gp.healthy_slots, 0);
        assert_eq!(gp.total_slots, 3);
        assert!(
            gp.is_exhausted(),
            "3 configured slots, all in cooldown, must be exhausted"
        );
        // Earliest reset must be the minimum across all cooldowns (30s), not
        // the max or an arbitrary slot — this value is surfaced to the caller
        // in the fail-loud error message.
        assert_eq!(gp.earliest_reset_secs, Some(30));
    }

    /// An empty table (no GFP keys configured at all) is NOT "exhausted" — it
    /// is a configuration state distinct from transient rate-limit exhaustion.
    /// The circuit breaker must not fire a misleading "pool exhausted, wait Xs"
    /// message when the real problem is "no keys configured".
    #[test]
    fn empty_table_is_not_exhausted() {
        let gp = gp_health_from_slots_at(&[], Instant::now());
        assert_eq!(gp.total_slots, 0);
        assert!(
            !gp.is_exhausted(),
            "empty table is a config gap, not exhaustion"
        );
        assert_eq!(gp.earliest_reset_secs, None);
    }

    /// At least one healthy slot → not exhausted, and no reset time is reported
    /// (there is nothing to wait for).
    #[test]
    fn partial_health_is_not_exhausted() {
        let now = Instant::now();
        let slots = vec![
            slot(true, None),
            slot(false, Some(now + Duration::from_secs(60))),
        ];
        let gp = gp_health_from_slots_at(&slots, now);
        assert!(!gp.is_exhausted());
        assert_eq!(gp.earliest_reset_secs, None);
    }

    // ── TaskClass → Tier mapping (all six) ────────────────────────────────────

    #[test]
    fn task_class_tier_mapping_all_six() {
        assert_eq!(tier_for_task_class(TaskClass::InteractiveChat), Tier::T1);
        assert_eq!(tier_for_task_class(TaskClass::FinalGate), Tier::T1);
        assert_eq!(tier_for_task_class(TaskClass::BulkExec), Tier::T2);
        assert_eq!(tier_for_task_class(TaskClass::AdversarialReview), Tier::T2);
        assert_eq!(tier_for_task_class(TaskClass::Sensitive), Tier::T2);
        assert_eq!(tier_for_task_class(TaskClass::Cheap), Tier::T3);
    }

    // ── Saturation gate ───────────────────────────────────────────────────────

    #[test]
    fn pcts_saturated_thresholds() {
        assert!(!pcts_saturated([0.0, 50.0, 99.9]));
        assert!(pcts_saturated([0.0, 100.0]));
        assert!(pcts_saturated([120.0]));
        assert!(!pcts_saturated(std::iter::empty::<f64>()));
    }

    // ── Spill engine ──────────────────────────────────────────────────────────

    #[test]
    fn spill_select_records_skips_and_picks_first_pass() {
        let items = ["a", "b", "c"];
        let mut tried = Vec::new();
        let picked = spill_select(
            items.iter().copied(),
            |s| {
                if s == "c" {
                    Gate::Pass
                } else {
                    Gate::Skip("gated".into())
                }
            },
            |s| s.to_string(),
            &mut tried,
        );
        assert_eq!(picked, Some("c"));
        assert_eq!(
            tried,
            vec!["a (gated)".to_string(), "b (gated)".to_string()]
        );
    }

    #[test]
    fn spill_select_all_gated_returns_none() {
        let items = ["a"];
        let mut tried = Vec::new();
        let picked = spill_select(
            items.iter().copied(),
            |_| Gate::Skip("no".into()),
            |s| s.to_string(),
            &mut tried,
        );
        assert!(picked.is_none());
        assert_eq!(tried.len(), 1);
    }

    // ── Chat preference ───────────────────────────────────────────────────────

    #[test]
    fn chat_prefers_gp_when_healthy() {
        let gp = GpHealthSnapshot {
            healthy_slots: 1,
            ..Default::default()
        };
        assert_eq!(preferred_chat_provider(&gp), Some(GP_CHAT_PROVIDER_ID));
    }

    /// Regression (E1-S6 review): the chat preference id must never be the
    /// bare "gemini" slug — that id is claimed by user-added DIRECT Gemini
    /// keys via IPC (`build_adapter_for_id`) and by providers.json entry ids,
    /// so preferring it would either no-op or pin chat to a single key.
    #[test]
    fn chat_preference_id_is_reserved_pool_id_not_bare_gemini() {
        assert_ne!(GP_CHAT_PROVIDER_ID, "gemini");
        assert_eq!(GP_CHAT_PROVIDER_ID, "gp-pool");
    }

    #[test]
    fn chat_no_preference_when_gp_unhealthy() {
        assert_eq!(preferred_chat_provider(&GpHealthSnapshot::default()), None);
    }

    // ── Override + model-class semantics preserved ────────────────────────────

    #[test]
    fn override_bypasses_gp_preference() {
        let gp = GpHealthSnapshot {
            healthy_slots: 1,
            ..Default::default()
        };
        let r = SelectionRequest {
            tier: Tier::T3,
            model_class: None,
            account_override: Some("claude".to_string()),
        };
        let target = select_account(&r, &full_snapshot(), &gp).expect("target");
        assert_eq!(target.account_id, "claude");
        assert!(target.reason.starts_with("override:"));
    }

    #[test]
    fn t1_fleet_first_selects_glm() {
        let target = select_account(
            &req(Tier::T1),
            &full_snapshot(),
            &GpHealthSnapshot::default(),
        )
        .expect("target");
        assert_eq!(target.account_id, "glm-acc1");
        assert_eq!(target.provider, Provider::Zai);
        assert_eq!(target.model, MODEL_GLM);
    }
}
