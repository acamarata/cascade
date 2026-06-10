//! Agent plugin trait — host-side plugin-facing API.
//!
//! Purpose: define the `AgentPlugin` trait that a WASM plugin must satisfy to
//!   be registered as an autonomous agent in the cascade-plugins host. Agents
//!   receive a context (conversation + available tools) and produce an observation.
//! Inputs: `AgentContext` — conversation history + available tools.
//! Outputs: `AgentObservation` — FinalAnswer, ToolCall, or Error.
//! Constraints: all public types are `Serialize + Deserialize`; trait is object-safe;
//!   `Send + Sync + 'static` required; no multi-step loop inside the trait itself.
//! SPORT: cascade-plugins / agent plugin trait (T-P4-E03-02)

use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use thiserror::Error;

// ── I/O shapes ────────────────────────────────────────────────────────────────

/// Metadata for a tool available to the agent.
///
/// Tools in this context are the tools the agent *can call* (declared by the
/// host or other plugins). The JSON schema follows the MCP tool spec shape.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AgentTool {
    /// Tool identifier (must match `ToolIntegration::name()`).
    pub name: String,
    /// Human-readable description of what the tool does.
    pub description: String,
    /// JSON Schema object describing the tool's input parameters.
    pub input_schema: serde_json::Value,
}

/// Context passed to every `AgentPlugin::run()` invocation.
///
/// The agent must not assume any state beyond what is in this struct.
/// The host reconstructs context fresh per invocation (stateless agent contract).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AgentContext {
    /// Ordered conversation history.
    pub messages: Vec<crate::traits::provider::Message>,

    /// Tools available to the agent in this invocation.
    #[serde(default)]
    pub available_tools: Vec<AgentTool>,

    /// Optional system instruction injected by the host.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub system: Option<String>,

    /// Session-scoped key/value context (e.g. current file path, active tier).
    #[serde(default)]
    pub session: std::collections::HashMap<String, serde_json::Value>,
}

/// An observation produced by one `AgentPlugin::run()` call.
///
/// The agent produces exactly one observation per invocation. If it needs
/// multiple tool calls, the host re-invokes it with the tool results appended
/// to `messages` (the react loop lives in the host, not the plugin).
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum AgentObservation {
    /// The agent produced a final text answer — no tool call needed.
    FinalAnswer {
        /// The agent's response text.
        text: String,
    },
    /// The agent wants to call a tool before producing its final answer.
    ToolCall {
        /// The tool to call (must match a name in `AgentContext::available_tools`).
        tool_name: String,
        /// JSON arguments to pass to the tool (must conform to the tool's schema).
        arguments: serde_json::Value,
    },
    /// The agent encountered an error it cannot recover from.
    Error {
        /// Human-readable error message.
        message: String,
    },
}

// ── Error type ────────────────────────────────────────────────────────────────

/// Errors that an `AgentPlugin` may return (host-level, not agent-level).
///
/// Note: agent-level errors are expressed via `AgentObservation::Error` so the
/// host can distinguish a plugin failure from an agent-logic refusal.
#[derive(Debug, Error, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum AgentPluginError {
    /// Plugin encountered an I/O failure (network, file system).
    #[error("IO error in agent plugin: {message}")]
    Io { message: String },

    /// The context payload was malformed.
    #[error("parse error in agent plugin: {message}")]
    Parse { message: String },

    /// The plugin is temporarily unavailable.
    #[error("agent plugin unavailable: {message}")]
    Unavailable { message: String },
}

// ── Capability declarations ───────────────────────────────────────────────────

/// Capability declaration for an `AgentPlugin`.
///
/// Agent plugins may load model weights or large context buffers; 256 MiB is
/// the default memory limit (same tier as Provider/Retriever).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AgentPluginCapabilities {
    /// Whether this plugin requires outbound network access.
    #[serde(default)]
    pub needs_net_outbound: bool,

    /// Whether this plugin requires filesystem read access.
    #[serde(default)]
    pub needs_fs_read: bool,

    /// Requested WASM memory in MiB. Host may cap to `MEM_LARGE_MB` (256).
    #[serde(default = "default_agent_memory_mb")]
    pub max_memory_mb: u32,
}

fn default_agent_memory_mb() -> u32 {
    256
}

impl Default for AgentPluginCapabilities {
    fn default() -> Self {
        Self {
            needs_net_outbound: false,
            needs_fs_read: false,
            max_memory_mb: default_agent_memory_mb(),
        }
    }
}

// ── Trait ─────────────────────────────────────────────────────────────────────

/// Host-side trait a WASM agent plugin must satisfy.
///
/// Agents are stateless per-invocation. The host manages the react loop; the
/// plugin produces one `AgentObservation` per call.
///
/// # Object-safety
///
/// This trait is object-safe. Async methods use `async_trait`.
///
/// # Example (mock implementation for tests)
///
/// ```rust
/// # use cascade_plugins::traits::agent::{AgentPlugin, AgentPluginCapabilities, AgentPluginError, AgentContext, AgentObservation};
/// # use async_trait::async_trait;
/// struct EchoAgent;
///
/// #[async_trait]
/// impl AgentPlugin for EchoAgent {
///     fn name(&self) -> &str { "echo-agent" }
///     fn capabilities(&self) -> AgentPluginCapabilities { Default::default() }
///
///     async fn run(&self, ctx: AgentContext) -> Result<AgentObservation, AgentPluginError> {
///         let last = ctx.messages.last().map(|m| m.content.clone()).unwrap_or_default();
///         Ok(AgentObservation::FinalAnswer { text: format!("echo: {last}") })
///     }
/// }
/// ```
#[async_trait]
pub trait AgentPlugin: Send + Sync + 'static {
    /// A stable, human-readable name for this plugin.
    fn name(&self) -> &str;

    /// Capability declaration — inspected at plugin registration time.
    fn capabilities(&self) -> AgentPluginCapabilities;

    /// Process `ctx` and produce one observation.
    ///
    /// The host calls this in a loop: if `AgentObservation::ToolCall` is returned,
    /// the host executes the tool and re-invokes with the result appended.
    async fn run(&self, ctx: AgentContext) -> Result<AgentObservation, AgentPluginError>;
}

