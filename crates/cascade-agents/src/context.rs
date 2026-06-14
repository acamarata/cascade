//! AgentRunContext — assembles what an agent sees for one task.
//!
//! Purpose: build the per-task execution envelope from an `AgentSpec` + task goal
//!   + conversation history + granted tools + injectable RAG context.
//!
//! Inputs: `AgentSpec`, goal string, messages, `ToolRegistry`, optional
//!   `ContextProvider` impl, session id.
//!
//! Outputs: `AgentRunContext` — the complete runtime view passed into `run_agent_step`.
//!
//! Constraints:
//!   - `ContextProvider` is an injectable trait; `NoopContextProvider` is the
//!     built-in no-op so callers that don't need RAG pay nothing.
//!   - `StepOutcome` is defined here (shared with executor.rs).
//!   - No live I/O in the context assembly path — all I/O is in the provider call.
//!
//! SPORT: cascade-agents / context — E-P6-03

use std::sync::Arc;

use async_trait::async_trait;
use serde::{Deserialize, Serialize};

use crate::spec::AgentSpec;
use crate::task::AgentTask;
use crate::tool_registry::{ToolDescriptor, ToolRegistry};

// ── ContextProvider trait ─────────────────────────────────────────────────────

/// Injectable RAG / memory context provider.
///
/// Implement this trait to plug in a vector-search retriever or other
/// memory source. The executor calls `retrieve(goal)` once per task before
/// the first step, and the results are attached to `AgentRunContext`.
///
/// # Safety
/// Must be `Send + Sync` — contexts are shared across async task workers.
#[async_trait]
pub trait ContextProvider: Send + Sync {
    /// Retrieve relevant context strings for the given `goal`.
    ///
    /// Returns an empty `Vec` on error — callers treat missing context as a
    /// soft degradation, not a hard failure.
    async fn retrieve(&self, goal: &str) -> Vec<String>;
}

// ── NoopContextProvider ───────────────────────────────────────────────────────

/// A no-op `ContextProvider` that always returns an empty context.
///
/// Used when RAG is not wired or not needed for a task.
pub struct NoopContextProvider;

#[async_trait]
impl ContextProvider for NoopContextProvider {
    async fn retrieve(&self, _goal: &str) -> Vec<String> {
        vec![]
    }
}

// ── ToolCall ──────────────────────────────────────────────────────────────────

/// A tool call requested by the model in one step of the ReAct loop.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ToolCall {
    /// The tool id (matches `ToolDescriptor::id`).
    pub tool_id: String,
    /// Structured arguments for the tool, as produced by the model.
    pub args: serde_json::Value,
    /// A call-site identifier assigned by the model for correlation in
    /// multi-call steps (mirrors the OpenAI `id` / Anthropic `toolUseId`).
    pub call_id: String,
}

// ── ToolResult ────────────────────────────────────────────────────────────────

/// The result of executing one `ToolCall`.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ToolResult {
    /// Correlates back to `ToolCall::call_id`.
    pub call_id: String,
    /// Tool output, serialised to a string for injection into the next message.
    pub output: String,
    /// Whether the tool call returned an error.
    pub is_error: bool,
}

// ── StepOutcome ───────────────────────────────────────────────────────────────

/// The outcome of one `run_agent_step` call.
///
/// The executor inspects `done` and `tool_calls` to decide whether to loop,
/// park for approval, or complete the task.
#[derive(Debug, Clone)]
pub struct StepOutcome {
    /// The assistant's text output for this step (may be empty if all output
    /// is in tool calls).
    pub assistant_text: String,
    /// Tool calls the model requested (empty when `done == true`).
    pub tool_calls: Vec<ToolCall>,
    /// Whether the model signalled end-of-task (no more tool calls, final answer).
    pub done: bool,
    /// Token usage for this step (prompt + completion).
    pub usage: TokenUsage,
}

/// Token usage for one step.
#[derive(Debug, Clone, Default)]
pub struct TokenUsage {
    /// Tokens in the input prompt (including system + messages + tool descriptors).
    pub prompt_tokens: u32,
    /// Tokens generated in this step.
    pub completion_tokens: u32,
}

impl TokenUsage {
    /// Total tokens consumed.
    pub fn total(&self) -> u32 {
        self.prompt_tokens + self.completion_tokens
    }
}

// ── AgentRunContext ───────────────────────────────────────────────────────────

