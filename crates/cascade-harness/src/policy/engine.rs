//! Policy engine — trait + chain evaluator.
//!
//! Purpose: Define the `PolicyEvaluator` trait and the `PolicyChain` that
//!   applies AND semantics (first Deny wins) across multiple evaluators.
//! Inputs: `PolicyAction` from dispatch callers.
//! Outputs: `PolicyResult` (Allow/Deny + reason + policy_id).
//! Constraints:
//!   - AND semantics: all evaluators must Allow for the action to proceed.
//!   - Chain stops at the first Deny.
//!   - Implementations must be Send + Sync for use in async dispatch paths.
//!
//! SPORT: MASTER-POLICIES.md

use cascade_types::policy::{PolicyAction, PolicyResult};

// ── PolicyEvaluator trait ─────────────────────────────────────────────────────

/// Evaluate one action against one policy.
///
/// Implementations must be `Send + Sync` so they can be stored in a global
/// `lazy_static` chain and called from async Tokio tasks.
pub trait PolicyEvaluator: Send + Sync {
    /// Evaluate `action` and return the policy decision.
    ///
    /// Returning `PolicyResult::allow(…)` means "this policy has no objection".
    /// Returning `PolicyResult::deny(…)` means "this policy blocks the action".
    fn evaluate(&self, action: &PolicyAction) -> PolicyResult;

    /// Human-readable policy identifier (e.g. `"default-deny-dangerous"`).
    fn policy_id(&self) -> &str;
}

// ── PolicyChain ───────────────────────────────────────────────────────────────

/// Ordered collection of evaluators.  AND semantics: the first Deny stops the
/// chain and the Deny result is returned.  If all evaluators Allow, the final
/// Allow from the last evaluator is returned.
pub struct PolicyChain {
    evaluators: Vec<Box<dyn PolicyEvaluator>>,
}

impl PolicyChain {
    /// Build a chain from a vec of boxed evaluators.
    pub fn new(evaluators: Vec<Box<dyn PolicyEvaluator>>) -> Self {
        Self { evaluators }
    }

    /// Evaluate `action` against all policies in order.
    /// Returns the first Deny encountered, or Allow if all pass.
    pub fn evaluate(&self, action: &PolicyAction) -> PolicyResult {
        for evaluator in &self.evaluators {
            let result = evaluator.evaluate(action);
            if result.is_deny() {
                return result;
            }
        }
        // All evaluators allowed.
        let last_id = self
            .evaluators
            .last()
            .map(|e| e.policy_id())
            .unwrap_or("no-policy");
        PolicyResult::allow(last_id)
    }

    /// Number of evaluators in the chain.
    pub fn len(&self) -> usize {
        self.evaluators.len()
    }

    /// True when the chain contains no evaluators.
    pub fn is_empty(&self) -> bool {
        self.evaluators.is_empty()
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::policy::{Decision, PolicyAction};
    use serde_json::json;

    struct AlwaysAllow;
    impl PolicyEvaluator for AlwaysAllow {
        fn evaluate(&self, _action: &PolicyAction) -> PolicyResult {
            PolicyResult::allow("always-allow")
        }
        fn policy_id(&self) -> &str {
            "always-allow"
        }
    }

    struct AlwaysDeny;
    impl PolicyEvaluator for AlwaysDeny {
        fn evaluate(&self, _action: &PolicyAction) -> PolicyResult {
            PolicyResult::deny("always-deny", "test denial")
        }
        fn policy_id(&self) -> &str {
            "always-deny"
        }
    }

    #[test]
    fn empty_chain_allows() {
        let chain = PolicyChain::new(vec![]);
        let action = PolicyAction::new("bash", json!({"cmd": "cargo build"}));
        let result = chain.evaluate(&action);
        assert_eq!(result.decision, Decision::Allow);
    }

    #[test]
    fn all_allow_chain_allows() {
        let chain = PolicyChain::new(vec![Box::new(AlwaysAllow), Box::new(AlwaysAllow)]);
        let action = PolicyAction::new("bash", json!({"cmd": "cargo build"}));
        let result = chain.evaluate(&action);
        assert_eq!(result.decision, Decision::Allow);
    }

    #[test]
    fn first_deny_stops_chain() {
        let chain = PolicyChain::new(vec![Box::new(AlwaysDeny), Box::new(AlwaysAllow)]);
        let action = PolicyAction::new("bash", json!({"cmd": "cargo build"}));
        let result = chain.evaluate(&action);
        assert_eq!(result.decision, Decision::Deny);
        assert_eq!(result.policy_id, "always-deny");
    }

    #[test]
    fn deny_in_middle_stops_chain() {
        let chain = PolicyChain::new(vec![
            Box::new(AlwaysAllow),
            Box::new(AlwaysDeny),
            Box::new(AlwaysAllow),
        ]);
        let action = PolicyAction::new("bash", json!({"cmd": "ls /tmp"}));
        let result = chain.evaluate(&action);
        assert_eq!(result.decision, Decision::Deny);
    }
}
