//! Privacy enforcement layer for cascade-rag.
//!
//! Contains:
//! - [`redaction`] — strip PII/secrets from content before indexing.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::privacy (T-RAG-07)

pub mod redaction;

pub use redaction::{RedactionConfig, RedactionPipeline};
