//! Policy engine types for Cascade guardrails.
//!
//! Purpose: Define the shared PolicyAction / PolicyResult / Decision types used
//!   by the policy engine in cascade-harness and the policy CLI in cascade-cli.
//!   Also defines the [policy] and [harness] cascade.toml table schemas.
//! Inputs: Structured action description from dispatch callers.
//! Outputs: Decision (Allow/Deny/RequireApproval) + reason + policy_id.
//! Constraints:
//!   - Zero provider coupling — no agent or LLM names.
//!   - Fully serializable so CLI tools can pass actions as JSON.
//!   - AND semantics across policy chains: first Deny wins.
//!   - Tier inheritance: lower tiers (PAC) inherit from higher (GCI); lower
//!     tiers may override rules with matching IDs but never silently drop them.
//!
//! SPORT: MASTER-POLICIES.md

use std::collections::HashMap;

use serde::{Deserialize, Serialize};

use crate::tiers::TierName;

// ── Decision ──────────────────────────────────────────────────────────────────

/// The outcome of evaluating a `PolicyAction` against a policy.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "UPPERCASE")]
pub enum Decision {
    /// The action is allowed to proceed.
    Allow,
    /// The action is denied; the reason field explains why.
    Deny,
    /// The action requires human approval before proceeding.
    RequireApproval,
}

impl std::fmt::Display for Decision {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Decision::Allow => write!(f, "ALLOW"),
            Decision::Deny => write!(f, "DENY"),
            Decision::RequireApproval => write!(f, "REQUIRE_APPROVAL"),
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

    /// Construct a RequireApproval result.
    pub fn require_approval(policy_id: impl Into<String>, reason: impl Into<String>) -> Self {
        Self {
            decision: Decision::RequireApproval,
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

    /// Returns true when the decision is RequireApproval.
    pub fn is_require_approval(&self) -> bool {
        self.decision == Decision::RequireApproval
    }
}

// ── PolicyScope ───────────────────────────────────────────────────────────────

/// Which cascade tier(s) a policy rule applies at.
///
/// Maps to the `scope` field in a `[[policy.rules]]` entry in `cascade.toml`.
/// Lower tiers inherit rules from higher tiers; a rule scoped to `Gci` applies
/// everywhere unless overridden by a rule with the same `id` at a lower tier.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum PolicyScope {
    /// Rule applies at every tier (GCI through PAC).
    Global,
    /// Rule applies only at the per-project tiers (PPC, PRC, PAC).
    Project,
    /// Rule applies only at the per-repo tier (PRC, PAC).
    Repo,
    /// Rule applies only at the per-app tier (PAC).
    App,
}

impl PolicyScope {
    /// Returns true if this scope covers the given tier.
    pub fn covers(&self, tier: TierName) -> bool {
        match self {
            PolicyScope::Global => true,
            PolicyScope::Project => matches!(tier, TierName::PPC | TierName::PRC | TierName::PAC),
            PolicyScope::Repo => matches!(tier, TierName::PRC | TierName::PAC),
            PolicyScope::App => matches!(tier, TierName::PAC),
        }
    }
}

impl std::fmt::Display for PolicyScope {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            PolicyScope::Global => write!(f, "global"),
            PolicyScope::Project => write!(f, "project"),
            PolicyScope::Repo => write!(f, "repo"),
            PolicyScope::App => write!(f, "app"),
        }
    }
}

// ── PolicyRule ────────────────────────────────────────────────────────────────

/// A single policy rule from the `[[policy.rules]]` table in `cascade.toml`.
///
/// # TOML shape
///
/// ```toml
/// [[policy.rules]]
/// id       = "no-force-push"
/// scope    = "global"
/// match    = "git push --force*"
/// decision = "DENY"
/// reason   = "Force-push to main is forbidden"
/// ```
///
/// - `id`: unique within a PolicySet; lower-tier rules with the same id override.
/// - `scope`: which tier(s) this rule applies at.
/// - `match`: a glob pattern matched against the serialized PolicyAction JSON.
/// - `decision`: Allow | Deny | RequireApproval.
/// - `reason`: human-readable explanation (required for Deny/RequireApproval).
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub struct PolicyRule {
    /// Unique rule identifier. Lower-tier rules with the same id override
    /// higher-tier rules (inheritance + override semantics).
    pub id: String,

    /// Which tier(s) this rule applies at.
    #[serde(default = "PolicyRule::default_scope")]
    pub scope: PolicyScope,

    /// Glob pattern matched against the serialized `PolicyAction` JSON string.
    /// Use `*` for single-segment wildcard, `**` is treated the same.
    pub r#match: String,

    /// The decision to return when the pattern matches.
    pub decision: Decision,

    /// Human-readable reason (required for Deny and RequireApproval).
    #[serde(default)]
    pub reason: String,
}

impl PolicyRule {
    fn default_scope() -> PolicyScope {
        PolicyScope::Global
    }

