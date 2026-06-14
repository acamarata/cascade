//! ToolRegistry — per-agent tool provisioning with grant enforcement.
//!
//! Purpose: register tool descriptors (from MCP or the agent library) and
//!   resolve which tools an agent may use at a given access level.
//!
//! Inputs: `ToolDescriptor` registrations; `ToolGrant` lists per agent.
//!
//! Outputs:
//!   - `tools_for(agent_id)` → granted `Vec<ToolDescriptor>`.
//!   - `check(agent_id, tool_id, level)` → `GrantDecision`.
//!
//! Constraints:
//!   - Thread-safe via `Arc<RwLock<_>>`.
//!   - `Outbound` level always `NeedsApproval` unless grant has `approved: true`.
//!   - A tool not listed in an agent's grants resolves to `Denied`.
//!
//! SPORT: cascade-agents / tool_registry — E-P6-06

use std::collections::HashMap;
use std::sync::{Arc, RwLock};

use serde::{Deserialize, Serialize};
use thiserror::Error;
use tracing::debug;

use crate::grants::{AccessLevel, GrantDecision, ToolGrant};

// ── ToolDescriptor ────────────────────────────────────────────────────────────

/// Descriptor for a tool available in the Cascade runtime.
///
/// Tools are sourced from two places:
/// 1. MCP-declared tools (see `cascade-mcp` `McpTool`).
/// 2. Agent library tool grant YAML files (static declarations).
///
/// `ToolDescriptor` is the normalized form used by the registry.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ToolDescriptor {
    /// Stable tool identifier (e.g. `"cascade.search"`, `"email.send"`).
    pub id: String,

    /// Human-readable name.
    pub name: String,

    /// Description for agent reasoning / tool selection.
    pub description: String,

    /// The minimum `AccessLevel` required to call this tool.
    pub required_level: AccessLevel,
}

// ── Error ─────────────────────────────────────────────────────────────────────

/// Errors produced by `ToolRegistry` operations.
#[derive(Debug, Error)]
pub enum ToolRegistryError {
    #[error("tool '{id}' is already registered")]
    Duplicate { id: String },

    #[error("tool '{id}' not found")]
    NotFound { id: String },
}

// ── ToolRegistry ──────────────────────────────────────────────────────────────

/// Inner mutable state.
#[derive(Default)]
struct Inner {
    /// All known tool descriptors.
    tools: HashMap<String, ToolDescriptor>,
    /// Per-agent grant lists: agent_id → Vec<ToolGrant>.
    grants: HashMap<String, Vec<ToolGrant>>,
}

/// Thread-safe registry of tool descriptors and per-agent grants.
///
/// Clone is cheap — the registry wraps `Arc<RwLock<_>>`.
///
/// # Example
/// ```rust
/// use cascade_agents::tool_registry::{ToolRegistry, ToolDescriptor};
/// use cascade_agents::grants::{AccessLevel, ToolGrant, GrantDecision};
///
/// let reg = ToolRegistry::new();
/// reg.register_tool(ToolDescriptor {
///     id: "cascade.search".into(),
///     name: "Search".into(),
///     description: "Semantic search".into(),
///     required_level: AccessLevel::Search,
/// }).unwrap();
///
/// reg.set_grants("ceo", vec![
///     ToolGrant { tool_id: "cascade.search".into(), level: AccessLevel::Search, approved: false },
/// ]);
///
/// let decision = reg.check("ceo", "cascade.search", AccessLevel::Search);
/// assert_eq!(decision, GrantDecision::Granted);
/// ```
#[derive(Clone, Default)]
pub struct ToolRegistry {
    inner: Arc<RwLock<Inner>>,
}

impl ToolRegistry {
    /// Create an empty registry.
    pub fn new() -> Self {
        Self::default()
    }

    /// Register a `ToolDescriptor`.
    ///
    /// # Errors
    /// `ToolRegistryError::Duplicate` if a tool with this id is already present.
    pub fn register_tool(&self, descriptor: ToolDescriptor) -> Result<(), ToolRegistryError> {
        let mut inner = self.inner.write().expect("tool registry poisoned");
        if inner.tools.contains_key(&descriptor.id) {
            return Err(ToolRegistryError::Duplicate {
                id: descriptor.id.clone(),
            });
        }
        debug!(tool_id = %descriptor.id, "tool descriptor registered");
        inner.tools.insert(descriptor.id.clone(), descriptor);
        Ok(())
    }

