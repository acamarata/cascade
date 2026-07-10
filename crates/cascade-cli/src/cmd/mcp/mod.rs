//! `cascade mcp` — MCP server token management and client setup.
//!
//! ## Purpose
//! Provides the `cascade mcp` subcommand surface for interacting with the
//! cascade MCP server: printing auth tokens, checking server status, and
//! configuring supported AI client tools.
//!
//! ## Subcommands
//! - `cascade mcp token` — print the current HMAC bearer token
//! - `cascade mcp status` — show MCP server transport status
//! - `cascade mcp setup --tool <tool>` — configure an MCP client
//! - `cascade mcp setup --all` — auto-detect and configure all clients
//! - `cascade mcp setup --list` — detect clients without configuring
//!
//! ## Inputs
//! - `~/.cascade/runtime/mcp-secret.key` — 32-byte secret key file
//! - Platform-specific client config files (merged non-destructively)
//!
//! ## Outputs
//! - Token printed to stdout (no trailing newline decorations)
//! - Config files written atomically (write temp → rename)
//!
//! ## Constraints
//! - Atomic writes: temp file → rename, never direct overwrite
//! - Non-destructive: existing config entries preserved; cascade entry upserted
//! - HOME-confined: all paths derived from home_dir(), never hard-coded
//! - Backup existing config before first write (`.bak` suffix)
//!
//! ## SPORT
//! MASTER-CLI.md: cascade mcp subcommands

mod args;
mod clients;
mod helpers;
mod setup;
mod status;
mod stdio;
mod tests;
mod token;

use async_trait::async_trait;
use cascade_types::error::Result;

use super::Command;
use args::McpSubcmd;

// Re-export public API at the same path as before. These are lib API
// surface; the bin compiles this module tree privately, so they count as
// unused imports there.
#[allow(unused_imports)]
pub use args::{McpArgs, McpSetupArgs, McpSubcmd as McpSubcommand, ToolName};
#[allow(unused_imports)]
pub use clients::{
    claude_code_settings_path, claude_desktop_config_path, detect_clients, opencode_config_path,
    opencode_installed, setup_all, setup_claude_code, setup_claude_desktop, setup_list,
    setup_opencode, setup_vscode, vscode_mcp_path, DetectionResult,
};
#[allow(unused_imports)]
pub use status::McpStatusArgs;
#[allow(unused_imports)]
pub use stdio::McpStdioArgs;
#[allow(unused_imports)]
pub use token::McpTokenArgs;

// ── Command dispatch ──────────────────────────────────────────────────────────

#[async_trait]
impl Command for McpArgs {
    async fn run(&self) -> Result<()> {
        match &self.subcommand {
            McpSubcmd::Token(args) => args.run().await,
            McpSubcmd::Status(args) => args.run().await,
            McpSubcmd::Setup(args) => args.run().await,
            McpSubcmd::Stdio(args) => args.run().await,
        }
    }
}
