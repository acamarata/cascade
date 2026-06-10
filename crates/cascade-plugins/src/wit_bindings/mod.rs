//! Host-side WIT bindings for cascade-plugins.
//!
//! Purpose: provide Rust type definitions that mirror the WIT records declared in
//!   `crates/cascade-plugins/wit/*.wit`, plus host-function registration helpers
//!   that wire cascade-types trait objects into the wasmtime `Linker` so WASM
//!   guests can call back into the host.
//!
//! # Architecture
//! The WIT files in `wit/` define the public contract. This module provides:
//! 1. Rust structs that mirror each WIT record (serialisable, round-trippable).
//! 2. `register_host_functions` — adds host-callable functions to a `Linker<T>`.
//! 3. Type-mapping helpers: WIT record <-> JSON ABI bytes used by dispatchers.
//!
//! # Code generation note
//! Full `wit-bindgen`-generated code is gated behind the `wit-codegen` feature
//! (see Cargo.toml). In the current release, bindings are hand-written to avoid
//! a build-time CLI dependency. The generated output would be identical in
//! structure — `wit-bindgen rust` produces the same record shapes from the WIT
//! source. The `build.rs` re-generates bindings when a `.wit` file changes.
//!
//! Inputs:  WIT source files in `wit/`
//! Outputs: Rust types + Linker registration functions
//! Constraints: all WIT records are `Serialize + Deserialize`; no raw pointers.
//! SPORT: cascade-plugins / WIT bindings (T-P4-E03-05)

pub mod agent;
pub mod chunker;
pub mod provider;
pub mod retriever;
pub mod tool_integration;

// Re-export all record types for ergonomic use by callers.
pub use agent::{AgentContext, AgentError, AgentResponse, ContextEntry, ToolCallRequest};
pub use chunker::{Chunk, ChunkMeta, ChunkOpts, ChunkerError, Document};
pub use provider::{EmbedOpts, Embedding, ProviderError};
pub use retriever::{RetrievalResult, RetrieveOpts, RetrieverError};
pub use tool_integration::{ToolCall, ToolError, ToolResult};

use serde_json::Value;
use thiserror::Error;
use wasmtime::Linker;

use crate::runtime::RuntimeStoreData;

/// Error type for WIT binding operations.
#[derive(Debug, Error)]
pub enum BindingError {
    #[error("WIT serialization error: {0}")]
    Serialize(#[from] serde_json::Error),
    #[error("WIT linker registration error: {0}")]
    Linker(String),
}

/// Register all cascade-plugins host functions into the provided `Linker`.
///
/// Host functions exposed to WASM guests:
/// - `cascade:plugins/host::log` — emit a tracing log line (info level)
/// - `cascade:plugins/host::kv-get` — retrieve a named key from the plugin's
///   scoped KV store (stub: always returns empty string in this release)
/// - `cascade:plugins/host::kv-set` — store a named key (stub: no-op in P4)
///
/// Net/HTTP fetch host functions are gated by manifest `net` permissions and
/// are registered separately via `register_http_fetch` (planned for P4 PDK).
///
/// # Module and function naming
/// Imports are named `cascade:plugins/host` to match the WIT world package path.
/// WASM guests that import these must declare:
/// ```wat
/// (import "cascade:plugins/host" "log" (func ...))
/// ```
pub fn register_host_functions(linker: &mut Linker<RuntimeStoreData>) -> Result<(), BindingError> {
    // ── log ──────────────────────────────────────────────────────────────────
    linker
        .func_wrap(
            "cascade:plugins/host",
            "log",
            |_caller: wasmtime::Caller<'_, RuntimeStoreData>, _level: i32, _ptr: i32, _len: i32| {
                // WHY: we don't read WASM memory here because this is a
                // demonstration stub. Real implementation reads the UTF-8 string
                // from linear memory using the Caller handle and emits via tracing.
                // Full implementation deferred to T-P4-E03-11 (PDK wave).
                tracing::info!("[plugin:wasm] log call received (stub)");
            },
        )
        .map_err(|e| BindingError::Linker(e.to_string()))?;

    // ── kv-get ────────────────────────────────────────────────────────────────
    linker
        .func_wrap(
            "cascade:plugins/host",
            "kv-get",
            |_caller: wasmtime::Caller<'_, RuntimeStoreData>,
             _key_ptr: i32,
             _key_len: i32,
             _out_ptr: i32|
             -> i32 {
                // Stub: KV store not implemented in P4 baseline. Returns 0 (empty).
                0
            },
        )
        .map_err(|e| BindingError::Linker(e.to_string()))?;

    // ── kv-set ────────────────────────────────────────────────────────────────
    linker
        .func_wrap(
            "cascade:plugins/host",
            "kv-set",
            |_caller: wasmtime::Caller<'_, RuntimeStoreData>,
             _key_ptr: i32,
             _key_len: i32,
             _val_ptr: i32,
             _val_len: i32| {
                // Stub: no-op in P4 baseline.
            },
        )
        .map_err(|e| BindingError::Linker(e.to_string()))?;

    Ok(())
}

/// Deserialise a JSON `Value` into a WIT binding type `T`.
///
/// Used by the dispatcher layer to convert WASM output bytes into typed structs.
pub fn from_json<T: serde::de::DeserializeOwned>(v: Value) -> Result<T, BindingError> {
    serde_json::from_value(v).map_err(BindingError::from)
}

/// Serialise a WIT binding type `T` into a JSON `Value`.
///
/// Used by the dispatcher layer to convert typed structs into WASM input bytes.
pub fn to_json<T: serde::Serialize>(v: &T) -> Result<Value, BindingError> {
    serde_json::to_value(v).map_err(BindingError::from)
}
