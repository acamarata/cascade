//! MCP tool registry — `tools/list` + `tools/call` dispatch.
//!
//! ## Module layout
//!
//! | Sub-module | Contents |
//! |---|---|
//! | [`types`] | Core types: `McpTool`, `ConnectionContext`, `RetrieverSlot` |
//! | [`helpers`] | Shared utilities: `tool_result`, `call_tool_error`, `chrono_local_date` |
//! | [`schemas`] | All 22 tool JSON-Schema definitions |
//! | [`handlers_core`] | 10 core tool handlers (read, search, inbox, memory, …) |
//! | [`handlers_pbd`] | 8 PBD tool handlers (get_current, update_ticket_status, …) |
//! | [`handlers_memory`] | 4 RAG-08 memory handlers (remember, recall, forget, search) |
//! | [`registry`] | `ToolRegistry` — dispatch hub |
//! | [`context_assembler`] | Role-aware context assembly layer (ctx-01) |

mod types;
mod helpers;
mod schemas;
mod handlers_core;
mod handlers_pbd;
pub(crate) mod handlers_memory;
pub mod context_assembler;
pub mod registry;

#[cfg(test)]
mod tests;

pub use types::{ConnectionContext, McpTool, RetrieverSlot};
pub use registry::ToolRegistry;