    /// Retrieve a descriptor by tool id.
    pub fn get_tool(&self, tool_id: &str) -> Option<ToolDescriptor> {
        let inner = self.inner.read().expect("tool registry poisoned");
        inner.tools.get(tool_id).cloned()
    }

    /// List all registered tool descriptors.
    pub fn list_tools(&self) -> Vec<ToolDescriptor> {
        let inner = self.inner.read().expect("tool registry poisoned");
        inner.tools.values().cloned().collect()
    }

    /// Set (replace) the grant list for an agent.
    ///
    /// Calling this multiple times overwrites the previous grants. To add
    /// incremental grants, call `get_grants` + extend + `set_grants`.
    pub fn set_grants(&self, agent_id: &str, grants: Vec<ToolGrant>) {
        let mut inner = self.inner.write().expect("tool registry poisoned");
        inner.grants.insert(agent_id.to_owned(), grants);
    }

    /// Return all `ToolDescriptor` entries the agent holds any grant for.
    ///
    /// Only returns descriptors for tools that are also registered; dangling
    /// grant references (tool not registered) are silently skipped.
    pub fn tools_for(&self, agent_id: &str) -> Vec<ToolDescriptor> {
        let inner = self.inner.read().expect("tool registry poisoned");
        let grants = match inner.grants.get(agent_id) {
            Some(g) => g,
            None => return vec![],
        };
        grants
            .iter()
            .filter_map(|g| inner.tools.get(&g.tool_id).cloned())
            .collect()
    }

