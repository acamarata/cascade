//! Roster — tiered agent roster loaded from TOML files.
//!
//! Purpose: load default `data/agents/*.toml` files at runtime, merge with
//!   project-level overrides (project TOML wins), produce AgentSpec entries
//!   for the registry.
//!
//! Inputs:
//!   - `data/agents/` directory path (default roster).
//!   - Optional project-level TOML override path.
//!   - A `&dyn BoardLlm` for `board_debate` calls.
//!
//! Outputs:
//!   - `Vec<AgentSpec>` for registration.
//!   - `board_debate()` — real LLM-backed opinion collection.
//!
//! Constraints:
//!   - TOML format (not YAML — library.rs owns YAML).
//!   - Project TOML wins over default on `id` collision.
//!   - `board_debate` is async (LLM calls); stance classification uses a simple
//!     keyword heuristic so consensus does not require an extra LLM round-trip.
//!
//! SPORT: cascade-agents / roster — agents-01

use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};
use thiserror::Error;

use crate::board_llm::BoardLlm;
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

// ── Board debate ──────────────────────────────────────────────────────────────

/// A single opinion from a board-level agent.
#[derive(Debug, Clone)]
pub struct BoardOpinion {
    pub agent_id: String,
    pub role: AgentRole,
    /// Stance string: "approve", "reject", "abstain", or "error".
    pub stance: String,
    pub rationale: String,
}

/// Result of a board debate session.
#[derive(Debug, Clone)]
pub struct BoardDebateResult {
    pub topic: String,
    pub opinions: Vec<BoardOpinion>,
    /// Majority-vote consensus: "approve", "reject", "abstain", or "split".
    pub consensus: Option<String>,
}

/// Classify raw LLM prose into a stance string.
///
/// Simple keyword heuristic — no extra LLM round-trip needed for classification.
fn classify_stance(text: &str) -> &'static str {
    let lower = text.to_lowercase();
    let approve_hits = lower.contains("approve")
        || lower.contains("support")
        || lower.contains("agree")
        || lower.contains("yes");
    let reject_hits = lower.contains("reject")
        || lower.contains("against")
        || lower.contains("oppose")
        || lower.contains("no ");
    match (approve_hits, reject_hits) {
        (true, false) => "approve",
        (false, true) => "reject",
        _ => "abstain",
    }
}

/// Derive consensus from a slice of stance strings via majority vote.
fn consensus_from_stances(stances: &[&str]) -> String {
    let mut approve = 0usize;
    let mut reject = 0usize;
    let mut abstain = 0usize;

    for &s in stances {
        match s {
            "approve" => approve += 1,
            "reject" => reject += 1,
            _ => abstain += 1, // "abstain" or "error"
        }
    }

    if approve > reject && approve > abstain {
        "approve".to_owned()
    } else if reject > approve && reject > abstain {
        "reject".to_owned()
    } else if abstain > approve && abstain > reject {
        "abstain".to_owned()
    } else {
        "split".to_owned()
    }
}

