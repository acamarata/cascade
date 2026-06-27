//! AgentRegistry — thread-safe store for `AgentSpec` entries.
//!
//! Purpose: register, retrieve, list, and remove agent specs; match agents by
//!   capability tag; route task-kind strings to the best-matching spec.
//!
//! Inputs: `AgentSpec` values produced by `crate::library` or built-in fixtures.
//!
//! Outputs: `&AgentSpec` references via `get`, `Vec<&AgentSpec>` for list/find.
//!
//! Constraints:
//!   - Thread-safe via `Arc<RwLock<_>>` — safe to clone and share across tasks.
//!   - No execution engine references — registry is lookup-only.
//!   - The registry is loadable from an on-disk agent library directory.
//!
//! SPORT: cascade-agents / registry — E-P6-01

use std::collections::HashMap;
use std::path::Path;
use std::sync::{Arc, RwLock};

use thiserror::Error;
use tracing::debug;

use cascade_types::agent::Tier;

use crate::library::{load_spec_from_file, validate_agent_library, LibraryIssue};
use crate::spec::{AgentRole, AgentSpec, Capability};

// ── Error type ────────────────────────────────────────────────────────────────

/// Errors produced by registry operations.
#[derive(Debug, Error)]
pub enum RegistryError {
    #[error("agent '{id}' is already registered")]
    Duplicate { id: String },

    #[error("agent '{id}' not found")]
    NotFound { id: String },

    #[error("no agent found with capability '{capability:?}'")]
    NoCapableAgent { capability: Capability },

    #[error("no agent found for task kind '{kind}'")]
    NoRouteForKind { kind: String },

    #[error("library load error: {0}")]
    LibraryLoad(String),

    #[error("IO error loading library: {0}")]
    Io(#[from] std::io::Error),
}

// ── AgentRegistry ─────────────────────────────────────────────────────────────

/// Inner mutable state — not exported.
#[derive(Default)]
struct Inner {
    specs: HashMap<String, AgentSpec>,
}

/// Thread-safe registry of `AgentSpec` entries.
///
/// Clone is cheap — the registry wraps `Arc<RwLock<_>>`, so all clones share
/// the same underlying store.
///
/// # Example
/// ```rust
/// use cascade_agents::registry::AgentRegistry;
/// use cascade_agents::spec::{builtin_ceo, builtin_coder, Capability};
///
/// let reg = AgentRegistry::new();
/// reg.register(builtin_ceo()).unwrap();
/// reg.register(builtin_coder()).unwrap();
///
/// let spec = reg.get("cascade.ceo").unwrap();
/// assert_eq!(spec.name, "CEO Orchestrator");
///
/// let capable = reg.find_for(&Capability::CodeWrite);
/// assert!(!capable.is_empty());
/// ```
#[derive(Clone, Default)]
pub struct AgentRegistry {
    inner: Arc<RwLock<Inner>>,
}

impl AgentRegistry {
    /// Create an empty registry.
    pub fn new() -> Self {
        Self::default()
    }

    /// Create a registry pre-loaded with the built-in first-party specs.
    pub fn with_builtins() -> Self {
        let reg = Self::new();
        reg.register(crate::spec::builtin_ceo())
            .expect("builtin ceo");
        reg.register(crate::spec::builtin_coder())
            .expect("builtin coder");
        reg
    }

    /// Register an `AgentSpec`.
    ///
    /// # Errors
    /// Returns `RegistryError::Duplicate` if an agent with the same `id` is
    /// already registered.
    pub fn register(&self, spec: AgentSpec) -> Result<(), RegistryError> {
        let mut inner = self.inner.write().expect("registry poisoned");
        if inner.specs.contains_key(&spec.id) {
            return Err(RegistryError::Duplicate { id: spec.id });
        }
        debug!(agent_id = %spec.id, role = ?spec.role, "agent registered");
        inner.specs.insert(spec.id.clone(), spec);
        Ok(())
    }

    /// Remove an agent by id.
    ///
    /// # Errors
    /// Returns `RegistryError::NotFound` if the agent is not registered.
    pub fn remove(&self, id: &str) -> Result<AgentSpec, RegistryError> {
        let mut inner = self.inner.write().expect("registry poisoned");
        inner
            .specs
            .remove(id)
            .ok_or_else(|| RegistryError::NotFound { id: id.to_owned() })
    }

    /// Retrieve an agent by id.
    ///
    /// Returns a clone of the stored spec (avoids holding the lock).
    pub fn get(&self, id: &str) -> Option<AgentSpec> {
        let inner = self.inner.read().expect("registry poisoned");
        inner.specs.get(id).cloned()
    }

