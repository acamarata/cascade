//! Roster — tiered agent roster loaded from TOML files.
//!
//! Purpose: load default `data/agents/*.toml` files at runtime, merge with
//!   project-level overrides (project TOML wins), produce AgentSpec entries
//!   for the registry.
//!
//! Inputs:
//!   - `data/agents/` directory path (default roster).
//!   - Optional project-level TOML override path.
//!
//! Outputs:
//!   - `Vec<AgentSpec>` for registration.
//!   - `board_debate()` stub for CEO/CTO/Architect opinion collection.
//!
//! Constraints:
//!   - TOML format (not YAML — library.rs owns YAML).
//!   - Project TOML wins over default on `id` collision.
//!   - No async; pure functions only.
//!
//! SPORT: cascade-agents / roster — agents-01

use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};
use thiserror::Error;

use crate::registry::AgentRegistry;
use crate::spec::{AgentRole, AgentSpec, Capability, Runtime};
use cascade_types::agent::Tier;

// ── Error type ────────────────────────────────────────────────────────────────

/// Errors produced by roster loading.
#[derive(Debug, Error)]
pub enum RosterError {
    #[error("TOML parse error in {file}: {detail}")]
    Parse { file: PathBuf, detail: String },

    #[error("IO error reading {file}: {source}")]
    Io {
        file: PathBuf,
        #[source]
        source: std::io::Error,
    },

    #[error("no agents dir found at {0}")]
    DirNotFound(PathBuf),
}

// ── Raw TOML entry ────────────────────────────────────────────────────────────

/// Raw deserialized form of a single agent TOML file.
///
/// Maps 1:1 to an `AgentSpec` via `From<RawRosterEntry>`.
#[derive(Debug, Deserialize, Serialize)]
struct RawRosterEntry {
    id: String,
    version: String,
    name: String,
    role: AgentRole,
    tier: Tier,
    #[serde(default)]
    soul_ref: Option<String>,
    #[serde(default)]
    system_prompt_ref: Option<String>,
    #[serde(default)]
    tool_grants_ref: Option<String>,
    #[serde(default)]
    capabilities: Vec<Capability>,
    #[serde(default = "default_runtime")]
    runtime: Runtime,
}

fn default_runtime() -> Runtime {
    Runtime::Native
}

impl From<RawRosterEntry> for AgentSpec {
    fn from(r: RawRosterEntry) -> Self {
        AgentSpec {
            id: r.id,
            version: r.version,
            name: r.name,
            role: r.role,
            tier: r.tier,
            capabilities: r.capabilities,
            model_pref: None,
            system_prompt_ref: r.system_prompt_ref,
            tool_grants_ref: r.tool_grants_ref,
            runtime: r.runtime,
            soul_ref: r.soul_ref,
        }
    }
}

// ── Roster loading ────────────────────────────────────────────────────────────

/// Load all `.toml` files from `roster_dir` into `AgentSpec` values.
///
/// Files that cannot be parsed produce a `RosterError::Parse`. IO failures
/// produce `RosterError::Io`. If the directory does not exist, returns
/// `RosterError::DirNotFound`.
pub fn load_roster(roster_dir: &Path) -> Result<Vec<AgentSpec>, RosterError> {
    if !roster_dir.exists() {
        return Err(RosterError::DirNotFound(roster_dir.to_owned()));
    }

    let mut specs = Vec::new();

    let read_dir = std::fs::read_dir(roster_dir).map_err(|source| RosterError::Io {
        file: roster_dir.to_owned(),
        source,
    })?;

    for entry in read_dir {
        let entry = entry.map_err(|source| RosterError::Io {
            file: roster_dir.to_owned(),
            source,
        })?;
        let path = entry.path();

        if path.extension().and_then(|e| e.to_str()) != Some("toml") {
            continue;
        }

        let raw = std::fs::read_to_string(&path).map_err(|source| RosterError::Io {
            file: path.clone(),
            source,
        })?;

        let entry: RawRosterEntry =
            toml::from_str(&raw).map_err(|e| RosterError::Parse {
                file: path.clone(),
                detail: e.to_string(),
            })?;

        specs.push(AgentSpec::from(entry));
    }

    Ok(specs)
}

/// Load the default roster from `data_dir/agents/`.
///
/// Callers pass the path to the crate's `data/` directory. Returns an empty
/// vec (not an error) if the directory does not exist, so tests can skip
/// gracefully in CI environments that strip data files.
pub fn load_default_roster(data_dir: &Path) -> Result<Vec<AgentSpec>, RosterError> {
    let agents_dir = data_dir.join("agents");
    if !agents_dir.exists() {
        return Ok(vec![]);
    }
    load_roster(&agents_dir)
}

/// Merge project-level overrides into a base roster.
///
/// An override spec with the same `id` as a base spec replaces the base spec
/// entirely. New ids in `overrides` are appended.
pub fn merge_overrides(base: Vec<AgentSpec>, overrides: Vec<AgentSpec>) -> Vec<AgentSpec> {
    let mut result: Vec<AgentSpec> = base;

    for ov in overrides {
        if let Some(pos) = result.iter().position(|s| s.id == ov.id) {
            result[pos] = ov;
        } else {
            result.push(ov);
        }
    }

    result
}

// ── Board debate stub ─────────────────────────────────────────────────────────