/// Board debate orchestrator — collects real LLM opinions from CEO, CTO, and
/// Architect roles and derives a majority-vote consensus.
///
/// Each role receives a brief system prompt establishing its persona. On LLM
/// failure, the opinion records `stance: "error"` with the error as rationale.
/// Failures count as "abstain" for consensus purposes.
pub async fn board_debate(
    topic: &str,
    _registry: &AgentRegistry,
    llm: &dyn BoardLlm,
) -> BoardDebateResult {
    const BOARD: &[(&str, AgentRole, &str)] = &[
        (
            "cascade.ceo",
            AgentRole::Ceo,
            "You are the CEO of Cascade. Evaluate the following topic from a strategic, \
             business, and stakeholder-impact perspective. State clearly whether you approve, \
             reject, or abstain, and give your reasoning concisely.",
        ),
        (
            "cascade.cto",
            AgentRole::Cto,
            "You are the CTO of Cascade. Evaluate the following topic from a technical \
             feasibility, architecture, and engineering-risk perspective. State clearly whether \
             you approve, reject, or abstain, and give your reasoning concisely.",
        ),
        (
            "cascade.architect",
            AgentRole::Architect,
            "You are the Lead Architect of Cascade. Evaluate the following topic from a \
             system-design, scalability, and maintainability perspective. State clearly whether \
             you approve, reject, or abstain, and give your reasoning concisely.",
        ),
    ];

    let mut opinions = Vec::with_capacity(BOARD.len());

    for &(agent_id, role, system) in BOARD {
        let (stance, rationale) = match llm.opine(&role, topic, system).await {
            Ok(text) => {
                let stance = classify_stance(&text).to_owned();
                (stance, text)
            }
            Err(e) => ("error".to_owned(), e),
        };
        opinions.push(BoardOpinion {
            agent_id: agent_id.to_owned(),
            role,
            stance,
            rationale,
        });
    }

    let stance_refs: Vec<&str> = opinions.iter().map(|o| o.stance.as_str()).collect();
    let consensus = Some(consensus_from_stances(&stance_refs));

    BoardDebateResult {
        topic: topic.to_owned(),
        opinions,
        consensus,
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use async_trait::async_trait;

    // ── MockBoardLlm ──────────────────────────────────────────────────────────

    /// Scripted mock: returns a fixed response string per role.
    struct MockBoardLlm {
        ceo_response: String,
        cto_response: String,
        architect_response: String,
    }

    impl MockBoardLlm {
        fn new(ceo: &str, cto: &str, architect: &str) -> Self {
            Self {
                ceo_response: ceo.to_owned(),
                cto_response: cto.to_owned(),
                architect_response: architect.to_owned(),
            }
        }
    }

    #[async_trait]
    impl BoardLlm for MockBoardLlm {
        async fn opine(
            &self,
            role: &AgentRole,
            _topic: &str,
            _system: &str,
        ) -> Result<String, String> {
            match role {
                AgentRole::Ceo => Ok(self.ceo_response.clone()),
                AgentRole::Cto => Ok(self.cto_response.clone()),
                AgentRole::Architect => Ok(self.architect_response.clone()),
                _ => Ok("abstain — not a board role".to_owned()),
            }
        }
    }

    // ── Stance classifier ─────────────────────────────────────────────────────

    #[test]
    fn classify_approve_keywords() {
        assert_eq!(classify_stance("I approve this proposal fully."), "approve");
        assert_eq!(classify_stance("I support the initiative."), "approve");
        assert_eq!(classify_stance("Yes, let's proceed."), "approve");
        assert_eq!(classify_stance("I agree with the direction."), "approve");
    }

    #[test]
    fn classify_reject_keywords() {
        assert_eq!(classify_stance("I reject this outright."), "reject");
        assert_eq!(classify_stance("I am against this change."), "reject");
        assert_eq!(classify_stance("I oppose the proposal."), "reject");
    }

    #[test]
    fn classify_abstain_when_ambiguous() {
        assert_eq!(classify_stance("This is a complex situation."), "abstain");
        assert_eq!(
            classify_stance("I approve some parts but reject others."),
            "abstain"
        );
    }

    // ── Consensus calculation ─────────────────────────────────────────────────

    #[test]
    fn consensus_majority_approve() {
        let stances = ["approve", "approve", "reject"];
        assert_eq!(consensus_from_stances(&stances), "approve");
    }

    #[test]
    fn consensus_majority_reject() {
        let stances = ["reject", "reject", "approve"];
        assert_eq!(consensus_from_stances(&stances), "reject");
    }

    #[test]
    fn consensus_split_on_tie() {
        let stances = ["approve", "reject", "abstain"];
        assert_eq!(consensus_from_stances(&stances), "split");
    }

    // ── board_debate with MockBoardLlm ────────────────────────────────────────

    #[tokio::test]
    async fn board_debate_uses_real_stances_not_pending() {
        let llm = MockBoardLlm::new(
            "I approve this initiative — it aligns with our strategic goals.",
            "I approve from a technical standpoint; the architecture is sound.",
            "I reject this; the design introduces too much coupling.",
        );
        let reg = AgentRegistry::new();
        let result = board_debate("adopt microservices", &reg, &llm).await;

        assert_eq!(result.opinions.len(), 3);
        assert_eq!(result.topic, "adopt microservices");

        // No opinion may be "pending"
        for op in &result.opinions {
            assert_ne!(op.stance, "pending", "stance must not be 'pending'");
            assert_ne!(
                op.rationale, "Awaiting fleet-01 routing.",
                "rationale must not be the old stub text"
            );
        }

        let ceo_op = result.opinions.iter().find(|o| o.role == AgentRole::Ceo).unwrap();
        assert_eq!(ceo_op.stance, "approve");

        let arch_op = result
            .opinions
            .iter()
            .find(|o| o.role == AgentRole::Architect)
            .unwrap();
        assert_eq!(arch_op.stance, "reject");

        // Consensus must be present
        assert!(result.consensus.is_some(), "consensus must be Some");
        // 2 approve, 1 reject → consensus = approve
        assert_eq!(result.consensus.as_deref(), Some("approve"));
    }

    #[tokio::test]
    async fn board_debate_unanimous_approve_consensus() {
        let llm = MockBoardLlm::new(
            "I approve.",
            "I approve and support this fully.",
            "I agree and approve.",
        );
        let reg = AgentRegistry::new();
        let result = board_debate("ship v2", &reg, &llm).await;
        assert_eq!(result.consensus.as_deref(), Some("approve"));
    }

    // ── NoopBoardLlm ──────────────────────────────────────────────────────────

    #[tokio::test]
    async fn noop_board_llm_yields_error_stances() {
        use crate::board_llm::NoopBoardLlm;

        let reg = AgentRegistry::new();
        let result = board_debate("any topic", &reg, &NoopBoardLlm).await;

        assert_eq!(result.opinions.len(), 3);
        for op in &result.opinions {
            assert_eq!(
                op.stance, "error",
                "NoopBoardLlm must produce error stances, got: {}",
                op.stance
            );
            assert!(
                op.rationale.contains("no LLM provider configured"),
                "rationale must explain the error, got: {}",
                op.rationale
            );
        }
        // consensus is still Some (errors counted as abstain → majority abstain)
        assert!(result.consensus.is_some());
    }

    // ── Original tests preserved ──────────────────────────────────────────────

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