    /// List all registered agent specs.
    pub fn list(&self) -> Vec<AgentSpec> {
        let inner = self.inner.read().expect("registry poisoned");
        inner.specs.values().cloned().collect()
    }

    /// Returns all specs that declare `capability` in their capabilities list.
    ///
    /// The list is sorted by tier (T0 first) for deterministic ordering.
    pub fn find_for(&self, capability: &Capability) -> Vec<AgentSpec> {
        let inner = self.inner.read().expect("registry poisoned");
        let mut matches: Vec<AgentSpec> = inner
            .specs
            .values()
            .filter(|s| s.capabilities.contains(capability))
            .cloned()
            .collect();
        matches.sort_by_key(|s| s.tier as u8);
        matches
    }

    /// Route a `task_kind` string to the best-matched `AgentSpec`.
    ///
    /// Strategy:
    /// 1. Try `AgentRole::from_task_kind(kind)` to get a preferred role.
    /// 2. Return the first registered spec with that role (lowest tier wins).
    /// 3. If no role match, return `Generic` agents, then any agent.
    ///
    /// # Errors
    /// Returns `RegistryError::NoRouteForKind` when the registry is empty or
    /// no spec can be matched.
    pub fn route(&self, task_kind: &str) -> Result<AgentSpec, RegistryError> {
        let inner = self.inner.read().expect("registry poisoned");

        if let Some(role) = AgentRole::from_task_kind(task_kind) {
            if let Some(spec) = inner
                .specs
                .values()
                .filter(|s| s.role == role)
                .min_by_key(|s| s.tier as u8)
            {
                return Ok(spec.clone());
            }
        }

        // Fall back: prefer Generic, then anything
        if let Some(spec) = inner
            .specs
            .values()
            .filter(|s| s.role == AgentRole::Generic)
            .min_by_key(|s| s.tier as u8)
        {
            return Ok(spec.clone());
        }

        inner
            .specs
            .values()
            .min_by_key(|s| s.tier as u8)
            .cloned()
            .ok_or_else(|| RegistryError::NoRouteForKind {
                kind: task_kind.to_owned(),
            })
    }

    /// Load specs from an on-disk agent library directory and register them.
    ///
    /// Runs `validate_agent_library` first; if there are any hard errors, the
    /// load aborts and returns an error listing the issues.
    ///
    /// # Errors
    /// `RegistryError::LibraryLoad` if validation fails or a file cannot be parsed.
    pub fn load_from_library(&self, agents_dir: &Path) -> Result<Vec<LibraryIssue>, RegistryError> {
        let issues = validate_agent_library(agents_dir)
            .map_err(|e| RegistryError::LibraryLoad(e.to_string()))?;

        // Hard errors block loading; warnings are returned to caller.
        let hard: Vec<&LibraryIssue> = issues.iter().filter(|i| i.is_error()).collect();

        if !hard.is_empty() {
            let msgs: Vec<String> = hard.iter().map(|i| i.to_string()).collect();
            return Err(RegistryError::LibraryLoad(msgs.join("; ")));
        }

        // Walk agent YAML files and register
        if agents_dir.is_dir() {
            for entry in std::fs::read_dir(agents_dir)? {
                let entry = entry?;
                let path = entry.path();
                if path.extension().and_then(|e| e.to_str()) == Some("yaml") {
                    match load_spec_from_file(&path) {
                        Ok(spec) => {
                            // Ignore duplicate if already registered (idempotent reload)
                            let _ = self.register(spec);
                        }
                        Err(e) => {
                            return Err(RegistryError::LibraryLoad(format!(
                                "{}: {}",
                                path.display(),
                                e
                            )));
                        }
                    }
                }
            }
        }

        Ok(issues)
    }

    /// Load the default agent roster from `data_dir/agents/` and register all specs.
    ///
    /// If `project_override_dir` is `Some`, also loads TOML files from that directory
    /// and merges them: a project spec with the same `id` replaces the default spec.
    ///
    /// Duplicate ids (already registered) are silently skipped (idempotent reload).
    ///
    /// # Errors
    /// `RegistryError::LibraryLoad` if the TOML files cannot be parsed.
    pub fn load_default_roster(
        &self,
        data_dir: &Path,
        project_override_dir: Option<&Path>,
    ) -> Result<(), RegistryError> {
        use crate::roster::{load_roster, merge_overrides};

        let agents_dir = data_dir.join("agents");
        let mut specs = if agents_dir.exists() {
            load_roster(&agents_dir).map_err(|e| RegistryError::LibraryLoad(e.to_string()))?
        } else {
            vec![]
        };

        if let Some(override_dir) = project_override_dir {
            if override_dir.exists() {
                let overrides = load_roster(override_dir)
                    .map_err(|e| RegistryError::LibraryLoad(e.to_string()))?;
                specs = merge_overrides(specs, overrides);
            }
        }

        for spec in specs {
            let _ = self.register(spec); // ignore duplicate (idempotent)
        }

        Ok(())
    }

