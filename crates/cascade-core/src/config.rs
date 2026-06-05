//! Injection-policy configuration for the CASCADE.md loader.
//!
//! # Purpose
//! Provides the `InjectionPolicy` enum and `InjectionConfig` struct that callers
//! can pass to `check_injection` to opt into blocking mode or declare allowlisted
//! content paths.
//!
//! # Inputs
//! Constructed by the caller; serialisable via serde so it can be loaded from a
//! project-level `cascade.toml` or equivalent config file.
//!
//! # Outputs
//! Read-only config values consumed by `loader::check_injection`.
//!
//! # Constraints
//! - Default policy is `Advisory` — preserves P2 advisory-only behaviour.
//! - Allowlist uses exact-string matching (not regex) to avoid ReDoS risk.
//!
//! # SPORT
//! MASTER-SECURITY.md — security/loader: injection_policy

use serde::{Deserialize, Serialize};

/// Governs how the loader reacts when a prompt-injection pattern is detected.
///
/// # Variants
/// - `Advisory` (default) — emit `tracing::warn!` and continue loading.  No
///   caller action is required; detection is purely observability.
/// - `Blocking` — refuse to load the instruction file and return
///   `Err(InjectionError::Blocked)`.  Opt-in; never the default.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "lowercase")]
pub enum InjectionPolicy {
    /// Warn and continue (default; no breaking change from P2).
    #[default]
    Advisory,
    /// Reject the file with an error when any injection pattern is found.
    Blocking,
}

/// Configuration for prompt-injection detection in the CASCADE.md loader.
///
/// # Fields
/// - `policy` — `advisory` (default) or `blocking`.
/// - `allowlist` — list of exact content strings that are trusted and bypass
///   detection entirely.  Exact-string comparison only; never regex.
///
/// # Example
/// ```rust
/// use cascade_core::config::{InjectionConfig, InjectionPolicy};
///
/// let cfg = InjectionConfig {
///     policy: InjectionPolicy::Blocking,
///     allowlist: vec!["# trusted legacy instruction".to_owned()],
/// };
/// ```
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct InjectionConfig {
    /// Detection policy applied when patterns are found.
    #[serde(default)]
    pub policy: InjectionPolicy,

    /// Exact content strings exempted from detection.
    ///
    /// When the full content of a CASCADE.md file exactly matches one of these
    /// entries, `check_injection` returns `Ok(())` without running patterns.
    /// Uses exact-string matching only — no regex — to avoid ReDoS risk in the
    /// allowlist itself.
    #[serde(default)]
    pub allowlist: Vec<String>,
}

impl Default for InjectionConfig {
    fn default() -> Self {
        Self {
            policy: InjectionPolicy::Advisory,
            allowlist: Vec::new(),
        }
    }
}