    /// Evaluate whether this rule matches the serialized action string.
    ///
    /// Uses simple glob matching: `*` matches any sequence of characters except
    /// nothing in particular — we convert `*` to a substring any-match.
    /// For full glob semantics the caller should use the `PolicySet` evaluator
    /// which compiles globs via the `glob` pattern.
    pub fn matches_action(&self, action_json: &str) -> bool {
        glob_match(&self.r#match, action_json)
    }
}

/// Simple glob matcher: `*` matches any substring (zero or more chars).
/// Compile-time patterns; no external deps — avoids ReDoS.
fn glob_match(pattern: &str, text: &str) -> bool {
    // Split pattern on `*` and verify each segment appears in order.
    let parts: Vec<&str> = pattern.split('*').collect();
    if parts.is_empty() {
        return true;
    }
    let mut remaining = text;
    for (i, part) in parts.iter().enumerate() {
        if part.is_empty() {
            continue;
        }
        if i == 0 && !pattern.starts_with('*') {
            // First part must match at start of remaining.
            if !remaining.starts_with(part) {
                return false;
            }
            remaining = &remaining[part.len()..];
        } else {
            match remaining.find(part) {
                Some(pos) => remaining = &remaining[pos + part.len()..],
                None => return false,
            }
        }
    }
    // If pattern does not end with `*`, remaining text must be empty.
    if !pattern.ends_with('*') && !remaining.is_empty() {
        // There are non-`*` chars after the last segment — text has trailing chars.
        // Only invalid if the last segment is not empty (i.e. pattern doesn't end `*`).
        // We already consumed the last segment above; if remaining is non-empty here
        // and pattern doesn't end with `*`, the match fails.
        return false;
    }
    true
}

// ── PolicyTableConfig ─────────────────────────────────────────────────────────

/// The `[policy]` table in `cascade.toml`.
///
/// # TOML shape
///
/// ```toml
/// [policy]
/// [[policy.rules]]
/// id       = "deny-rm-rf"
/// scope    = "global"
/// match    = "*rm -rf /*"
/// decision = "DENY"
/// reason   = "Recursive delete from root is forbidden"
///
/// [[policy.rules]]
/// id       = "require-approval-deploy"
/// scope    = "project"
/// match    = "*deploy*prod*"
/// decision = "REQUIREAPPROVAL"
/// reason   = "Production deploys require human approval"
/// ```
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(default, rename_all = "snake_case")]
pub struct PolicyTableConfig {
    /// Policy rules defined at this tier.
    #[serde(default)]
    pub rules: Vec<PolicyRule>,
}

// ── PolicySet ─────────────────────────────────────────────────────────────────

/// The effective merged policy set for a given cwd, produced by merging
/// `PolicyTableConfig` entries across the resolved tier chain.
///
/// Merge semantics:
/// - Rules are accumulated from GCI (highest precedence) through PAC.
/// - A lower-tier rule with the same `id` as a higher-tier rule REPLACES the
///   higher-tier rule (override semantics), allowing project-scoped relaxations.
/// - Rules are filtered by `scope.covers(tier)` at the tier where they are
///   defined. A `scope = "project"` rule at GCI level still applies at PPC/PRC/PAC.
/// - The merge result preserves provenance: `PolicySetEntry` records which
///   tier the winning rule came from.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct PolicySet {
    /// The effective rules in evaluation order (GCI-sourced first, then lower tiers).
    pub entries: Vec<PolicySetEntry>,
}

/// A rule + its provenance tier in the merged `PolicySet`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PolicySetEntry {
    /// The effective policy rule.
    pub rule: PolicyRule,
    /// The tier that provided the winning version of this rule.
    pub source_tier: TierName,
}

impl PolicySet {
    /// Build a merged `PolicySet` from a list of `(tier, config)` pairs.
    ///
    /// Pairs must be in highest-precedence-first order (GCI first, PAC last).
    /// Lower-tier rules with the same `id` override higher-tier rules.
    /// Rules are filtered by `scope.covers(tier)` at their definition tier.
    pub fn merge(tier_configs: &[(TierName, PolicyTableConfig)]) -> Self {
        // Use an ordered map keyed by rule id to handle overrides.
        // We process highest-tier-first; lower tiers override (replace) entries.
        let mut rule_map: HashMap<String, PolicySetEntry> = HashMap::new();

        for (tier, cfg) in tier_configs {
            for rule in &cfg.rules {
                // Only include rule if its scope covers this tier.
                if rule.scope.covers(*tier) {
                    rule_map.insert(
                        rule.id.clone(),
                        PolicySetEntry {
                            rule: rule.clone(),
                            source_tier: *tier,
                        },
                    );
                }
            }
        }

        // Collect and sort: GCI-sourced entries first (by TierName's Ord impl).
        let mut entries: Vec<PolicySetEntry> = rule_map.into_values().collect();
        entries.sort_by_key(|e| e.source_tier);

        Self { entries }
    }

    /// Evaluate `action_json` against the merged policy set.
    ///
    /// Returns the first non-Allow result (Deny or RequireApproval) or Allow.
    /// Deny beats RequireApproval: if any rule Denies, the result is Deny.
    pub fn evaluate(&self, action_json: &str) -> PolicyResult {
        let mut require_approval: Option<PolicySetEntry> = None;

        for entry in &self.entries {
            if entry.rule.matches_action(action_json) {
                match entry.rule.decision {
                    Decision::Deny => {
                        return PolicyResult::deny(
                            entry.rule.id.clone(),
                            entry.rule.reason.clone(),
                        );
                    }
                    Decision::RequireApproval => {
                        if require_approval.is_none() {
                            require_approval = Some(entry.clone());
                        }
                    }
                    Decision::Allow => {}
                }
            }
        }

        if let Some(entry) = require_approval {
            return PolicyResult::require_approval(
                entry.rule.id.clone(),
                entry.rule.reason.clone(),
            );
        }

        PolicyResult::allow("policy-set")
    }

    /// Returns true when the set contains no rules.
    pub fn is_empty(&self) -> bool {
        self.entries.is_empty()
    }

    /// Number of effective rules in the merged set.
    pub fn len(&self) -> usize {
        self.entries.len()
    }
}
