//! # cascade_core::library::types
//!
//! Serde structs for the Cascade library item schema (T-P3-E07-01).
//!
//! ## Purpose
//!
//! Defines [`LibraryItem`], the canonical on-disk YAML representation of a
//! prompt, agent, persona, or slash-command stored in
//! `~/.cascade/library/{type}/{slug}.yaml`.
//!
//! ## Design notes
//!
//! - The `item_type` field uses `#[serde(rename = "type")]` to match the YAML
//!   key `type` (Rust reserved word avoidance).
//! - `metadata` is an open `serde_json::Map` so unknown type-specific keys
//!   round-trip without loss.
//! - All fields use `snake_case` names on disk (serde default) and
//!   `camelCase` over the Tauri IPC wire.
//! - Every field carries `#[serde(default)]` to tolerate partial files.
//!
//! ## SPORT
//!
//! MASTER-COMPONENTS.md — LibraryItem schema (T-P3-E07-01/02)

use serde::{Deserialize, Serialize};

/// The four item types stored in the Cascade library.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum LibraryItemKind {
    /// A reusable prompt text.
    #[default]
    Prompt,
    /// An agent definition with optional tool list.
    Agent,
    /// A persona definition with system prompt and tone.
    Persona,
    /// A slash-command shortcut.
    SlashCommand,
}

impl LibraryItemKind {
    /// Returns the subdirectory name for this item kind.
    pub fn dir_name(&self) -> &'static str {
        match self {
            LibraryItemKind::Prompt => "prompts",
            LibraryItemKind::Agent => "agents",
            LibraryItemKind::Persona => "personas",
            LibraryItemKind::SlashCommand => "slash-commands",
        }
    }

    /// Parse from directory name string.
    pub fn from_dir_name(s: &str) -> Option<Self> {
        match s {
            "prompts" => Some(LibraryItemKind::Prompt),
            "agents" => Some(LibraryItemKind::Agent),
            "personas" => Some(LibraryItemKind::Persona),
            "slash-commands" => Some(LibraryItemKind::SlashCommand),
            _ => None,
        }
    }
}

/// A single library item — prompt, agent, persona, or slash-command.
///
/// On disk: `~/.cascade/library/{type}/{slug}.yaml`.
/// Over IPC: serialised as JSON with `camelCase` field names.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub struct LibraryItem {
    /// URL-safe slug — unique within the item's type directory.
    #[serde(default)]
    pub id: String,

    /// Item type discriminator.
    #[serde(rename = "type", default)]
    pub item_type: LibraryItemKind,

    /// Human-readable title.
    #[serde(default)]
    pub title: String,

    /// One-line description.
    #[serde(default)]
    pub description: String,

    /// Primary instruction / prompt text.
    #[serde(default)]
    pub content: String,

    /// Taxonomy tags (empty list allowed).
    #[serde(default)]
    pub tags: Vec<String>,

    /// Semantic version string, e.g. `"1.0.0"`.
    #[serde(default)]
    pub version: String,

    /// RFC 3339 creation timestamp.
    #[serde(default)]
    pub created_at: String,

    /// RFC 3339 last-update timestamp.
    #[serde(default)]
    pub updated_at: String,

    /// Harness targets — subset of `["cc", "oc", "codex"]`.
    #[serde(default)]
    pub harness_targets: Vec<String>,

    /// Type-specific metadata (open map; forward-compatible).
    #[serde(default)]
    pub metadata: serde_json::Map<String, serde_json::Value>,
}
