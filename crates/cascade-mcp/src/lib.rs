//! cascade-mcp — MCP 2025-03 spec server for the Cascade AI context framework.
//!
//! This crate exposes Cascade's retrieval, inbox, memory, and master-list
//! capabilities over the Model Context Protocol so any MCP-aware AI client
//! (Claude Code, Claude Desktop, VS Code / Continue, OpenCode) can consume
//! Cascade as a context source.
//!
//! ## Architecture
//!
//! ```text
//! ┌───────────────────────────────────────────────────────┐
//! │                  McpServer                            │
//! │  ┌──────────────┐  ┌───────────┐  ┌──────────────┐  │
//! │  │  Resources   │  │  Tools    │  │  Prompts     │  │
//! │  │  (cascade://)│  │  (7 tools)│  │  (templates) │  │
//! │  └──────────────┘  └───────────┘  └──────────────┘  │
//! │  ┌────────────────────────────────────────────────┐  │
//! │  │  Sampling │ Logging │ Progress │ Cancellation  │  │
//! │  └────────────────────────────────────────────────┘  │
//! └────────────────────────┬──────────────────────────────┘
//!                          │  (trait: Transport)
//!       ┌──────────────────┼────────────────────┐
//!       │                  │                    │
//!   stdio          SSE/HTTP streaming      Unix socket
//! ```
//!
//! ## MCP version
//!
//! Implements the **MCP 2025-03 specification** (JSON-RPC 2.0 transport,
//! capability negotiation, resources/tools/prompts/sampling/logging).
//!
//! ## Tools exposed
//!
//! | Tool | Description |
//! |------|-------------|
//! | `cascade.search` | Hybrid RAG search across the watched corpus |
//! | `cascade.read` | Read a cascade tier instruction file |
//! | `cascade.search_codebase` | Code-aware search via tree-sitter index |
//! | `cascade.inbox.list` | List PCI inbox messages for a project |
//! | `cascade.inbox.send` | Send a PCI message to a project inbox |
//! | `cascade.master_lists` | Read project master lists (routes/tables/etc.) |
//! | `cascade.memory.read` | Read a project memory file |
//! | `cascade.memory.write` | Append to a project memory file |
//!
//! ## Usage
//!
//! ```rust,no_run
//! use cascade_mcp::{McpServer, McpServerConfig};
//! use cascade_mcp::transport::stdio::StdioTransport;
//!
//! # async fn run() -> cascade_types::error::Result<()> {
//! let config = McpServerConfig::default();
//! let transport = StdioTransport::new();
//! let server = McpServer::new(config, Box::new(transport));
//! server.run().await?;
//! # Ok(())
//! # }
//! ```

// ── Module declarations ───────────────────────────────────────────────────────

pub mod auth;
pub mod cancellation;
pub mod error;
pub mod handler;
pub mod handlers;
pub mod logging;
pub mod notification;
pub mod paths;
pub mod progress;
pub mod prompt;
pub mod resource;
pub mod sampling;
pub mod server;
pub mod tool;
pub mod tracing_layer;
pub mod transport;

// ── Re-exports ────────────────────────────────────────────────────────────────

pub use auth::{AuthError, McpAuth, McpSecretManager};
pub use error::McpServerError;
pub use handler::{HandlerRegistry, McpHandler};
pub use notification::NotificationBus;
pub use server::{McpServer, McpServerConfig};
pub use transport::connection::{ConnectionContext, SubscriptionSet, MAX_FRAME_BYTES};
pub use transport::McpTransport;
