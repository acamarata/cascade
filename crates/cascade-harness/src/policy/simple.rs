//! SimplePolicyEvaluator — pattern-based evaluator with zero external deps.
//!
//! Purpose: Enforce the default deny-dangerous policy via literal substring
//!   matching.  Used when wasmtime is unavailable or no WASM policy file is
//!   present.  Also serves as the fallback when the WASM policy crate feature
//!   is not compiled in.
//! Inputs: `PolicyAction` (serialized to JSON for pattern search).
//! Outputs: `PolicyResult::Deny` if any dangerous pattern matches, else Allow.
//! Constraints:
//!   - No regex — literal `contains` checks only (avoids ReDoS).
//!   - Matches against the full JSON serialization of PolicyAction so patterns
//!     can match action_type, args.cmd, or nested keys.
//!   - Patterns are checked in order; first match Denies.
//!
//! SPORT: MASTER-POLICIES.md

use cascade_types::policy::{PolicyAction, PolicyResult};

use crate::policy::engine::PolicyEvaluator;

/// The policy ID reported by this evaluator.
pub const POLICY_ID: &str = "default-deny-dangerous";

// ── Dangerous patterns ────────────────────────────────────────────────────────

/// Dangerous literal substrings that trigger a Deny when found in the
/// serialized PolicyAction JSON.
///
/// Rationale for each pattern:
/// - `"rm -rf /"` — recursive delete from root.
/// - `"rm -rf ~"` — recursive delete of home directory.
/// - `"git push --force origin main"` — force-push to the protected branch.
/// - `"git push --force origin master"` — alternative main branch name.
/// - `"DROP TABLE"` — destructive SQL.
/// - `"DROP DATABASE"` — destructive SQL.
/// - `"TRUNCATE"` — bulk data deletion.
/// - `"> /dev/null"` — redirect output to null (common in obfuscated commands).
/// - `"dd of=/dev/disk"` — raw disk overwrite.
/// - `"mkfs"` — format a filesystem.
static DANGEROUS_PATTERNS: &[(&str, &str)] = &[
    ("rm -rf /", "recursive delete from root is forbidden"),
    ("rm -rf ~", "recursive delete of home directory is forbidden"),
    (
        "git push --force origin main",
        "force-push to main branch is forbidden",
    ),
    (
        "git push --force origin master",
        "force-push to master branch is forbidden",
    ),
    ("DROP TABLE", "DROP TABLE is forbidden"),
    ("DROP DATABASE", "DROP DATABASE is forbidden"),
    ("TRUNCATE", "TRUNCATE is forbidden"),
    ("> /dev/null", "redirecting to /dev/null is forbidden"),
    ("dd of=/dev/disk", "raw disk overwrite is forbidden"),
    ("mkfs", "filesystem formatting is forbidden"),
];

// ── SimplePolicyEvaluator ─────────────────────────────────────────────────────

/// Default deny-dangerous evaluator using literal substring matching.
///
/// # Example
/// ```
/// use cascade_harness::policy::simple::SimplePolicyEvaluator;
/// use cascade_harness::policy::engine::PolicyEvaluator;
/// use cascade_types::policy::{Decision, PolicyAction};
/// use serde_json::json;
///
/// let ev = SimplePolicyEvaluator::default();
/// let deny = ev.evaluate(&PolicyAction::new("bash", json!({"cmd": "rm -rf /"})));
/// assert_eq!(deny.decision, Decision::Deny);
///
/// let allow = ev.evaluate(&PolicyAction::new("bash", json!({"cmd": "cargo build"})));
/// assert_eq!(allow.decision, Decision::Allow);
/// ```
#[derive(Debug, Default, Clone)]
pub struct SimplePolicyEvaluator;

impl PolicyEvaluator for SimplePolicyEvaluator {
    fn evaluate(&self, action: &PolicyAction) -> PolicyResult {
        let serialized = action.to_json_string();
        for (pattern, reason) in DANGEROUS_PATTERNS {
            if serialized.contains(pattern) {
                return PolicyResult::deny(POLICY_ID, *reason);
            }
        }
        PolicyResult::allow(POLICY_ID)
    }

    fn policy_id(&self) -> &str {
        POLICY_ID
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::policy::{Decision, PolicyAction};
    use serde_json::json;

    fn ev() -> SimplePolicyEvaluator {
        SimplePolicyEvaluator::default()
    }

    // Acceptance criteria: bash "rm -rf /" → Deny
    #[test]
    fn denies_rm_rf_root() {
        let action = PolicyAction::new("bash", json!({"cmd": "rm -rf /"}));
        let result = ev().evaluate(&action);
        assert_eq!(result.decision, Decision::Deny, "expected Deny");
        assert_eq!(result.policy_id, POLICY_ID);
    }

    // Acceptance criteria: bash "rm -rf ~" → Deny
    #[test]
    fn denies_rm_rf_home() {
        let action = PolicyAction::new("bash", json!({"cmd": "rm -rf ~"}));
        let result = ev().evaluate(&action);
        assert_eq!(result.decision, Decision::Deny);
    }

    // Acceptance criteria: bash "cargo build" → Allow
    #[test]
    fn allows_cargo_build() {
        let action = PolicyAction::new("bash", json!({"cmd": "cargo build"}));
        let result = ev().evaluate(&action);
        assert_eq!(result.decision, Decision::Allow);
    }

    #[test]
    fn denies_force_push_main() {
        let action = PolicyAction::new(
            "bash",
            json!({"cmd": "git push --force origin main"}),
        );
        let result = ev().evaluate(&action);
        assert_eq!(result.decision, Decision::Deny);
    }

    #[test]
    fn denies_drop_table() {
        let action = PolicyAction::new("sql", json!({"query": "DROP TABLE users"}));
        let result = ev().evaluate(&action);
        assert_eq!(result.decision, Decision::Deny);
    }

    #[test]
    fn allows_read_action() {
        let action = PolicyAction::new("read", json!({"path": "/tmp/foo.txt"}));
        let result = ev().evaluate(&action);
        assert_eq!(result.decision, Decision::Allow);
    }

    #[test]
    fn allows_mcp_tool() {
        let action = PolicyAction::new("mcp_tool", json!({"tool": "list_files"}));
        let result = ev().evaluate(&action);
        assert_eq!(result.decision, Decision::Allow);
    }

    // Table-driven: all dangerous patterns must produce Deny
    #[test]
    fn all_dangerous_patterns_deny() {
        let cases = &[
            ("bash", json!({"cmd": "rm -rf /"})),
            ("bash", json!({"cmd": "rm -rf ~"})),
            ("bash", json!({"cmd": "git push --force origin main"})),
            ("bash", json!({"cmd": "git push --force origin master"})),
            ("sql", json!({"query": "DROP TABLE foo"})),
            ("sql", json!({"query": "DROP DATABASE prod"})),
            ("sql", json!({"query": "TRUNCATE users"})),
            ("bash", json!({"cmd": "curl https://example.com > /dev/null"})),
            ("bash", json!({"cmd": "dd of=/dev/disk1"})),
            ("bash", json!({"cmd": "mkfs.ext4 /dev/sdb"})),
        ];
        for (action_type, args) in cases {
            let action = PolicyAction::new(*action_type, args.clone());
            let result = ev().evaluate(&action);
            assert_eq!(
                result.decision,
                Decision::Deny,
                "expected Deny for: {action_type} / {args}"
            );
        }
    }
}