// ── Object-safety static assertion ───────────────────────────────────────────

fn _assert_object_safe(_: &dyn AgentPlugin) {}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::traits::provider::{Message, MessageRole};

    struct MockAgent;

    #[async_trait]
    impl AgentPlugin for MockAgent {
        fn name(&self) -> &str {
            "mock-agent"
        }

        fn capabilities(&self) -> AgentPluginCapabilities {
            Default::default()
        }

        async fn run(&self, ctx: AgentContext) -> Result<AgentObservation, AgentPluginError> {
            if ctx.messages.is_empty() {
                return Err(AgentPluginError::Parse {
                    message: "no messages in context".into(),
                });
            }
            let last = ctx.messages.last().unwrap().content.clone();
            if last == "use_tool" {
                return Ok(AgentObservation::ToolCall {
                    tool_name: "calculator".into(),
                    arguments: serde_json::json!({ "expression": "2 + 2" }),
                });
            }
            Ok(AgentObservation::FinalAnswer {
                text: format!("echo: {last}"),
            })
        }
    }

    fn make_ctx(content: &str) -> AgentContext {
        AgentContext {
            messages: vec![Message {
                role: MessageRole::User,
                content: content.into(),
            }],
            available_tools: vec![],
            system: None,
            session: Default::default(),
        }
    }

    #[tokio::test]
    async fn agent_plugin_final_answer() {
        let plugin = MockAgent;
        let obs = plugin.run(make_ctx("hello")).await.unwrap();
        assert!(
            matches!(obs, AgentObservation::FinalAnswer { ref text } if text.contains("echo: hello"))
        );
    }

    #[tokio::test]
    async fn agent_plugin_tool_call() {
        let plugin = MockAgent;
        let obs = plugin.run(make_ctx("use_tool")).await.unwrap();
        if let AgentObservation::ToolCall {
            tool_name,
            arguments,
        } = obs
        {
            assert_eq!(tool_name, "calculator");
            assert_eq!(arguments["expression"], "2 + 2");
        } else {
            panic!("expected ToolCall observation");
        }
    }

    #[tokio::test]
    async fn agent_plugin_error_path() {
        let plugin = MockAgent;
        let ctx = AgentContext {
            messages: vec![],
            available_tools: vec![],
            system: None,
            session: Default::default(),
        };
        let err = plugin.run(ctx).await.unwrap_err();
        assert!(matches!(err, AgentPluginError::Parse { .. }));
    }

    #[test]
    fn agent_context_serde_round_trip() {
        let ctx = AgentContext {
            messages: vec![Message {
                role: MessageRole::User,
                content: "what is cargo?".into(),
            }],
            available_tools: vec![AgentTool {
                name: "search".into(),
                description: "Search docs".into(),
                input_schema: serde_json::json!({ "type": "object" }),
            }],
            system: Some("You are helpful.".into()),
            session: std::collections::HashMap::from([(
                "cwd".into(),
                serde_json::Value::String("/home/user".into()),
            )]),
        };
        let json = serde_json::to_string(&ctx).unwrap();
        let decoded: AgentContext = serde_json::from_str(&json).unwrap();
        assert_eq!(decoded.messages.len(), 1);
        assert_eq!(decoded.available_tools.len(), 1);
        assert_eq!(decoded.available_tools[0].name, "search");
        assert_eq!(decoded.system, Some("You are helpful.".into()));
    }

    #[test]
    fn agent_observation_serde_round_trip() {
        let obs = AgentObservation::ToolCall {
            tool_name: "read_file".into(),
            arguments: serde_json::json!({ "path": "/etc/hosts" }),
        };
        let json = serde_json::to_string(&obs).unwrap();
        let decoded: AgentObservation = serde_json::from_str(&json).unwrap();
        if let AgentObservation::ToolCall {
            tool_name,
            arguments,
        } = decoded
        {
            assert_eq!(tool_name, "read_file");
            assert_eq!(arguments["path"], "/etc/hosts");
        } else {
            panic!("expected ToolCall");
        }
    }

    #[test]
    fn agent_plugin_error_serde_round_trip() {
        let err = AgentPluginError::Unavailable {
            message: "model loading".into(),
        };
        let json = serde_json::to_string(&err).unwrap();
        let decoded: AgentPluginError = serde_json::from_str(&json).unwrap();
        let msg = decoded.to_string();
        assert!(msg.contains("model loading"));
    }

    #[test]
    fn trait_object_usable_as_box() {
        let _plugin: Box<dyn AgentPlugin> = Box::new(MockAgent);
    }
}
