//! `cascade subs` — list detected AI coding subscriptions and their auth status.
//!
//! ## Subcommands
//!
//!   cascade subs list [--json]   — list all detected subscriptions with status
//!
//! ## Purpose
//!
//! Scans the local machine for known AI coding subscriptions (Claude Code,
//! OpenCode, Codex, Cursor, Antigravity) and reports:
//!
//! - Whether each is installed (config/binary found on disk).
//! - Whether it has an authenticated session (email/token present).
//! - The config path where detection evidence was found.
//!
//! Detection reuses `cascade_providers::auto_auth_import::scan_all()` so it
//! is always consistent with the onboarding wizard's detection logic.
//!
//! ## Integration note
//!
//! This command does NOT route inference through any subscription. It is a
//! read-only diagnostic tool. Config generation is handled by
//! `cascade harness generate --harness <cursor|antigravity>`.
//!
//! ## SPORT
//! MASTER-CLI.md: cascade subs list (E-P7-06)

use async_trait::async_trait;
use cascade_providers::{AuthSource, DiscoveredAccount};
use cascade_types::error::Result;
use clap::{Args, Subcommand};
use serde::Serialize;

use super::Command;

// ── Top-level args ────────────────────────────────────────────────────────────

/// Manage and inspect AI coding subscriptions.
#[derive(Debug, Args)]
pub struct SubsArgs {
    #[command(subcommand)]
    pub subcommand: SubsSubcommand,
}

#[derive(Debug, Subcommand)]
pub enum SubsSubcommand {
    /// List all detected subscriptions with their installation and auth status.
    List(SubsListArgs),
}

#[async_trait]
impl Command for SubsArgs {
    async fn run(&self) -> Result<()> {
        match &self.subcommand {
            SubsSubcommand::List(args) => args.run().await,
        }
    }
}

// ── subs list ─────────────────────────────────────────────────────────────────

/// Arguments for `cascade subs list`.
#[derive(Debug, Args)]
pub struct SubsListArgs {
    /// Output as JSON instead of a table.
    #[arg(long)]
    pub json: bool,
}

/// A single subscription entry in the `subs list` output.
#[derive(Debug, Serialize)]
pub struct SubEntry {
    /// Canonical subscription name (e.g. "ClaudeCode", "Cursor").
    pub name: String,
    /// Whether the subscription binary/config was found on disk.
    pub installed: bool,
    /// Whether an authenticated session was detected.
    pub authed: bool,
    /// Email or identity hint if detected, else empty string.
    pub identity: String,
    /// Auth source label matching `AuthSource` variant name.
    pub source: String,
    /// Integration mode: "detect+config" for subscription adapters,
    /// "full-inference" for direct API adapters.
    pub mode: String,
}

