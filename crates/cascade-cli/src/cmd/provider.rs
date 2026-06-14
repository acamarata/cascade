//! `cascade provider` — manage AI provider credentials from the CLI.
//!
//! # Purpose
//!
//! Enables a fresh machine to authenticate a cloud AI provider without the GUI.
//! Credentials are validated against the provider's auth endpoint before any
//! persistent write.  Secrets are stored in the OS keychain only; they are
//! never written to disk in plaintext.
//!
//! # Subcommands
//!
//! | Subcommand | Description |
//! |------------|-------------|
//! | `add` | Validate and store a provider API key |
//! | `list` | Show connected providers and health status |
//! | `remove` | Delete a provider's keychain entry + providers.json record |
//! | `test` | Perform a live health-check against a connected provider |
//!
//! # Usage examples
//!
//! ```text
//! cascade provider add --kind anthropic --api-key sk-ant-...
//! cascade provider list
//! cascade provider remove anthropic
//! cascade provider test anthropic
//! ```
//!
//! # Constraints
//!
//! - API key is NEVER printed, logged, or stored in plaintext.
//! - `add` calls `validate_api_key` before any keychain write.
//! - `test` re-validates the stored key (reads from keychain, pings provider).
//! - `--kind` accepts all slugs defined in [`cascade_providers::ProviderKind`].
//!
//! # SPORT
//!
//! Entity: cascade-cli / provider subcommand (E-P5-08)

use async_trait::async_trait;
use cascade_keychain::platform_keychain;
use cascade_providers::{
    connect_provider, list_providers, remove_provider, validate_api_key, Credential, ProviderKind,
    PROVIDER_KEYCHAIN_SERVICE,
};
use cascade_types::error::{CascadeError, Result};
use clap::{Args, Subcommand};

use super::Command;

// ── Arg types ─────────────────────────────────────────────────────────────────

/// Arguments for `cascade provider`.
#[derive(Debug, Args)]
pub struct ProviderArgs {
    #[command(subcommand)]
    pub command: ProviderCommands,
}

/// Subcommands under `cascade provider`.
#[derive(Debug, Subcommand)]
pub enum ProviderCommands {
    /// Validate and store a provider API key.
    Add(AddArgs),
    /// Show connected providers and health status.
    List(ListArgs),
    /// Delete a provider's keychain entry and providers.json record.
    Remove(RemoveArgs),
    /// Perform a live health-check against a connected provider.
    Test(TestArgs),
}

/// Arguments for `cascade provider add`.
#[derive(Debug, Args)]
pub struct AddArgs {
    /// Provider kind (e.g. anthropic, openai, gemini, openrouter, groq, mistral).
    #[arg(long, short = 'k')]
    pub kind: String,

    /// API key to validate and store.
    #[arg(long, short = 'a')]
    pub api_key: String,

    /// Override the validation base URL (for testing or self-hosted instances).
    #[arg(long, hide = true)]
    pub base_url: Option<String>,
}

/// Arguments for `cascade provider list`.
#[derive(Debug, Args)]
pub struct ListArgs {
    /// Output as JSON instead of a human-readable table.
    #[arg(long)]
    pub json: bool,
}

/// Arguments for `cascade provider remove`.
#[derive(Debug, Args)]
pub struct RemoveArgs {
    /// Provider id to remove (e.g. `anthropic`, `openai`).
    pub kind: String,
}

/// Arguments for `cascade provider test`.
#[derive(Debug, Args)]
pub struct TestArgs {
    /// Provider id to test (e.g. `anthropic`).
    pub kind: String,
}

// ── Command impls ─────────────────────────────────────────────────────────────

#[async_trait]
impl Command for ProviderArgs {
    async fn run(&self) -> Result<()> {
        match &self.command {
            ProviderCommands::Add(args) => args.run().await,
            ProviderCommands::List(args) => args.run().await,
            ProviderCommands::Remove(args) => args.run().await,
            ProviderCommands::Test(args) => args.run().await,
        }
    }
}

// ── add ───────────────────────────────────────────────────────────────────────

