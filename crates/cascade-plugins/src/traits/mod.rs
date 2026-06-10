//! Plugin-facing host-side trait definitions.
//!
//! Purpose: define the five extension-kind traits that the cascade-plugins host
//!   exposes to plugins at the WASM boundary. These are distinct from the
//!   `cascade-types` internal RAG traits — they carry serde-able JSON I/O shapes
//!   suitable for the WASM ABI (JSON ptr/len protocol).
//! Inputs: N/A — module re-export only.
//! Outputs: public trait and I/O type exports.
//! Constraints: all I/O types must be `Serialize + Deserialize` for WASM JSON ABI;
//!   all traits must be object-safe with `Send + Sync + 'static` bounds.
//! SPORT: cascade-plugins / plugin-facing traits layer (T-P4-E03-01/02/03)

pub mod agent;
pub mod chunker;
pub mod provider;
pub mod retriever;
pub mod tool_integration;
pub mod widget;

// Re-export all public items for ergonomic import.
pub use agent::{
    AgentContext, AgentObservation, AgentPlugin, AgentPluginCapabilities, AgentPluginError,
    AgentTool,
};
pub use chunker::{
    Chunk, ChunkPlugin, ChunkPluginCapabilities, ChunkPluginError, Query as ChunkQuery,
};
pub use provider::{
    Message, MessageRole, ProviderDelta, ProviderPlugin, ProviderPluginCapabilities,
    ProviderPluginError, ProviderRequest, ProviderResponse,
};
pub use retriever::{
    RetrievalResult, RetrieveQuery, RetrieverPlugin, RetrieverPluginCapabilities,
    RetrieverPluginError,
};
pub use tool_integration::{
    ToolCall, ToolIntegration, ToolIntegrationCapabilities, ToolIntegrationError, ToolResult,
};
pub use widget::{WidgetData, WidgetPlugin, WidgetPluginCapabilities, WidgetPluginError};
