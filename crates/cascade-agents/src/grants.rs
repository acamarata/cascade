//! Tool grants — per-agent access level declarations for tool calls.
//!
//! Purpose: define the `ToolGrant` (per-agent allow-list) and `AccessLevel`
//!   enum that governs what an agent may do with a given tool. `Outbound`
//!   access (email/ticket/publish) requires explicit approval by default.
//! Inputs: a `ToolGrant` set loaded from the agent library or constructed in code.
//! Outputs: `GrantDecision` returned by `check(agent, tool, level)`.
//! Constraints:
//!   - `Outbound` always resolves to `NeedsApproval` unless explicitly overridden
//!     with `approved: true` in the grant (safe default, per E-P6-06 spec).
//!   - No async — grant checking is a pure synchronous lookup.
//!   - serde camelCase; STRUCT variants only.
//! SPORT: cascade-agents / grants — E-P6-06

use serde::{Deserialize, Serialize};

// ── AccessLevel ───────────────────────────────────────────────────────────────

/// The level of access an agent is granted for a specific tool.
///
/// Levels are ordered by privilege; a grant at a higher level implies all
/// lower levels (e.g. `Write` implies `Read`).
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub enum AccessLevel {
    /// Read-only access (e.g. read files, list directories).
    Read,
    /// Search or query access (e.g. semantic search, code search).
    Search,
    /// Call an LLM provider.
    CallLlm,
    /// Write or mutate state (e.g. write files, create issues).
    Write,
    /// Outbound operations: email, ticket publish, external API post.
    ///
    /// **Default: requires approval.** The `ToolGrant` may set `approved: true`
    /// to bypass the approval gate for trusted, pre-authorized agents.
    Outbound,
}

// ── GrantDecision ─────────────────────────────────────────────────────────────

/// The outcome of a grant check for a specific (agent, tool, level) triple.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", tag = "decision")]
pub enum GrantDecision {
    /// The agent may proceed with the requested access level.
    Granted,
    /// The access level requires explicit human approval before proceeding.
    /// This is always returned for `Outbound` unless `approved: true` is set.
    NeedsApproval {
        /// Human-readable reason for the approval requirement.
        reason: String,
    },
    /// The agent is not permitted to use this tool at this level.
    Denied {
        /// Human-readable reason for the denial.
        reason: String,
    },
}

impl GrantDecision {
    /// Returns `true` if the decision is `Granted`.
    pub fn is_granted(&self) -> bool {
        matches!(self, Self::Granted)
    }

    /// Returns `true` if the decision requires human approval.
    pub fn needs_approval(&self) -> bool {
        matches!(self, Self::NeedsApproval { .. })
    }
}

// ── ToolGrant ─────────────────────────────────────────────────────────────────

/// A permission entry allowing a specific tool at a specific access level.
///
/// `ToolGrant` entries are collected in an allow-list per agent. Any (tool, level)
/// combination not on the allow-list is `Denied`.
///
/// # Example (YAML)
/// ```yaml
/// toolId: "cascade.search"
/// level: "search"
/// approved: false
/// ```
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ToolGrant {
    /// The tool this grant applies to (matches `ToolDescriptor::id`).
    pub tool_id: String,

    /// The maximum access level granted.
    pub level: AccessLevel,

    /// When `true` and `level == Outbound`, the approval gate is bypassed.
    ///
    /// For all non-Outbound levels this field is ignored.
    /// Setting this to `true` must be an explicit, intentional decision.
    #[serde(default)]
    pub approved: bool,
}

impl ToolGrant {
    /// Evaluate whether `requested` access is allowed.
    ///
    /// Decision logic:
    /// 1. If `requested > self.level`: `Denied`.
    /// 2. If `requested == Outbound` and `!self.approved`: `NeedsApproval`.
    /// 3. Otherwise: `Granted`.
    pub fn check(&self, requested: AccessLevel) -> GrantDecision {
        if requested > self.level {
            return GrantDecision::Denied {
                reason: format!(
                    "requested level {:?} exceeds grant level {:?} for tool '{}'",
                    requested, self.level, self.tool_id
                ),
            };
        }

        if requested == AccessLevel::Outbound && !self.approved {
            return GrantDecision::NeedsApproval {
                reason: format!(
                    "outbound access to tool '{}' requires explicit approval",
                    self.tool_id
                ),
            };
        }

        GrantDecision::Granted
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    fn grant(tool_id: &str, level: AccessLevel, approved: bool) -> ToolGrant {
        ToolGrant {
            tool_id: tool_id.into(),
            level,
            approved,
        }
    }

    // E-P6-06: Outbound without approved = NeedsApproval (safe default)
    #[test]
    fn outbound_requires_approval_by_default() {
        let g = grant("email.send", AccessLevel::Outbound, false);
        let decision = g.check(AccessLevel::Outbound);
        assert!(
            decision.needs_approval(),
            "Outbound without approved=true must NeedsApproval"
        );
    }

    #[test]
    fn outbound_pre_approved_is_granted() {
        let g = grant("email.send", AccessLevel::Outbound, true);
        assert_eq!(g.check(AccessLevel::Outbound), GrantDecision::Granted);
    }

    #[test]
    fn read_within_grant_is_granted() {
        let g = grant("cascade.search", AccessLevel::Search, false);
        assert_eq!(g.check(AccessLevel::Read), GrantDecision::Granted);
    }

    #[test]
    fn write_exceeding_read_grant_is_denied() {
        let g = grant("files", AccessLevel::Read, false);
        let decision = g.check(AccessLevel::Write);
        assert!(matches!(decision, GrantDecision::Denied { .. }));
    }

    #[test]
    fn exact_level_non_outbound_is_granted() {
        let g = grant("code.search", AccessLevel::Search, false);
        assert_eq!(g.check(AccessLevel::Search), GrantDecision::Granted);
    }

    #[test]
    fn call_llm_grant_allows_read_and_search() {
        let g = grant("provider", AccessLevel::CallLlm, false);
        assert_eq!(g.check(AccessLevel::Read), GrantDecision::Granted);
        assert_eq!(g.check(AccessLevel::Search), GrantDecision::Granted);
        assert_eq!(g.check(AccessLevel::CallLlm), GrantDecision::Granted);
    }

    #[test]
    fn grant_serde_camel_case_round_trip() {
        let g = ToolGrant {
            tool_id: "cascade.memory.write".into(),
            level: AccessLevel::Write,
            approved: false,
        };
        let json = serde_json::to_string(&g).unwrap();
        assert!(json.contains("\"toolId\""), "expected camelCase toolId");
        let decoded: ToolGrant = serde_json::from_str(&json).unwrap();
        assert_eq!(decoded.tool_id, "cascade.memory.write");
        assert_eq!(decoded.level, AccessLevel::Write);
    }

    #[test]
    fn grant_decision_is_granted_predicate() {
        assert!(GrantDecision::Granted.is_granted());
        assert!(!GrantDecision::NeedsApproval { reason: "x".into() }.is_granted());
        assert!(!GrantDecision::Denied { reason: "y".into() }.is_granted());
    }
}
