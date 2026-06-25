//! `cascade mcp status` — show MCP server transport status.

use async_trait::async_trait;
use cascade_types::{error::Result, paths::home_dir};

use crate::cmd::Command;
use super::token::McpTokenArgs;

// ── Status ────────────────────────────────────────────────────────────────────

/// Arguments for `cascade mcp status`.
#[derive(Debug, clap::Args)]
pub struct McpStatusArgs;

#[async_trait]
impl Command for McpStatusArgs {
    async fn run(&self) -> Result<()> {
        let runtime_dir = home_dir().join(".cascade").join("runtime");
        let socket_path = runtime_dir.join("mcp.sock");

        let socket_status = if socket_path.exists() {
            "active"
        } else {
            "inactive"
        };

        println!("MCP Server Status");
        println!("─────────────────────────────────────");
        println!("Unix socket:  {socket_status}  ({})", socket_path.display());
        println!("HTTP:         http://127.0.0.1:7722/mcp");
        println!("stdio:        available (subprocess mode)");

        // Token (best-effort — may not exist if server not started)
        match McpTokenArgs::mint_token() {
            Ok(token) => println!("\nAuth token:   {token}"),
            Err(e) => println!("\nAuth token:   unavailable ({e})"),
        }

        Ok(())
    }
}