    /// Returns the default `ModelTier` for an `AgentRole` per the GCI routing table.
    ///
    /// Delegates to `AgentRole::default_tier` — kept here for registry-level
    /// ergonomics so callers do not need to import spec directly.
    pub fn tier_for_role(role: AgentRole) -> Tier {
        role.default_tier()
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::spec::{builtin_ceo, builtin_coder, Capability};

    fn populated() -> AgentRegistry {
        let reg = AgentRegistry::new();
        reg.register(builtin_ceo()).unwrap();
        reg.register(builtin_coder()).unwrap();
        reg
    }

    #[test]
    fn register_and_get() {
        let reg = populated();
        let spec = reg.get("cascade.ceo").expect("ceo should be present");
        assert_eq!(spec.name, "CEO Orchestrator");
    }

    #[test]
    fn register_duplicate_fails() {
        let reg = AgentRegistry::new();
        reg.register(builtin_ceo()).unwrap();
        let err = reg.register(builtin_ceo()).unwrap_err();
        assert!(matches!(err, RegistryError::Duplicate { .. }));
    }

    #[test]
    fn remove_existing() {
        let reg = populated();
        let removed = reg.remove("cascade.ceo").unwrap();
        assert_eq!(removed.id, "cascade.ceo");
        assert!(reg.get("cascade.ceo").is_none());
    }

    #[test]
    fn remove_nonexistent_errors() {
        let reg = AgentRegistry::new();
        let err = reg.remove("ghost").unwrap_err();
        assert!(matches!(err, RegistryError::NotFound { .. }));
    }

    #[test]
    fn list_all() {
        let reg = populated();
        let list = reg.list();
        assert_eq!(list.len(), 2);
    }

    #[test]
    fn find_for_capability_code_write() {
        let reg = populated();
        let found = reg.find_for(&Capability::CodeWrite);
        assert_eq!(found.len(), 1);
        assert_eq!(found[0].id, "cascade.coder");
    }

    #[test]
    fn find_for_capability_llm_call_returns_both() {
        let reg = populated();
        let found = reg.find_for(&Capability::LlmCall);
        // Both CEO and Coder declare LlmCall
        assert_eq!(found.len(), 2);
        // Sorted by tier — T1 (CEO) before T2 (Coder)
        assert_eq!(found[0].id, "cascade.ceo");
    }

    #[test]
    fn find_for_absent_capability_empty() {
        let reg = populated();
        let found = reg.find_for(&Capability::EmailDraft);
        assert!(found.is_empty());
    }

    #[test]
    fn route_code_write_returns_coder() {
        let reg = populated();
        let spec = reg.route("code.write").unwrap();
        assert_eq!(spec.role, AgentRole::Coder);
    }

    #[test]
    fn route_orchestrate_returns_ceo() {
        let reg = populated();
        let spec = reg.route("orchestrate").unwrap();
        assert_eq!(spec.role, AgentRole::Ceo);
    }

    #[test]
    fn route_unknown_kind_falls_back_to_lowest_tier() {
        let reg = populated();
        // No Generic agent; falls back to lowest-tier agent (T1 CEO)
        let spec = reg.route("something.unknown").unwrap();
        // Should return the T1 CEO as lowest tier fallback
        assert_eq!(spec.tier, cascade_types::agent::Tier::T1);
    }

    #[test]
    fn route_empty_registry_errors() {
        let reg = AgentRegistry::new();
        let err = reg.route("code.write").unwrap_err();
        assert!(matches!(err, RegistryError::NoRouteForKind { .. }));
    }

    #[test]
    fn with_builtins_has_two_agents() {
        let reg = AgentRegistry::with_builtins();
        assert_eq!(reg.list().len(), 2);
    }

    #[test]
    fn registry_is_clone_shares_state() {
        let reg = AgentRegistry::new();
        let reg2 = reg.clone();
        reg.register(builtin_ceo()).unwrap();
        // reg2 shares the same Arc
        assert!(reg2.get("cascade.ceo").is_some());
    }
}
