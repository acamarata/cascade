//! `cascade mcp token` — print the current HMAC bearer token.

use std::path::PathBuf;

use async_trait::async_trait;
use cascade_mcp::{McpAuth, McpSecretManager};
use cascade_types::{
    error::{CascadeError, Result},
    paths::home_dir,
};

use crate::cmd::Command;

// ── Token ─────────────────────────────────────────────────────────────────────

/// Arguments for `cascade mcp token`.
#[derive(Debug, clap::Args)]
pub struct McpTokenArgs;

impl McpTokenArgs {
    fn secret_path() -> PathBuf {
        home_dir()
            .join(".cascade")
            .join("runtime")
            .join("mcp-secret.key")
    }

    pub fn load_auth() -> Result<McpAuth> {
        let path = Self::secret_path();
        if !path.exists() {
            return Err(CascadeError::Other(
                "Cascade MCP server not running or not yet initialized. Run: cascade daemon start"
                    .into(),
            ));
        }
        McpSecretManager::load_or_create(&path).map_err(|e| CascadeError::Other(e.to_string()))
    }

    pub fn mint_token() -> Result<String> {
        let auth = Self::load_auth()?;
        Ok(auth.generate_token())
    }
}

#[async_trait]
impl Command for McpTokenArgs {
    async fn run(&self) -> Result<()> {
        let token = McpTokenArgs::mint_token()?;
        print!("{token}");
        Ok(())
    }
}
