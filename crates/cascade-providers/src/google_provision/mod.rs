//! # google_provision
//!
//! GCP project + API key provisioning engine.
//!
//! ## Purpose
//! Implements the three provisioning modes for adding Google accounts to the
//! Gemini Pool wizard step (T-P3-E03-39b):
//! - `FullAuto`: creates GCP project → enables generativelanguage API → creates API key.
//! - `Guided`: returns Cloud Console deep-link URLs for each step.
//! - `Manual`: accepts a pasted API key and validates it via `validate_gemini_key`.
//!
//! ## Sub-modules
//! - `types`: error types, options, checkpoint, and multi-result structs.
//! - `client`: `GoogleProvisionClient` with all provisioning methods.

// ── Base URL constants (overridable in tests via wiremock) ────────────────────

/// Resource Manager API base URL.
pub const RESOURCE_MANAGER_BASE: &str = "https://cloudresourcemanager.googleapis.com";
/// Service Usage API base URL.
pub const SERVICE_USAGE_BASE: &str = "https://serviceusage.googleapis.com";
/// API Keys API base URL.
pub const APIKEYS_BASE: &str = "https://apikeys.googleapis.com";

// ── Sub-modules ───────────────────────────────────────────────────────────────

pub mod client;
pub mod types;

// ── Public re-exports (preserve existing API surface) ────────────────────────

pub use cascade_types::provision::{ProvisionMode, ProvisionRequest, ProvisionResult};
pub use client::GoogleProvisionClient;
pub use types::{
    ProvisionError, ProvisionMultiResult, ProvisionOptions, ProvisionedKey, ProvisioningCheckpoint,
};