/// The complete runtime view assembled for one task execution.
///
/// `AgentRunContext` is built by `AgentRunContextBuilder` and passed to
/// `run_agent_step`. It carries everything an agent needs without further I/O.
#[derive(Debug)]
pub struct AgentRunContext {
    /// The spec of the agent that will handle this task.
    pub spec: AgentSpec,
    /// The goal text for this task.
    pub goal: String,
    /// Parent task chain (newest ancestor first).
    pub parent_chain: Vec<AgentTask>,
    /// Full conversation so far (system prompt is prepended by the executor).
    pub messages: Vec<crate::context::ContextMessage>,
    /// Tools the agent is allowed to call.
    pub granted_tools: Vec<ToolDescriptor>,
    /// Retrieved RAG / memory snippets (may be empty).
    pub memory_context: Vec<String>,
    /// Unique session identifier — stable across steps of the same task.
    pub session_id: String,
}

/// A single message in the assembled conversation history.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ContextMessage {
    /// Role of the message author.
    pub role: ContextRole,
    /// Text content.
    pub content: String,
}

/// Role of a message author in the assembled context.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub enum ContextRole {
    /// System instruction (system prompt from the spec).
    System,
    /// User / orchestrator turn.
    User,
    /// Model assistant turn.
    Assistant,
    /// Tool result injected back into the conversation.
    Tool,
}

// ── AgentRunContextBuilder ────────────────────────────────────────────────────

/// Builder for `AgentRunContext`.
///
/// # Example
/// ```rust,ignore
/// use cascade_agents::context::{AgentRunContextBuilder, NoopContextProvider};
/// use cascade_agents::spec::builtin_ceo;
/// use cascade_agents::tool_registry::ToolRegistry;
/// use std::sync::Arc;
///
/// let ctx = AgentRunContextBuilder::new(builtin_ceo(), "summarise the codebase")
///     .tool_registry(Arc::new(ToolRegistry::new()), "cascade.ceo")
///     .context_provider(Arc::new(NoopContextProvider))
///     .session_id("sess-01")
///     .build()
///     .await;
/// assert_eq!(ctx.goal, "summarise the codebase");
/// ```
pub struct AgentRunContextBuilder {
    spec: AgentSpec,
    goal: String,
    parent_chain: Vec<AgentTask>,
    messages: Vec<ContextMessage>,
    tool_registry: Option<(Arc<ToolRegistry>, String)>,
    context_provider: Option<Arc<dyn ContextProvider>>,
    session_id: Option<String>,
}

impl AgentRunContextBuilder {
    /// Start building a context for `spec` and `goal`.
    pub fn new(spec: AgentSpec, goal: impl Into<String>) -> Self {
        Self {
            spec,
            goal: goal.into(),
            parent_chain: vec![],
            messages: vec![],
            tool_registry: None,
            context_provider: None,
            session_id: None,
        }
    }

    /// Attach the parent task chain (oldest first; builder reverses to newest-first).
    pub fn parent_chain(mut self, chain: Vec<AgentTask>) -> Self {
        self.parent_chain = chain;
        self
    }

    /// Append an existing conversation history.
    pub fn messages(mut self, messages: Vec<ContextMessage>) -> Self {
        self.messages = messages;
        self
    }

    /// Supply a `ToolRegistry` and agent id for `tools_for` resolution.
    pub fn tool_registry(
        mut self,
        registry: Arc<ToolRegistry>,
        agent_id: impl Into<String>,
    ) -> Self {
        self.tool_registry = Some((registry, agent_id.into()));
        self
    }

    /// Inject a `ContextProvider` implementation (RAG or memory).
    pub fn context_provider(mut self, provider: Arc<dyn ContextProvider>) -> Self {
        self.context_provider = Some(provider);
        self
    }

    /// Override the session id (default: a new UUIDv4).
    pub fn session_id(mut self, id: impl Into<String>) -> Self {
        self.session_id = Some(id.into());
        self
    }

