//! # cascade_core::security
//!
//! Security infrastructure for the Cascade runtime.
//!
//! ## Module layout
//!
//! | Module | Responsibility |
//! |--------|---------------|
//! | [`validator`] | Input validation: path traversal, prompt injection, command injection |
//! | [`sanitizer`] | Output sanitization: secret exfil scanning, malicious command guards |
//! | [`keychain`] | OS keychain integration — implements `KeyStorage` from `cascade-types` |
//! | [`audit`] | Tamper-evident hash-chain audit log |
//! | [`env_allowlist`] | Env var injection guard applied at daemon startup |
//! | [`oauth`] | OAuth callback HMAC state validation |
//! | [`mcp_envelope`] | MCP JSON-RPC 2025-03 envelope schema validation |
//! | [`secrets_scanner`] | Regex + entropy patterns for cloud provider access keys and secrets |
//! | [`rag_poisoning`] | RAG ingestion/retrieval scan profiles against prompt injection |
//!
//! ## STRIDE coverage
//!
//! Each module maps to one or more STRIDE letters:
//!
//! - **S** Spoofing: `keychain`, `oauth` (identity verification)
//! - **T** Tampering: `validator`, `mcp_envelope` (input integrity)
//! - **R** Repudiation: `audit` (append-only tamper-evident log)
//! - **I** Info disclosure: `sanitizer`, `secrets_scanner` (output scanning)
//! - **D** Denial of service: `validator` (fuel/resource guards), `env_allowlist`
//! - **E** Elevation: `env_allowlist`, `mcp_envelope` (capability boundary)
//!
//! ## Usage
//!
//! Wire the `ScanPipeline` from [`validator`] into the tool-call ingestion path,
//! and `OutputSanitizer` from [`sanitizer`] into the agent response path.
//! The `AuditLogger` from [`audit`] should receive events from both pipelines.
//!
//! ```rust,no_run
//! use cascade_core::security::{validator, sanitizer, audit};
//! ```

pub mod audit;
pub mod env_allowlist;
pub mod keychain;
pub mod mcp_envelope;
pub mod oauth;
pub mod rag_poisoning;
pub mod sanitizer;
pub mod secrets_scanner;
pub mod validator;

// Re-export the most-used surface so callers can write
// `use cascade_core::security::ScanPipeline` etc.
pub use audit::{AuditEvent, AuditLogger};
pub use env_allowlist::EnvVarAllowlist;
pub use keychain::build_key_storage;
pub use sanitizer::{OutputSanitizer, SanitizationResult};
pub use validator::{ScanPipeline, ValidationContext, ValidationError};
