//! # cascade_rag::memory
//!
//! Purpose: RAG-08 memory module — episodic memory, factual memory, recall, and
//!   chat history with hard namespace isolation and personal-firewall enforcement.
//!
//! ## Namespace scopes
//! - `"personal"` — local-only, blocked from MCP/HTTP unless `opt_in = true`
//! - `"dev-<slug>"` — per-project developer RAG (e.g. "dev-nself")
//! - `"meta"` — cascade's own self-knowledge
//!
//! ## Modules
//! - `namespace` — validation + personal firewall (`PersonalFirewallError`)
//! - `episode`   — insert / delete episodic memory (raw observations)
//! - `fact`      — upsert / decay / consolidate factual memory (BLAKE3 dedup)
//! - `recall`    — namespace-scoped keyword recall across episodes + facts
//!
//! ## Guarantee
//! Every SQL query in this module includes `WHERE namespace = ?`. Cross-namespace
//! leakage is architecturally impossible at the query level.

pub mod chat;
pub mod episode;
pub mod fact;
pub mod namespace;
pub mod recall;

// ── Re-exports ─────────────────────────────────────────────────────────────────

pub use chat::{clear_chat, insert_chat, list_chat, ChatMessage};
pub use episode::{count_episodes, delete_episode, insert_episode};
pub use fact::{
    archive_fact, consolidate_namespace, decay_namespace, upsert_fact, CONSOLIDATION_THRESHOLD,
};
pub use namespace::{validate, validate_with_firewall, NamespaceError, ValidatedNamespace};
pub use recall::{recall, RecallHit};