#[async_trait]
impl Command for AddArgs {
    async fn run(&self) -> Result<()> {
        let kind = parse_kind(&self.kind)?;
        let name = kind.display_name().to_string();

        let kc = platform_keychain();
        let result = connect_provider(kind, Credential::ApiKey(self.api_key.clone()), kc.as_ref())
            .await
            .map_err(|e| CascadeError::Other(e.to_string()))?;

        println!(
            "Connected: {} ({}) — stored in OS keychain",
            result.name, result.id
        );
        let _ = name; // used in error path via kind.display_name()
        Ok(())
    }
}

// ── list ──────────────────────────────────────────────────────────────────────

#[async_trait]
impl Command for ListArgs {
    async fn run(&self) -> Result<()> {
        let kc = platform_keychain();
        let providers = list_providers(kc.as_ref());

        if self.json {
            let json = serde_json::to_string_pretty(&providers)
                .map_err(|e| CascadeError::Other(e.to_string()))?;
            println!("{json}");
            return Ok(());
        }

        if providers.is_empty() {
            println!(
                "No providers connected. Use `cascade provider add --kind <KIND> --api-key <KEY>`."
            );
            return Ok(());
        }

        println!("{:<20} {:<30} {:<10}", "ID", "NAME", "HEALTHY");
        println!("{}", "-".repeat(62));
        for p in &providers {
            println!(
                "{:<20} {:<30} {:<10}",
                p.id,
                p.name,
                if p.healthy { "yes" } else { "no (key missing)" }
            );
        }

        Ok(())
    }
}

// ── remove ────────────────────────────────────────────────────────────────────

#[async_trait]
impl Command for RemoveArgs {
    async fn run(&self) -> Result<()> {
        let kc = platform_keychain();
        let removed = remove_provider(&self.kind, kc.as_ref())
            .map_err(|e| CascadeError::Other(e.to_string()))?;

        if removed {
            println!("Removed provider '{}'.", self.kind);
        } else {
            println!("Provider '{}' was not connected.", self.kind);
        }

        Ok(())
    }
}

// ── test ──────────────────────────────────────────────────────────────────────

#[async_trait]
impl Command for TestArgs {
    async fn run(&self) -> Result<()> {
        let kind = parse_kind(&self.kind)?;
        let kc = platform_keychain();

        // Read the stored secret from keychain.
        let api_key = kc
            .get_key(PROVIDER_KEYCHAIN_SERVICE, &kind.id())
            .map_err(|_| {
                CascadeError::Other(format!(
                    "No API key found for '{}'. Run `cascade provider add` first.",
                    kind.id()
                ))
            })?;

        // Validate against the live endpoint.
        validate_api_key(&kind, &api_key, None)
            .await
            .map_err(|e| CascadeError::Other(e.to_string()))?;

        println!("Provider '{}' is healthy.", kind.id());
        Ok(())
    }
}

// ── helpers ───────────────────────────────────────────────────────────────────

/// Parse a provider kind from a CLI slug, returning a friendly error on unknown slugs.
fn parse_kind(slug: &str) -> Result<ProviderKind> {
    ProviderKind::from_slug(slug).ok_or_else(|| {
        CascadeError::Other(format!(
            "Unknown provider kind '{slug}'. Supported: anthropic, openai, gemini, openrouter, groq, mistral, deepseek, together, cohere."
        ))
    })
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_kind_known_slugs() {
        for slug in [
            "anthropic",
            "openai",
            "gemini",
            "openrouter",
            "groq",
            "mistral",
            "deepseek",
            "together",
            "cohere",
        ] {
            let r = parse_kind(slug);
            assert!(r.is_ok(), "slug '{slug}' should parse");
        }
    }

    #[test]
    fn parse_kind_unknown_slug_returns_error() {
        let r = parse_kind("totally-unknown-provider");
        assert!(r.is_err());
        let msg = r.unwrap_err().to_string();
        assert!(
            msg.contains("totally-unknown-provider"),
            "error should mention the bad slug"
        );
    }

    #[test]
    fn parse_kind_case_insensitive() {
        assert!(parse_kind("ANTHROPIC").is_ok());
        assert!(parse_kind("OpenAI").is_ok());
    }
}
