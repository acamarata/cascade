//! `cascade inbox` — manage `.cascade/inbox/` messages.
//!
//! Subcommands:
//! - `list [--tier <tier>]` — list messages from the active tier inboxes
//! - `send <target> <type> <subject>` — write a message to a target inbox
//! - `archive <msg-id>` — move a processed message to `archive/inbox/`

use std::path::PathBuf;

use async_trait::async_trait;
use cascade_types::error::Result;
use clap::{Args, Subcommand};

use super::Command;

/// Arguments for `cascade inbox`.
#[derive(Debug, Args)]
pub struct InboxArgs {
    #[command(subcommand)]
    pub subcommand: InboxSubcmd,
}

#[derive(Debug, Subcommand)]
pub enum InboxSubcmd {
    /// List unarchived messages from all visible tier inboxes.
    List(InboxListArgs),
    /// Write a new message to the specified target inbox.
    Send(InboxSendArgs),
    /// Move a processed message to `archive/inbox/`.
    Archive(InboxArchiveArgs),
}

#[derive(Debug, Args)]
pub struct InboxListArgs {
    /// Filter to messages in a specific tier (gci, pci, apc, ppc, prc, pac).
    #[arg(long)]
    pub tier: Option<String>,
}

#[derive(Debug, Args)]
pub struct InboxSendArgs {
    /// Target cascade tier directory path (e.g. `/home/user/projects/myapp/.cascade`).
    pub target: PathBuf,
    /// Message type: bug, feature, enhancement, info, question, etc.
    #[arg(long, default_value = "info")]
    pub r#type: String,
    /// Message subject line.
    pub subject: String,
    /// Priority: critical, high, medium, low.
    #[arg(long, default_value = "medium")]
    pub priority: String,
    /// Message body read from stdin when `-` is passed, otherwise this string.
    #[arg(long)]
    pub body: Option<String>,
}

#[derive(Debug, Args)]
pub struct InboxArchiveArgs {
    /// Path to the message file to archive.
    pub message: PathBuf,
}

#[async_trait]
impl Command for InboxArgs {
    async fn run(&self) -> Result<()> {
        match &self.subcommand {
            InboxSubcmd::List(args) => args.run().await,
            InboxSubcmd::Send(args) => args.run().await,
            InboxSubcmd::Archive(args) => args.run().await,
        }
    }
}

#[async_trait]
impl Command for InboxListArgs {
    async fn run(&self) -> Result<()> {
        let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
        let tiers = cascade_core::discovery::TierDiscovery::new().discover(&cwd)?;

        for tier in &tiers {
            if !tier.is_readable() {
                continue;
            }
            // WHY: DiscoveredTier.cascade_dir is already the `.cascade/` directory.
            let messages = cascade_core::inbox::list(&tier.cascade_dir).await?;
            for msg in &messages {
                let preview = msg.content.lines().next().unwrap_or("(empty)");
                println!(
                    "[{:?}] {} — {}",
                    tier.tier,
                    msg.path.file_name().unwrap_or_default().to_string_lossy(),
                    preview
                );
            }
        }
        Ok(())
    }
}

#[async_trait]
impl Command for InboxSendArgs {
    async fn run(&self) -> Result<()> {
        let slug = self
            .subject
            .to_lowercase()
            .split_whitespace()
            .take(5)
            .collect::<Vec<_>>()
            .join("-");

        let body = self.body.clone().unwrap_or_default();
        let content = format!(
            "---\nSubject: {}\nType: {}\nPriority: {}\nFrom: cascade-cli\nTo: {}\n---\n\n{}\n",
            self.subject,
            self.r#type,
            self.priority,
            self.target.display(),
            body
        );

        let path = cascade_core::inbox::send(&self.target, &slug, &content).await?;
        println!("Sent: {}", path.display());
        Ok(())
    }
}

#[async_trait]
impl Command for InboxArchiveArgs {
    async fn run(&self) -> Result<()> {
        // Derive cascade_dir from the message's parent's parent (.cascade/).
        let cascade_dir = self
            .message
            .parent()
            .and_then(|p| p.parent())
            .unwrap_or_else(|| std::path::Path::new("."))
            .to_path_buf();
        cascade_core::inbox::archive(&cascade_dir, &self.message).await?;
        println!("Archived: {}", self.message.display());
        Ok(())
    }
}