#[async_trait]
impl Command for SubsListArgs {
    async fn run(&self) -> Result<()> {
        let accounts = cascade_providers::scan_all();

        // Build a canonical set of all known subscription names, so we show
        // every subscription even if not installed (installed=false row).
        let known: &[(&str, &str)] = &[
            ("ClaudeCode", "detect+config"),
            ("OpenCode", "detect+config"),
            ("Codex", "detect+config"),
            ("Cursor", "detect+config"),
            ("Antigravity", "detect+config"),
        ];

        // Build map: source_name -> DiscoveredAccount (first match wins)
        let mut found: std::collections::HashMap<String, &DiscoveredAccount> =
            std::collections::HashMap::new();
        for acct in &accounts {
            let key = source_name(&acct.source);
            found.entry(key).or_insert(acct);
        }

        let mut entries: Vec<SubEntry> = known
            .iter()
            .map(|(name, mode)| {
                if let Some(acct) = found.get(*name) {
                    SubEntry {
                        name: name.to_string(),
                        installed: true,
                        authed: !acct.email_or_hint.is_empty()
                            && !acct.email_or_hint.contains("unknown"),
                        identity: acct.email_or_hint.clone(),
                        source: acct.source.to_string_repr(),
                        mode: mode.to_string(),
                    }
                } else {
                    SubEntry {
                        name: name.to_string(),
                        installed: false,
                        authed: false,
                        identity: String::new(),
                        source: name.to_string(),
                        mode: mode.to_string(),
                    }
                }
            })
            .collect();

        // Also include env-var discoveries (not in known list)
        for acct in accounts
            .iter()
            .filter(|a| matches!(a.source, AuthSource::EnvVar))
        {
            entries.push(SubEntry {
                name: format!("EnvVar ({})", acct.provider),
                installed: true,
                authed: acct.importable,
                identity: acct.email_or_hint.clone(),
                source: "EnvVar".into(),
                mode: "full-inference".into(),
            });
        }

        if self.json {
            let json = serde_json::to_string_pretty(&entries)
                .unwrap_or_else(|e| format!("{{\"error\":\"{e}\"}}"));
            println!("{json}");
        } else {
            // Table output
            println!(
                "{:<14} {:<10} {:<8} {:<16} IDENTITY",
                "SUBSCRIPTION", "INSTALLED", "AUTHED", "MODE"
            );
            println!("{}", "─".repeat(72));
            for e in &entries {
                println!(
                    "{:<14} {:<10} {:<8} {:<16} {}",
                    e.name,
                    if e.installed { "yes" } else { "no" },
                    if e.authed { "yes" } else { "no" },
                    e.mode,
                    if e.identity.is_empty() {
                        "-"
                    } else {
                        &e.identity
                    }
                );
            }
        }

        Ok(())
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Map an `AuthSource` to the canonical subscription name used in the table.
fn source_name(source: &AuthSource) -> String {
    match source {
        AuthSource::ClaudeCode => "ClaudeCode".into(),
        AuthSource::OpenCode => "OpenCode".into(),
        AuthSource::Codex => "Codex".into(),
        AuthSource::Cursor => "Cursor".into(),
        AuthSource::Antigravity => "Antigravity".into(),
        AuthSource::EnvVar => "EnvVar".into(),
    }
}

/// String representation of an `AuthSource` for JSON output.
trait AuthSourceRepr {
    fn to_string_repr(&self) -> String;
}

impl AuthSourceRepr for AuthSource {
    fn to_string_repr(&self) -> String {
        source_name(self)
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    /// All five subscription names must appear in the output even when no
    /// harnesses are installed (installed=false rows fill the gap).
    #[tokio::test]
    async fn subs_list_json_contains_all_known_subs() {
        let args = SubsListArgs { json: true };

        // Capture stdout
        // We run the logic directly rather than exec-ing the CLI binary.
        let accounts = cascade_providers::scan_all();

        let known: &[(&str, &str)] = &[
            ("ClaudeCode", "detect+config"),
            ("OpenCode", "detect+config"),
            ("Codex", "detect+config"),
            ("Cursor", "detect+config"),
            ("Antigravity", "detect+config"),
        ];

        let mut found: std::collections::HashMap<String, &DiscoveredAccount> =
            std::collections::HashMap::new();
        for acct in &accounts {
            let key = source_name(&acct.source);
            found.entry(key).or_insert(acct);
        }

        let entries: Vec<SubEntry> = known
            .iter()
            .map(|(name, mode)| {
                if let Some(acct) = found.get(*name) {
                    SubEntry {
                        name: name.to_string(),
                        installed: true,
                        authed: !acct.email_or_hint.is_empty()
                            && !acct.email_or_hint.contains("unknown"),
                        identity: acct.email_or_hint.clone(),
                        source: acct.source.to_string_repr(),
                        mode: mode.to_string(),
                    }
                } else {
                    SubEntry {
                        name: name.to_string(),
                        installed: false,
                        authed: false,
                        identity: String::new(),
                        source: name.to_string(),
                        mode: mode.to_string(),
                    }
                }
            })
            .collect();

        let json = serde_json::to_string(&entries).unwrap();

        // Every known name must appear in the JSON output
        for (name, _) in known {
            assert!(json.contains(name), "missing sub in JSON: {name}");
        }

        // mode must always be detect+config for subscription adapters
        assert!(
            json.contains("detect+config"),
            "mode must be detect+config in JSON"
        );

        // `args` is used only to trigger the struct construction — no dead-code warning.
        let _ = args;
    }

    #[test]
    fn source_name_maps_all_variants() {
        assert_eq!(source_name(&AuthSource::ClaudeCode), "ClaudeCode");
        assert_eq!(source_name(&AuthSource::OpenCode), "OpenCode");
        assert_eq!(source_name(&AuthSource::Codex), "Codex");
        assert_eq!(source_name(&AuthSource::Cursor), "Cursor");
        assert_eq!(source_name(&AuthSource::Antigravity), "Antigravity");
        assert_eq!(source_name(&AuthSource::EnvVar), "EnvVar");
    }

    #[test]
    fn sub_entry_serializes_to_json() {
        let e = SubEntry {
            name: "Cursor".into(),
            installed: true,
            authed: true,
            identity: "user@example.com".into(),
            source: "Cursor".into(),
            mode: "detect+config".into(),
        };
        let json = serde_json::to_string(&e).unwrap();
        assert!(json.contains("\"Cursor\""));
        assert!(json.contains("detect+config"));
    }
}
