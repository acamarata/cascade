//! # sensitivity_guard — dispatch guard for the sensitive-data firewall
//!
//! ## Purpose
//!
//! Implements [`SensitivityGuardEvaluator`], a [`PolicyEvaluator`] that is
//! inserted into the policy chain **before** any dispatch to a provider.
//!
//! The guard:
//! 1. Extracts the `payload` (user text) and `provider` fields from
//!    [`PolicyAction::args`].
//! 2. Calls [`cascade_core::sensitivity::classify_sensitivity`] on the payload.
//! 3. If the content is [`Sensitive`] and the provider is **not** in the trusted
//!    list (Claude / local), returns [`PolicyResult::deny`] with a clear reason.
//! 4. If no `payload` or `provider` is present in `args`, the guard allows by
//!    default (other evaluators handle non-dispatch actions).
//!
//! ## Integration
//!
//! The guard is prepended to the `PolicyChain` built by
//! [`crate::policy::dispatch::build_chain`] so it always runs first.
//!
//! ## Action contract
//!
//! The caller (daemon dispatch path) must set `action_type = "dispatch"` and
//! include at minimum:
//!
//! ```json
//! {
//!   "action_type": "dispatch",
//!   "args": {
//!     "payload": "<user text to send to the model>",
//!     "provider": "<provider-id>"
//!   }
//! }
//! ```
//!
//! Other `action_type` values pass through the guard unchanged.
//!
//! ## Constraints
//!
//! - No I/O, no async — the guard is purely in-process.
//! - No `unwrap()` outside `#[cfg(test)]`.
//! - File ≤ 300 lines.
//!
//! ## SPORT
//!
//! MASTER-POLICIES.md — cascade-harness: sensitivity_guard evaluator (v1.2)

use cascade_core::sensitivity::{
    classify_sensitivity, provider_is_trusted_for_sensitive, ContentSensitivity,
};
use cascade_types::policy::{PolicyAction, PolicyResult};

use crate::policy::engine::PolicyEvaluator;

// ── SensitivityGuardEvaluator ─────────────────────────────────────────────────

/// Policy evaluator that blocks sensitive content from reaching external
/// providers (Gemini / OpenAI / GFP / OC-Go / Codex).
///
/// Insert this evaluator at the **front** of the [`PolicyChain`] so it runs
/// before any other policy check for dispatch actions.
///
/// [`PolicyChain`]: crate::policy::engine::PolicyChain
pub struct SensitivityGuardEvaluator;

impl PolicyEvaluator for SensitivityGuardEvaluator {
    fn evaluate(&self, action: &PolicyAction) -> PolicyResult {
        // Only guard dispatch actions.
        if action.action_type != "dispatch" {
            return PolicyResult::allow(self.policy_id());
        }

        let args = &action.args;

        // Extract payload text. If absent, let the action through (nothing to classify).
        let payload = match args.get("payload").and_then(|v| v.as_str()) {
            Some(p) => p,
            None => return PolicyResult::allow(self.policy_id()),
        };

        // Extract provider id. If absent, let the action through.
        let provider = match args.get("provider").and_then(|v| v.as_str()) {
            Some(p) => p,
            None => return PolicyResult::allow(self.policy_id()),
        };

        // Classify the payload.
        let sensitivity = classify_sensitivity(payload);

        if sensitivity == ContentSensitivity::Sensitive
            && !provider_is_trusted_for_sensitive(provider)
        {
            return PolicyResult::deny(
                self.policy_id(),
                format!(
                    "Sensitive content (PII / VA / health / personal) cannot be dispatched to \
                     external provider '{}'. Allowed providers: Claude or local models only. \
                     Reclassify or switch provider before retrying.",
                    provider
                ),
            );
        }

        PolicyResult::allow(self.policy_id())
    }

