//! AgentSpec — the descriptor for a single agent in the Cascade runtime.
//!
//! Purpose: define the immutable identity contract for an agent: its role,
//!   capability tags, tier mapping, model preference, system prompt reference,
//!   tool grant reference, and runtime (native or WASM plugin).
//!
//! Inputs: constructed programmatically or deserialized from on-disk YAML.
//!
//! Outputs: used by `AgentRegistry` for routing and capability matching.
//!
//! Constraints:
//!   - Serde uses camelCase; enum variants are struct-style (repo rule).
//!   - No execution engine references — this is the type layer only.
//!   - Must be `Clone + Send + Sync` for the registry's `Arc<RwLock<_>>` store.
//!
//! SPORT: cascade-agents / spec — E-P6-01

use cascade_types::agent::Tier;
use serde::{Deserialize, Serialize};

// ── Capability tags ───────────────────────────────────────────────────────────

/// A fine-grained capability tag carried by an `AgentSpec`.
///
/// Capability strings follow a `domain.verb` convention. The `Custom` variant
/// allows first- and third-party agents to declare arbitrary capabilities
/// without modifying this enum.
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub enum Capability {
    /// Read source files, directory listings.
    #[serde(rename = "code.read")]
    CodeRead,
    /// Produce or edit source code.
    #[serde(rename = "code.write")]
    CodeWrite,
    /// Execute or compile code.
    #[serde(rename = "code.execute")]
    CodeExecute,
    /// Full-text and semantic search across the corpus.
    #[serde(rename = "search")]
    Search,
    /// Call an LLM provider for completions.
    #[serde(rename = "llm.call")]
    LlmCall,
    /// Draft an outbound email (requires Outbound approval).
    #[serde(rename = "email.draft")]
    EmailDraft,
    /// Open and interact with browser tabs.
    #[serde(rename = "browser")]
    Browser,
    /// Create, read, update, or close issues/tickets.
    #[serde(rename = "issue.manage")]
    IssueManage,
    /// Review a pull request diff and produce comments.
    #[serde(rename = "pr.review")]
    PrReview,
    /// Summarise or analyse documents.
    #[serde(rename = "doc.analyze")]
    DocAnalyze,
    /// Write or update documentation.
    #[serde(rename = "doc.write")]
    DocWrite,
    /// Classify, triage, or label items.
    #[serde(rename = "triage")]
    Triage,
    /// Any capability not covered by the enum variants above.
    Custom(String),
}

// ── AgentRole ─────────────────────────────────────────────────────────────────

/// The functional role of an agent in the Cascade orchestration hierarchy.
///
/// Roles are used by the `AgentRegistry` routing table: a dispatched task
/// carries a `task_kind` string, and `route(task_kind)` returns the best-
/// matched `AgentSpec`. New roles can be added here without breaking existing
/// specs — specs carry the role as a field.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub enum AgentRole {
    /// Top-level orchestrator; plans, reviews, spawns child agents.
    Ceo,
    /// Architecture and system design decisions.
    Architect,
    /// Bulk code writing and editing.
    Coder,
    /// Code and PR review.
    Reviewer,
    /// Web and document research.
    Researcher,
    /// Documentation authoring.
    DocWriter,
    /// Classification and triage.
    Triage,
    /// Customer service interactions.
    CustomerService,
    /// Outbound email drafting (requires approval).
    Emailer,
    /// No specialisation — generic task execution.
    Generic,
}

impl AgentRole {
    /// Maps a task-kind hint to the preferred `AgentRole`.
    ///
    /// This is a **best-effort** match; the registry may override via
    /// explicit route entries. Returns `None` if no role maps to this kind.
    pub fn from_task_kind(kind: &str) -> Option<Self> {
        match kind {
            "code.write" | "code.edit" | "code.refactor" => Some(Self::Coder),
            "code.review" | "pr.review" => Some(Self::Reviewer),
            "doc.write" | "doc.update" => Some(Self::DocWriter),
            "research" | "search" => Some(Self::Researcher),
            "triage" | "classify" | "label" => Some(Self::Triage),
            "orchestrate" | "plan" => Some(Self::Ceo),
            "email.draft" | "email.send" => Some(Self::Emailer),
            "customer.service" => Some(Self::CustomerService),
            _ => None,
        }
    }
}

// ── Runtime ──────────────────────────────────────────────────────────────────

/// The execution environment for an agent.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", tag = "kind")]
pub enum Runtime {
    /// Agent is a native Rust struct registered in-process.
    Native,
    /// Agent is a WASM plugin loaded by `cascade-plugins`.
    Wasm {
        /// The plugin ID as registered with the WASM plugin host.
        plugin_id: String,
    },
}

// ── AgentSpec ─────────────────────────────────────────────────────────────────

