//! Policy engine types for Cascade guardrails.
//!
//! Purpose: Define the shared PolicyAction / PolicyResult / Decision types used
//!   by the policy engine in cascade-harness and the policy CLI in cascade-cli.
//! Inputs: Structured action description from dispatch callers.
//! Outputs: Decision (Allow/Deny) + reason + policy_id.
//! Constraints:
//!   - Zero provider coupling — no agent or LLM names.
//!   - Fully serializable so CLI tools can pass actions as JSON.
//!   - AND semantics across policy chains: first Deny wins.
//!
//! SPORT: MASTER-POLICIES.md

use serde::{Deserialize, Serialize};

// ── Decision ──────────────────────────────────────────────────────────────────

/// The outcome of evaluating a `PolicyAction` against a policy.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "UPPERCASE")]
pub enum Decision {
    /// The action is allowed to proceed.
    Allow,
    /// The action is denied; the reason field explains why.
    Deny,
}

impl std::fmt::Display for Decision {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Decision::Allow => write!(f, "ALLOW"),
            Decision::Deny => write!(f, "DENY"),
        }
    }
}

// ── PolicyAction ─────────────────────────────────────────────────────────────

/// A discrete action that the policy engine evaluates before dispatch.
///
/// `action_type` is a free-form string identifying the kind of operation
/// (e.g. `"bash"`, `"read"`, `"write"`, `"mcp_tool"`).  `args` carries
/// the typed payload; `context` carries ambient data (cwd, repo, harness).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PolicyAction {
    /// Category of action being taken (e.g. `"bash"`, `"read"`, `"mcp_tool"`).
    pub action_type: String,
    /// Action-specific arguments (serialized as a JSON object).
    pub args: serde_json::Value,
    /// Ambient context at dispatch time (cwd, repo path, harness, etc.).
    #[serde(default)]
    pub context: serde_json::Value,
}

impl PolicyAction {
    /// Convenience constructor.
    pub fn new(action_type: impl Into<String>, args: serde_json::Value) -> Self {
        Self {
            action_type: action_type.into(),
            args,
            context: serde_json::Value::Null,
        }
    }

    /// Serialize the full action to a compact JSON string for pattern matching.
    pub fn to_json_string(&self) -> String {
        serde_json::to_string(self).unwrap_or_default()
    }
}

// ── PolicyResult ─────────────────────────────────────────────────────────────

/// The result produced by a `PolicyEvaluator` for one `PolicyAction`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PolicyResult {
    /// Allow or Deny.
    pub decision: Decision,
    /// Human-readable explanation (empty string for Allow when no comment needed).
    pub reason: String,
    /// The ID of the policy that produced this decision.
    /// For `SimplePolicyEvaluator` this is `"default-deny-dangerous"`.
    /// For WASM evaluators this is the WASM file stem.
    pub policy_id: String,
}

impl PolicyResult {
    /// Construct an Allow result.
    pub fn allow(policy_id: impl Into<String>) -> Self {
        Self {
            decision: Decision::Allow,
            reason: String::new(),
            policy_id: policy_id.into(),
        }
    }

    /// Construct a Deny result.
    pub fn deny(policy_id: impl Into<String>, reason: impl Into<String>) -> Self {
        Self {
            decision: Decision::Deny,
            reason: reason.into(),
            policy_id: policy_id.into(),
        }
    }

    /// Returns true when the decision is Allow.
    pub fn is_allow(&self) -> bool {
        self.decision == Decision::Allow
    }

    /// Returns true when the decision is Deny.
    pub fn is_deny(&self) -> bool {
        self.decision == Decision::Deny
    }
}