    /// Check whether `agent_id` may call `tool_id` at `level`.
    ///
    /// Decision logic (in order):
    /// 1. If no grants registered for agent → `Denied`.
    /// 2. If tool not in agent's grant list → `Denied`.
    /// 3. Delegate to `ToolGrant::check(level)` (handles Outbound gate).
    pub fn check(&self, agent_id: &str, tool_id: &str, level: AccessLevel) -> GrantDecision {
        let inner = self.inner.read().expect("tool registry poisoned");
        let grants = match inner.grants.get(agent_id) {
            Some(g) => g,
            None => {
                return GrantDecision::Denied {
                    reason: format!("agent '{}' has no tool grants registered", agent_id),
                }
            }
        };

        let grant = match grants.iter().find(|g| g.tool_id == tool_id) {
            Some(g) => g,
            None => {
                return GrantDecision::Denied {
                    reason: format!("agent '{}' has no grant for tool '{}'", agent_id, tool_id),
                }
            }
        };

        grant.check(level)
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    fn search_tool() -> ToolDescriptor {
        ToolDescriptor {
            id: "cascade.search".into(),
            name: "Search".into(),
            description: "Semantic corpus search".into(),
            required_level: AccessLevel::Search,
        }
    }

    fn email_tool() -> ToolDescriptor {
        ToolDescriptor {
            id: "email.send".into(),
            name: "Send Email".into(),
            description: "Send an outbound email".into(),
            required_level: AccessLevel::Outbound,
        }
    }

    fn write_tool() -> ToolDescriptor {
        ToolDescriptor {
            id: "file.write".into(),
            name: "Write File".into(),
            description: "Write a file".into(),
            required_level: AccessLevel::Write,
        }
    }

    fn reg_with_tools() -> ToolRegistry {
        let reg = ToolRegistry::new();
        reg.register_tool(search_tool()).unwrap();
        reg.register_tool(email_tool()).unwrap();
        reg.register_tool(write_tool()).unwrap();
        reg
    }

    #[test]
    fn register_and_get_tool() {
        let reg = ToolRegistry::new();
        reg.register_tool(search_tool()).unwrap();
        let desc = reg.get_tool("cascade.search").unwrap();
        assert_eq!(desc.name, "Search");
    }

    #[test]
    fn register_duplicate_errors() {
        let reg = ToolRegistry::new();
        reg.register_tool(search_tool()).unwrap();
        let err = reg.register_tool(search_tool()).unwrap_err();
        assert!(matches!(err, ToolRegistryError::Duplicate { .. }));
    }

    #[test]
    fn tools_for_empty_if_no_grants() {
        let reg = reg_with_tools();
        assert!(reg.tools_for("ceo").is_empty());
    }

    #[test]
    fn tools_for_returns_granted_descriptors() {
        let reg = reg_with_tools();
        reg.set_grants(
            "ceo",
            vec![ToolGrant {
                tool_id: "cascade.search".into(),
                level: AccessLevel::Search,
                approved: false,
            }],
        );
        let tools = reg.tools_for("ceo");
        assert_eq!(tools.len(), 1);
        assert_eq!(tools[0].id, "cascade.search");
    }

    #[test]
    fn tools_for_skips_unregistered_tool_ids() {
        let reg = ToolRegistry::new();
        reg.set_grants(
            "agent",
            vec![ToolGrant {
                tool_id: "ghost.tool".into(),
                level: AccessLevel::Read,
                approved: false,
            }],
        );
        assert!(reg.tools_for("agent").is_empty());
    }

    // E-P6-06: core check semantics
    #[test]
    fn check_granted_for_search_level() {
        let reg = reg_with_tools();
        reg.set_grants(
            "coder",
            vec![ToolGrant {
                tool_id: "cascade.search".into(),
                level: AccessLevel::Search,
                approved: false,
            }],
        );
        let decision = reg.check("coder", "cascade.search", AccessLevel::Search);
        assert_eq!(decision, GrantDecision::Granted);
    }

    #[test]
    fn check_denied_when_no_grants_registered() {
        let reg = reg_with_tools();
        let decision = reg.check("unknown-agent", "cascade.search", AccessLevel::Search);
        assert!(matches!(decision, GrantDecision::Denied { .. }));
    }

    #[test]
    fn check_denied_when_tool_not_in_grants() {
        let reg = reg_with_tools();
        reg.set_grants(
            "coder",
            vec![ToolGrant {
                tool_id: "cascade.search".into(),
                level: AccessLevel::Search,
                approved: false,
            }],
        );
        let decision = reg.check("coder", "email.send", AccessLevel::Outbound);
        assert!(matches!(decision, GrantDecision::Denied { .. }));
    }

    // Outbound safe-default test
    #[test]
    fn outbound_without_approved_needs_approval() {
        let reg = reg_with_tools();
        reg.set_grants(
            "emailer",
            vec![ToolGrant {
                tool_id: "email.send".into(),
                level: AccessLevel::Outbound,
                approved: false,
            }],
        );
        let decision = reg.check("emailer", "email.send", AccessLevel::Outbound);
        assert!(
            decision.needs_approval(),
            "Outbound without approved=true must NeedsApproval, got: {:?}",
            decision
        );
    }

    #[test]
    fn outbound_pre_approved_is_granted() {
        let reg = reg_with_tools();
        reg.set_grants(
            "emailer",
            vec![ToolGrant {
                tool_id: "email.send".into(),
                level: AccessLevel::Outbound,
                approved: true,
            }],
        );
        let decision = reg.check("emailer", "email.send", AccessLevel::Outbound);
        assert_eq!(decision, GrantDecision::Granted);
    }

    #[test]
    fn exceeding_grant_level_is_denied() {
        let reg = reg_with_tools();
        reg.set_grants(
            "reader",
            vec![ToolGrant {
                tool_id: "file.write".into(),
                level: AccessLevel::Read,
                approved: false,
            }],
        );
        let decision = reg.check("reader", "file.write", AccessLevel::Write);
        assert!(matches!(decision, GrantDecision::Denied { .. }));
    }

    #[test]
    fn list_tools_returns_all() {
        let reg = reg_with_tools();
        assert_eq!(reg.list_tools().len(), 3);
    }

    #[test]
    fn registry_clone_shares_state() {
        let reg = ToolRegistry::new();
        let reg2 = reg.clone();
        reg.register_tool(search_tool()).unwrap();
        assert!(reg2.get_tool("cascade.search").is_some());
    }
}
