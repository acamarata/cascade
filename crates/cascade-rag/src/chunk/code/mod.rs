//! Code-aware chunker: function/class-level AST extraction via tree-sitter.
//!
//! Parses source files using tree-sitter grammars and extracts function/method/
//! class nodes as individual [`Chunk`]s.  Each chunk carries rich metadata:
//! symbol name, kind, language, doc-comment, file path, and 1-based start/end lines.
//!
//! ## Language support
//!
//! | Extension | Language |
//! |-----------|----------|
//! | `.rs`     | Rust (`function_item`, `impl_item`) |
//! | `.ts`, `.tsx` | TypeScript (`function_declaration`, `method_definition`, `class_declaration`) |
//! | `.js`, `.jsx` | JavaScript (`function_declaration`, `method_definition`, `class_declaration`) |
//! | `.py`     | Python (`function_definition`, `class_definition`) |
//!
//! ## Fallback
//!
//! Files with unsupported extensions fall back to [`SemanticChunker`] with a
//! `warn!` log.  Malformed or unparseable files also fall back — never panic.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::chunk::CodeChunker

mod async_impl;
mod chunker;
mod language;
mod tests;
mod ts_impl;

// ── Public re-exports (preserve original paths) ───────────────────────────────

pub use async_impl::AsyncCodeChunker;
pub use chunker::CodeChunker;
pub use language::{detect_language_for, SourceLanguage};