/// The immutable descriptor for a Cascade agent.
///
/// An `AgentSpec` identifies an agent, its capability surface, routing role,
/// preferred model tier, and execution runtime. It is produced from the on-disk
/// agent library YAML (see `crate::library`) or constructed programmatically.
///
/// # serde contract
/// - All field names serialize as `camelCase`.
/// - Enum variants use struct style (no newtype or unit implicit forms).
///
/// # Example
/// ```rust
/// use cascade_agents::spec::{AgentSpec, AgentRole, Capability, Runtime};
/// use cascade_types::agent::Tier;
///
/// let spec = AgentSpec {
///     id: "ceo-01".into(),
///     version: "1.0.0".into(),
///     name: "CEO".into(),
///     role: AgentRole::Ceo,
///     tier: Tier::T1,
///     capabilities: vec![Capability::LlmCall, Capability::Search],
///     model_pref: None,
///     system_prompt_ref: None,
///     tool_grants_ref: None,
///     runtime: Runtime::Native,
/// };
/// assert_eq!(spec.role, AgentRole::Ceo);
/// ```
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AgentSpec {
    /// Stable, unique identifier (e.g. `"ceo-01"`, `"coder-wasm"`).
    pub id: String,

    /// SemVer string for this spec (e.g. `"1.0.0"`).
    pub version: String,

    /// Human-readable display name.
    pub name: String,

    /// Functional role used for routing.
    pub role: AgentRole,

    /// Capability tier (maps to `cascade_types::agent::Tier`).
    pub tier: Tier,

    /// Capability tags this agent declares.
    #[serde(default)]
    pub capabilities: Vec<Capability>,

    /// Optional model preference (opaque string, e.g. `"claude-3-5-sonnet"`).
    /// If `None`, the runtime selects based on `tier`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub model_pref: Option<String>,

    /// Optional reference to a system prompt in the agent library
    /// (e.g. `"library/prompts/ceo.md"`).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub system_prompt_ref: Option<String>,

    /// Optional reference to a tool-grant set in the agent library
    /// (e.g. `"library/grants/ceo.yaml"`).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub tool_grants_ref: Option<String>,

    /// Execution runtime for this agent.
    pub runtime: Runtime,
}

// ── First-party fixtures ──────────────────────────────────────────────────────

/// Returns the built-in `AgentSpec` for the CEO orchestrator.
pub fn builtin_ceo() -> AgentSpec {
    AgentSpec {
        id: "cascade.ceo".into(),
        version: "1.0.0".into(),
        name: "CEO Orchestrator".into(),
        role: AgentRole::Ceo,
        tier: Tier::T1,
        capabilities: vec![
            Capability::LlmCall,
            Capability::Search,
            Capability::CodeRead,
        ],
        model_pref: None,
        system_prompt_ref: Some("library/prompts/ceo.md".into()),
        tool_grants_ref: Some("library/grants/ceo.yaml".into()),
        runtime: Runtime::Native,
    }
}

/// Returns the built-in `AgentSpec` for the primary coder agent.
pub fn builtin_coder() -> AgentSpec {
    AgentSpec {
        id: "cascade.coder".into(),
        version: "1.0.0".into(),
        name: "Coder".into(),
        role: AgentRole::Coder,
        tier: Tier::T2,
        capabilities: vec![
            Capability::CodeRead,
            Capability::CodeWrite,
            Capability::LlmCall,
            Capability::Search,
        ],
        model_pref: None,
        system_prompt_ref: Some("library/prompts/coder.md".into()),
        tool_grants_ref: Some("library/grants/coder.yaml".into()),
        runtime: Runtime::Native,
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn agent_spec_serde_camel_case_round_trip() {
        let spec = builtin_ceo();
        let json = serde_json::to_string(&spec).unwrap();

        // camelCase fields must appear in the JSON
        assert!(
            json.contains("\"systemPromptRef\""),
            "expected camelCase systemPromptRef, got: {json}"
        );
        assert!(
            json.contains("\"toolGrantsRef\""),
            "expected camelCase toolGrantsRef"
        );
        assert!(
            json.contains("\"modelPref\"") || !json.contains("modelPref"),
            "unexpected field"
        );

        let decoded: AgentSpec = serde_json::from_str(&json).unwrap();
        assert_eq!(decoded.id, "cascade.ceo");
        assert_eq!(decoded.role, AgentRole::Ceo);
        assert_eq!(decoded.tier, Tier::T1);
        assert!(decoded.capabilities.contains(&Capability::LlmCall));
    }

    #[test]
    fn coder_spec_serde_round_trip() {
        let spec = builtin_coder();
        let json = serde_json::to_string(&spec).unwrap();
        let decoded: AgentSpec = serde_json::from_str(&json).unwrap();
        assert_eq!(decoded.role, AgentRole::Coder);
        assert_eq!(decoded.tier, Tier::T2);
        assert!(decoded.capabilities.contains(&Capability::CodeWrite));
    }

    #[test]
    fn runtime_wasm_serde() {
        let rt = Runtime::Wasm {
            plugin_id: "my-plugin".into(),
        };
        let json = serde_json::to_string(&rt).unwrap();
        let decoded: Runtime = serde_json::from_str(&json).unwrap();
        assert_eq!(decoded, rt);
    }

    #[test]
    fn role_from_task_kind_known() {
        assert_eq!(
            AgentRole::from_task_kind("code.write"),
            Some(AgentRole::Coder)
        );
        assert_eq!(
            AgentRole::from_task_kind("pr.review"),
            Some(AgentRole::Reviewer)
        );
        assert_eq!(AgentRole::from_task_kind("triage"), Some(AgentRole::Triage));
        assert_eq!(
            AgentRole::from_task_kind("email.draft"),
            Some(AgentRole::Emailer)
        );
    }

    #[test]
    fn role_from_task_kind_unknown() {
        assert_eq!(AgentRole::from_task_kind("unknown.kind"), None);
    }

    #[test]
    fn capability_custom_serde() {
        let cap = Capability::Custom("telemetry.export".into());
        let json = serde_json::to_string(&cap).unwrap();
        let decoded: Capability = serde_json::from_str(&json).unwrap();
        assert_eq!(decoded, cap);
    }
}
