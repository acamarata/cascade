//! MCP tool registry — `tools/list` + `tools/call` dispatch.
//!
//! ## Module layout
//!
//! | Sub-module | Contents |
//! |---|---|
//! | [`types`] | Core types: `McpTool`, `ConnectionContext`, `RetrieverSlot` |
//! | [`helpers`] | Shared utilities: `tool_result`, `call_tool_error`, `chrono_local_date` |
//! | [`schemas`] | All 24 tool JSON-Schema definitions |
//! | [`handlers_core`] | 10 core tool handlers (read, search, inbox, memory, …) |
//! | [`handlers_pbd`] | 8 PBD tool handlers (get_current, update_ticket_status, …) |
//! | [`handlers_memory`] | 4 RAG-08 memory handlers (remember, recall, forget, search) |
//! | [`handlers_security`] | 2 security handlers (secret_scan, audit) |
//! | [`registry`] | `ToolRegistry` — dispatch hub |
//! | [`context_assembler`] | Role-aware context assembly layer (ctx-01) |

pub mod context_assembler;
mod handlers_core;
pub(crate) mod handlers_memory;
mod handlers_pbd;
mod handlers_security;
mod helpers;
pub mod registry;
mod schemas;
mod types;

#[cfg(test)]
mod tests;

pub use registry::ToolRegistry;
pub use types::{ConnectionContext, McpTool, RetrieverSlot};
