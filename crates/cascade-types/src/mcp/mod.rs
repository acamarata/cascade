//! # MCP 2025-03-26 type definitions
//!
//! This module provides all data-transfer types required by the Model Context
//! Protocol specification (2025-03-26). It is split into three sub-modules:
//!
//! | Module | Contents |
//! |--------|---------|
//! | [`jsonrpc`] | JSON-RPC 2.0 base types and error codes |
//! | [`capabilities`] | Initialize handshake + capability types |
//! | [`primitives`] | Resources, tools, prompts, sampling, logging |

pub mod capabilities;
pub mod jsonrpc;
pub mod primitives;

// Re-export everything so callers can write `use cascade_types::mcp::*`
pub use capabilities::*;
pub use jsonrpc::*;
pub use primitives::*;
