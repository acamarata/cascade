//! OpenAI Chat Completions adapter.
//!
//! # Purpose
//!
//! Implements [`ProviderAdapter`] for the OpenAI Chat Completions API
//! (`POST /v1/chat/completions`).  Supports GPT-4o, GPT-4o-mini, gpt-4-turbo,
//! gpt-3.5-turbo, o1, o1-mini, o3, o4-mini, and text-davinci-003 (Codex-compatible).
//!
//! # Module layout
//!
//! - `types`   — private wire types (request/response/stream/model-list JSON shapes)
//! - `helpers` — constants and `model_context_window` helper
//! - `adapter` — `OpenAIAdapter` struct, `ProviderAdapter` impl, and tests

mod helpers;
mod types;

pub mod adapter;

#[cfg(test)]
mod tests;

pub use adapter::OpenAIAdapter;