    /// Assemble and return the `AgentRunContext`.
    ///
    /// Calls `ContextProvider::retrieve` once if a provider is set.
    pub async fn build(self) -> AgentRunContext {
        // Resolve granted tools
        let granted_tools = if let Some((registry, agent_id)) = &self.tool_registry {
            registry.tools_for(agent_id)
        } else {
            vec![]
        };

        // Retrieve memory / RAG context
        let memory_context = if let Some(provider) = &self.context_provider {
            provider.retrieve(&self.goal).await
        } else {
            vec![]
        };

        // Prepend system prompt from spec if present and not already set
        let mut messages = self.messages;
        if !messages.iter().any(|m| m.role == ContextRole::System) {
            if let Some(ref prompt_ref) = self.spec.system_prompt_ref {
                messages.insert(
                    0,
                    ContextMessage {
                        role: ContextRole::System,
                        content: format!("[system prompt ref: {prompt_ref}]"),
                    },
                );
            }
        }

        let session_id = self
            .session_id
            .unwrap_or_else(|| uuid::Uuid::new_v4().to_string());

        AgentRunContext {
            spec: self.spec,
            goal: self.goal,
            parent_chain: self.parent_chain,
            messages,
            granted_tools,
            memory_context,
            session_id,
        }
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::grants::{AccessLevel, ToolGrant};
    use crate::spec::{builtin_ceo, builtin_coder};
    use crate::tool_registry::{ToolDescriptor, ToolRegistry};

    fn search_tool() -> ToolDescriptor {
        ToolDescriptor {
            id: "cascade.search".into(),
            name: "Search".into(),
            description: "Semantic search".into(),
            required_level: AccessLevel::Search,
        }
    }

    #[tokio::test]
    async fn builder_defaults_work() {
        let ctx = AgentRunContextBuilder::new(builtin_ceo(), "write tests")
            .build()
            .await;
        assert_eq!(ctx.goal, "write tests");
        assert!(ctx.granted_tools.is_empty());
        assert!(ctx.memory_context.is_empty());
        assert!(!ctx.session_id.is_empty());
    }

    #[tokio::test]
    async fn builder_resolves_granted_tools() {
        let registry = Arc::new(ToolRegistry::new());
        registry.register_tool(search_tool()).unwrap();
        registry.set_grants(
            "cascade.ceo",
            vec![ToolGrant {
                tool_id: "cascade.search".into(),
                level: AccessLevel::Search,
                approved: false,
            }],
        );

        let ctx = AgentRunContextBuilder::new(builtin_ceo(), "search code")
            .tool_registry(registry, "cascade.ceo")
            .build()
            .await;

        assert_eq!(ctx.granted_tools.len(), 1);
        assert_eq!(ctx.granted_tools[0].id, "cascade.search");
    }

    #[tokio::test]
    async fn noop_provider_returns_empty_context() {
        let ctx = AgentRunContextBuilder::new(builtin_coder(), "goal")
            .context_provider(Arc::new(NoopContextProvider))
            .build()
            .await;
        assert!(ctx.memory_context.is_empty());
    }

    struct EchoProvider(String);

    #[async_trait]
    impl ContextProvider for EchoProvider {
        async fn retrieve(&self, _goal: &str) -> Vec<String> {
            vec![self.0.clone()]
        }
    }

    #[tokio::test]
    async fn custom_provider_injects_context() {
        let ctx = AgentRunContextBuilder::new(builtin_ceo(), "find auth logic")
            .context_provider(Arc::new(EchoProvider("auth.rs snippet".into())))
            .build()
            .await;
        assert_eq!(ctx.memory_context, vec!["auth.rs snippet"]);
    }

    #[tokio::test]
    async fn system_prompt_ref_prepended_as_system_message() {
        // builtin_ceo has system_prompt_ref = Some("library/prompts/ceo.md")
        let ctx = AgentRunContextBuilder::new(builtin_ceo(), "plan the sprint")
            .build()
            .await;
        assert!(!ctx.messages.is_empty());
        assert_eq!(ctx.messages[0].role, ContextRole::System);
        assert!(ctx.messages[0].content.contains("ceo.md"));
    }

    #[tokio::test]
    async fn existing_system_message_not_duplicated() {
        let existing = vec![ContextMessage {
            role: ContextRole::System,
            content: "custom system".into(),
        }];
        let ctx = AgentRunContextBuilder::new(builtin_ceo(), "task")
            .messages(existing)
            .build()
            .await;
        let system_msgs: Vec<_> = ctx
            .messages
            .iter()
            .filter(|m| m.role == ContextRole::System)
            .collect();
        assert_eq!(system_msgs.len(), 1);
        assert_eq!(system_msgs[0].content, "custom system");
    }

    #[tokio::test]
    async fn session_id_override_works() {
        let ctx = AgentRunContextBuilder::new(builtin_ceo(), "g")
            .session_id("my-session-123")
            .build()
            .await;
        assert_eq!(ctx.session_id, "my-session-123");
    }

    #[test]
    fn step_outcome_done_flag() {
        let outcome = StepOutcome {
            assistant_text: "final answer".into(),
            tool_calls: vec![],
            done: true,
            usage: TokenUsage {
                prompt_tokens: 10,
                completion_tokens: 5,
            },
        };
        assert!(outcome.done);
        assert_eq!(outcome.usage.total(), 15);
    }

    #[test]
    fn tool_call_serde_round_trip() {
        let tc = ToolCall {
            tool_id: "cascade.search".into(),
            args: serde_json::json!({ "query": "foo" }),
            call_id: "call-001".into(),
        };
        let json = serde_json::to_string(&tc).unwrap();
        let decoded: ToolCall = serde_json::from_str(&json).unwrap();
        assert_eq!(decoded.tool_id, "cascade.search");
        assert_eq!(decoded.call_id, "call-001");
    }
}
