//! Re-export shim: the real implementation lives in `crate::chunk::code`.
//!
//! This module exists only to satisfy the `mod code_chunk;` declaration in
//! `lib.rs` that was added during scaffolding.  All new code should import
//! from `cascade_rag::chunk::code` directly.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::code_chunk (absorbed into chunk::code)
