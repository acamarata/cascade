//! `cascade mcp setup` — Command impl for McpSetupArgs.

use async_trait::async_trait;
use cascade_types::error::{CascadeError, Result};

use super::args::{McpSetupArgs, ToolName};
use super::clients::{
    setup_all, setup_claude_code, setup_claude_desktop, setup_list, setup_opencode, setup_vscode,
};
use crate::cmd::Command;

#[async_trait]
impl Command for McpSetupArgs {
    async fn run(&self) -> Result<()> {
        if self.list {
            return setup_list();
        }
        if self.all {
            return setup_all(self.remove, self.dry_run);
        }
        match &self.tool {
            Some(ToolName::ClaudeCode) => setup_claude_code(self.remove, self.dry_run),
            Some(ToolName::ClaudeDesktop) => setup_claude_desktop(self.remove, self.dry_run),
            Some(ToolName::Vscode) => setup_vscode(self.remove, self.global, self.dry_run),
            Some(ToolName::Opencode) => {
                setup_opencode(self.remove, self.local, self.http, self.dry_run)
            }
            None => {
                eprintln!("Error: specify --tool <tool>, --all, or --list");
                eprintln!("  Tools: claude-code, claude-desktop, vscode, opencode");
                Err(CascadeError::Other("no tool specified".into()))
            }
        }
    }
}
