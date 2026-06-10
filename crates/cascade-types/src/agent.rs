//! Agent trait and associated types.
//!
//! An `Agent` is a discrete unit of autonomous work dispatched by the Cascade
//! orchestrator. The `Tier` enum encodes the model capability class (not any
//! specific model name) so agents remain vendor-neutral.

use crate::error::Result;
use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use std::collections::HashMap;

// ── Tier ─────────────────────────────────────────────────────────────────────

/// Agent capability tier.
///
/// Tiers are used only for internal dispatch and configuration; no vendor or
/// model name appears in public types. The `cascade-types` crate is fully
/// vendor-neutral.
///
/// | Tier | Role | Typical use |
/// |------|------|-------------|
/// | T0 | Observer / orchestrator | Plans, reviews, spawns child agents |
/// | T1 | Planner / decision-maker | Architecture decisions, final gates |
/// | T2 | Executor | Bulk code edits, research, ticket work |
/// | T3 | Cheap / triage | Classification, extraction, labeling |
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
pub enum Tier {
    /// Observer — orchestrates and reviews. Lowest per-call cost at this tier.
    T0,
    /// Planner — decisions and architecture review. Reserved for high-value turns.
    T1,
    /// Executor — bulk implementation and research. Primary workhorse.
    T2,
    /// Cheap — triage, classification, ≤10-line structured output.
    T3,
}

impl Tier {
    /// Returns a human-readable description.
    pub fn description(&self) -> &'static str {
        match self {
            Self::T0 => "Observer/orchestrator",
            Self::T1 => "Planner/decision-maker",
            Self::T2 => "Executor",
            Self::T3 => "Cheap/triage",
        }
    }
}

impl std::fmt::Display for Tier {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let s = match self {
            Self::T0 => "T0",
            Self::T1 => "T1",
            Self::T2 => "T2",
            Self::T3 => "T3",
        };
        f.write_str(s)
    }
}

// ── Context ──────────────────────────────────────────────────────────────────

/// The context object passed to every agent invocation.
///
/// Context carries the resolved cascade text, retrieval hits (if RAG is
/// enabled), and any structured metadata the dispatcher injected.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Context {
    /// The merged cascade instruction text for the current tier stack.
    /// This is the output of `cascade resolve` for the current working directory.
    pub cascade_text: String,

    /// Retrieved chunks from the RAG index. Empty when RAG is disabled.
    pub retrieval_hits: Vec<crate::retriever::RetrievalHit>,

    /// The tier this agent is dispatched at.
    pub tier: Tier,

    /// A unique identifier for this invocation, used for tracing and logging.
    pub invocation_id: String,

    /// Session-scoped key/value store. Agents may read values injected by
    /// the orchestrator (e.g. prior tool call results, user preferences).
    pub session: HashMap<String, serde_json::Value>,

    /// File-system working directory when the agent was dispatched.
    pub cwd: std::path::PathBuf,
}

// ── AgentResponse ─────────────────────────────────────────────────────────────

/// The structured response from an agent `handle` call.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AgentResponse {
    /// The primary text output of the agent.
    pub text: String,

    /// Structured output, if the agent was configured to return JSON.
    pub structured: Option<serde_json::Value>,

    /// Diagnostic metadata: token counts, latency, model info.
    pub meta: AgentMeta,

    /// Whether the agent signals that additional turns are needed.
    pub needs_continuation: bool,
}

/// Metadata attached to every agent response for observability.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct AgentMeta {
    /// Input token count (if the provider reported it).
    pub input_tokens: Option<u32>,

    /// Output token count (if the provider reported it).
    pub output_tokens: Option<u32>,

    /// Wall-clock latency in milliseconds.
    pub latency_ms: Option<u64>,

    /// Human-readable model identifier (vendor-neutral, e.g. "T2-executor").
    pub model_label: Option<String>,
}

// ── Trait ─────────────────────────────────────────────────────────────────────

/// Processes a prompt with a given context and returns a response.
///
/// Cascade agents are stateless per-invocation; any state needed across
/// turns is passed through `ctx.session`.
///
/// # Example
///
/// ```rust,no_run
/// # use cascade_types::agent::{Agent, Context, AgentResponse, AgentMeta, Tier};
/// # use cascade_types::error::Result;
/// # use async_trait::async_trait;
/// struct EchoAgent;
///
/// #[async_trait]
/// impl Agent for EchoAgent {
///     fn tier(&self) -> Tier { Tier::T3 }
///
///     async fn handle(&self, prompt: &str, ctx: &Context) -> Result<AgentResponse> {
///         Ok(AgentResponse {
///             text: format!("echo: {prompt}"),
///             structured: None,
///             meta: AgentMeta::default(),
///             needs_continuation: false,
///         })
///     }
/// }
/// ```
#[async_trait]
pub trait Agent: Send + Sync {
    /// The tier this agent operates at.
    fn tier(&self) -> Tier;

    /// Process `prompt` using `ctx` and return a response.
    ///
    /// # Errors
    ///
    /// Returns [`CascadeError::AgentFailed`] if the underlying model call
    /// fails, times out, or returns an unusable response.
    async fn handle(&self, prompt: &str, ctx: &Context) -> Result<AgentResponse>;

    /// A human-readable name for this agent (used in tracing and `cascade status`).
    fn name(&self) -> &str {
        "unknown-agent"
    }
}

// ── No-op implementation (for tests) ─────────────────────────────────────────

/// An agent that returns the prompt prefixed with `"[noop] "` for testing.
#[derive(Debug, Default)]
pub struct NoopAgent;

#[async_trait]
impl Agent for NoopAgent {
    fn tier(&self) -> Tier {
        Tier::T3
    }

    async fn handle(&self, prompt: &str, _ctx: &Context) -> Result<AgentResponse> {
        Ok(AgentResponse {
            text: format!("[noop] {prompt}"),
            structured: None,
            meta: AgentMeta::default(),
            needs_continuation: false,
        })
    }

    fn name(&self) -> &str {
        "noop"
    }
}

fn _assert_object_safe(_: &dyn Agent) {}
