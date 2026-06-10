//! Context item types — serde structs for `.cascade/contexts/{slug}.yaml`.
//!
//! Purpose: defines the canonical schema for context sessions (named sets of
//!   pinned items: file paths, notes, URLs) persisted under `.cascade/contexts/`.
//! Inputs:  deserialized from YAML via serde_yaml.
//! Outputs: serialized to YAML via serde_yaml; exposed to Tauri IPC layer.
//! Constraints: DateTime fields are RFC3339 strings (never numeric).
//!   `ItemType` uses `#[serde(rename_all = "camelCase")]` + deny_unknown_fields
//!   is NOT set on the enum to allow forward-compatible unknown variants via
//!   `#[serde(other)]` on the Unknown variant.
//! SPORT: MASTER-COMPONENTS.md — context_pack module (T-P3-E07-08)

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};

// ── Item type enum ─────────────────────────────────────────────────────────────

/// The type of a context item.
///
/// Forward-compatible: new variants added in future schema versions will
/// deserialize as `Unknown` rather than erroring, allowing older clients
/// to round-trip YAML they don't fully understand.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub enum ItemType {
    /// A file path on the local filesystem.
    File,
    /// A free-form text note.
    Note,
    /// A URL (http/https/etc.).
    Url,
    /// An unknown variant introduced by a future schema version.
    #[serde(other)]
    Unknown,
}

// ── ContextItem ────────────────────────────────────────────────────────────────

/// A single pinned item inside a context session.
///
/// For `File` items, `path` holds the filesystem path and `content` is None.
/// For `Note` items, `content` holds the text and `path` is None.
/// For `Url` items, `path` holds the URL string and `content` is None.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ContextItem {
    /// The type discriminator for this item.
    pub item_type: ItemType,

    /// File path or URL — present for `File` and `Url` variants.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub path: Option<String>,

    /// Note body — present for `Note` variant.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub content: Option<String>,

    /// Optional human-readable label for this item.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub label: Option<String>,
}

// ── ContextSession ─────────────────────────────────────────────────────────────

/// A named context session — the root structure of each `.cascade/contexts/{slug}.yaml`.
///
/// A session is a named, ordered collection of pinned items that the user wants
/// to inject into an AI coding session. Sessions are identified by their `id`
/// (used as the filename slug) and have a human-readable `title`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ContextSession {
    /// Unique identifier — also the filename slug (no spaces or special chars).
    pub id: String,

    /// Human-readable title shown in the UI.
    pub title: String,

    /// ISO 8601 / RFC3339 timestamp of initial creation.
    pub created_at: DateTime<Utc>,

    /// ISO 8601 / RFC3339 timestamp of last modification.
    pub updated_at: DateTime<Utc>,

    /// Ordered list of pinned items in this session.
    #[serde(default)]
    pub items: Vec<ContextItem>,
}

// ── ContextMeta ───────────────────────────────────────────────────────────────

/// Lightweight metadata returned by `list_contexts` — no items body.
///
/// Purpose: allows the UI to list all sessions without reading every YAML file
/// in full. Only id, title, and item_count are populated.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ContextMeta {
    /// Session identifier (same as filename slug).
    pub id: String,

    /// Human-readable title.
    pub title: String,

    /// Number of items currently in this session.
    pub item_count: usize,
}