/// A single opinion from a board-level agent.
#[derive(Debug, Clone)]
pub struct BoardOpinion {
    pub agent_id: String,
    pub role: AgentRole,
    /// Stance string, e.g. "approve", "reject", "abstain", "pending".
    pub stance: String,
    pub rationale: String,
}

/// Result of a board debate session.
#[derive(Debug, Clone)]
pub struct BoardDebateResult {
    pub topic: String,
    pub opinions: Vec<BoardOpinion>,
    /// Consensus string, populated once fleet-01 routing is implemented.
    pub consensus: Option<String>,
}

/// Board debate orchestrator stub.
///
/// Collects opinions from CEO, CTO, and Architect roles. Heavy routing logic
/// is deferred to fleet-01; this stub returns placeholder stances so the type
/// layer compiles and tests pass.
///
/// # TODO(fleet-01)
/// Route each board role to a live LLM via fleet routing and aggregate
/// responses into a real consensus.
pub fn board_debate(topic: &str, _registry: &AgentRegistry) -> BoardDebateResult {
    BoardDebateResult {
        topic: topic.to_owned(),
        opinions: vec![
            BoardOpinion {
                agent_id: "cascade.ceo".into(),
                role: AgentRole::Ceo,
                stance: "pending".into(),
                rationale: "Awaiting fleet-01 routing.".into(),
            },
            BoardOpinion {
                agent_id: "cascade.cto".into(),
                role: AgentRole::Cto,
                stance: "pending".into(),
                rationale: "Awaiting fleet-01 routing.".into(),
            },
            BoardOpinion {
                agent_id: "cascade.architect".into(),
                role: AgentRole::Architect,
                stance: "pending".into(),
                rationale: "Awaiting fleet-01 routing.".into(),
            },
        ],
        consensus: None,
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    fn agents_data_dir() -> PathBuf {
        PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join("data")
            .join("agents")
    }

    #[test]
    fn load_default_roster_succeeds() {
        let dir = agents_data_dir();
        if !dir.exists() {
            return; // skip if data dir missing in CI
        }
        let specs = load_roster(&dir).expect("default roster must load");
        assert!(!specs.is_empty(), "roster must have at least one agent");
    }

    #[test]
    fn ceo_spec_has_correct_tier() {
        let dir = agents_data_dir();
        if !dir.exists() {
            return;
        }
        let specs = load_roster(&dir).expect("roster loads");
        let ceo = specs.iter().find(|s| s.role == AgentRole::Ceo);
        assert!(ceo.is_some(), "CEO must be in roster");
        assert_eq!(ceo.unwrap().tier, Tier::T1);
    }

    #[test]
    fn merge_override_replaces_by_id() {
        let base_spec = AgentSpec {
            id: "cascade.ceo".into(),
            version: "1.0.0".into(),
            name: "CEO".into(),
            role: AgentRole::Ceo,
            tier: Tier::T1,
            capabilities: vec![],
            model_pref: None,
            system_prompt_ref: None,
            tool_grants_ref: None,
            runtime: Runtime::Native,
            soul_ref: Some("professional-minimal".into()),
        };
        let override_spec = AgentSpec {
            name: "Custom CEO".into(),
            soul_ref: Some("verbose-teacher".into()),
            ..base_spec.clone()
        };

        let merged = merge_overrides(vec![base_spec], vec![override_spec]);
        assert_eq!(merged.len(), 1);
        assert_eq!(merged[0].name, "Custom CEO");
        assert_eq!(merged[0].soul_ref.as_deref(), Some("verbose-teacher"));
    }

    #[test]
    fn board_debate_returns_three_opinions() {
        let reg = AgentRegistry::new();
        let result = board_debate("test topic", &reg);
        assert_eq!(result.opinions.len(), 3);
        assert_eq!(result.topic, "test topic");
        assert!(result.consensus.is_none());
    }

    #[test]
    fn role_default_tier_mapping() {
        assert_eq!(AgentRole::Board.default_tier(), Tier::T1);
        assert_eq!(AgentRole::Triage.default_tier(), Tier::T3);
        assert_eq!(AgentRole::Coder.default_tier(), Tier::T2);
        assert_eq!(AgentRole::Ceo.default_tier(), Tier::T1);
        assert_eq!(AgentRole::Cto.default_tier(), Tier::T1);
        assert_eq!(AgentRole::Generic.default_tier(), Tier::T3);
    }

    #[test]
    fn load_roster_missing_dir_returns_error() {
        let result = load_roster(Path::new("/nonexistent/path/agents"));
        assert!(matches!(result, Err(RosterError::DirNotFound(_))));
    }

    #[test]
    fn load_default_roster_missing_data_dir_returns_empty() {
        let result = load_default_roster(Path::new("/nonexistent/data"));
        assert!(result.is_ok());
        assert!(result.unwrap().is_empty());
    }

    #[test]
    fn merge_appends_new_ids() {
        let base_spec = AgentSpec {
            id: "cascade.ceo".into(),
            version: "1.0.0".into(),
            name: "CEO".into(),
            role: AgentRole::Ceo,
            tier: Tier::T1,
            capabilities: vec![],
            model_pref: None,
            system_prompt_ref: None,
            tool_grants_ref: None,
            runtime: Runtime::Native,
            soul_ref: None,
        };
        let new_spec = AgentSpec {
            id: "cascade.cto".into(),
            name: "CTO".into(),
            role: AgentRole::Cto,
            ..base_spec.clone()
        };
        let merged = merge_overrides(vec![base_spec], vec![new_spec]);
        assert_eq!(merged.len(), 2);
    }
}