    fn policy_id(&self) -> &str {
        "sensitive-data-firewall"
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::policy::{Decision, PolicyAction};
    use serde_json::json;

    fn guard() -> SensitivityGuardEvaluator {
        SensitivityGuardEvaluator
    }

    // ── Non-dispatch actions pass through ────────────────────────────────────

    #[test]
    fn non_dispatch_action_always_allowed() {
        let action = PolicyAction::new("bash", json!({"cmd": "cargo build"}));
        let result = guard().evaluate(&action);
        assert_eq!(result.decision, Decision::Allow);
    }

    #[test]
    fn read_action_always_allowed() {
        let action = PolicyAction::new("read", json!({"path": "/tmp/foo"}));
        let result = guard().evaluate(&action);
        assert_eq!(result.decision, Decision::Allow);
    }

    // ── Missing payload / provider → Allow ───────────────────────────────────

    #[test]
    fn dispatch_without_payload_allows() {
        let action = PolicyAction::new("dispatch", json!({"provider": "gemini"}));
        let result = guard().evaluate(&action);
        assert_eq!(result.decision, Decision::Allow);
    }

    #[test]
    fn dispatch_without_provider_allows() {
        let action = PolicyAction::new("dispatch", json!({"payload": "my ssn is 123-45-6789"}));
        let result = guard().evaluate(&action);
        assert_eq!(result.decision, Decision::Allow);
    }

    // ── Sensitive content + external provider → Deny ─────────────────────────

    #[test]
    fn sensitive_content_blocked_from_gemini() {
        let action = PolicyAction::new(
            "dispatch",
            json!({
                "payload": "my va disability rating is 70% service-connected",
                "provider": "gemini"
            }),
        );
        let result = guard().evaluate(&action);
        assert_eq!(
            result.decision,
            Decision::Deny,
            "VA content must be blocked from gemini"
        );
        assert_eq!(result.policy_id, "sensitive-data-firewall");
        assert!(result.reason.contains("gemini"));
    }

    #[test]
    fn sensitive_content_blocked_from_openai() {
        let action = PolicyAction::new(
            "dispatch",
            json!({
                "payload": "my diagnosis is PTSD and major depression",
                "provider": "openai"
            }),
        );
        let result = guard().evaluate(&action);
        assert_eq!(result.decision, Decision::Deny);
    }

    #[test]
    fn sensitive_content_blocked_from_gfp() {
        let action = PolicyAction::new(
            "dispatch",
            json!({
                "payload": "routing number 021000021 for wire transfer",
                "provider": "gfp"
            }),
        );
        let result = guard().evaluate(&action);
        assert_eq!(result.decision, Decision::Deny);
    }

    #[test]
    fn sensitive_content_blocked_from_oc_go() {
        let action = PolicyAction::new(
            "dispatch",
            json!({
                "payload": "social security number 123-45-6789",
                "provider": "oc-go"
            }),
        );
        let result = guard().evaluate(&action);
        assert_eq!(result.decision, Decision::Deny);
    }

    #[test]
    fn sensitive_content_blocked_from_codex() {
        let action = PolicyAction::new(
            "dispatch",
            json!({
                "payload": "my prescription expires next month",
                "provider": "codex"
            }),
        );
        let result = guard().evaluate(&action);
        assert_eq!(result.decision, Decision::Deny);
    }

    // ── Sensitive content + trusted provider → Allow ─────────────────────────

    #[test]
    fn sensitive_content_allowed_for_claude() {
        let action = PolicyAction::new(
            "dispatch",
            json!({
                "payload": "my va claim number is C12345678 disability rating 70%",
                "provider": "claude"
            }),
        );
        let result = guard().evaluate(&action);
        assert_eq!(
            result.decision,
            Decision::Allow,
            "sensitive content must be allowed for claude"
        );
    }

    #[test]
    fn sensitive_content_allowed_for_claude_variant() {
        let action = PolicyAction::new(
            "dispatch",
            json!({
                "payload": "date of birth: 1980-04-15",
                "provider": "claude-sonnet-4-6"
            }),
        );
        let result = guard().evaluate(&action);
        assert_eq!(result.decision, Decision::Allow);
    }

    #[test]
    fn sensitive_content_allowed_for_extra_account() {
        let action = PolicyAction::new(
            "dispatch",
            json!({
                "payload": "custody agreement and parenting plan",
                "provider": "acc2"
            }),
        );
        let result = guard().evaluate(&action);
        assert_eq!(result.decision, Decision::Allow);
    }

    #[test]
    fn sensitive_content_allowed_for_local_llm() {
        let action = PolicyAction::new(
            "dispatch",
            json!({
                "payload": "my medical record shows a diagnosis of hypertension",
                "provider": "local-llm"
            }),
        );
        let result = guard().evaluate(&action);
        assert_eq!(result.decision, Decision::Allow);
    }

    // ── Public content + any provider → Allow ───────────────────────────────

    #[test]
    fn public_content_allowed_for_all_providers() {
        let payload = "refactor the auth module to use JWT tokens";
        for provider in &["gemini", "openai", "gfp", "oc-go", "codex", "claude"] {
            let action = PolicyAction::new(
                "dispatch",
                json!({"payload": payload, "provider": provider}),
            );
            let result = guard().evaluate(&action);
            assert_eq!(
                result.decision,
                Decision::Allow,
                "public content should be allowed on provider {}",
                provider
            );
        }
    }
}
