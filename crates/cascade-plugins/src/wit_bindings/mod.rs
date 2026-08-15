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
//! Constraints: all WIT records are `Serialize + Deserialize`; guest pointers
//! are bounds-checked against the calling module's exported linear memory.
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

use std::path::{Path, PathBuf};
use std::time::Duration;

use rusqlite::{params, Connection, OptionalExtension};
use serde_json::Value;
use thiserror::Error;
use wasmtime::{Caller, Extern, Linker, Memory};

use crate::runtime::RuntimeStoreData;

/// Error type for WIT binding operations.
#[derive(Debug, Error)]
pub enum BindingError {
    #[error("WIT serialization error: {0}")]
    Serialize(#[from] serde_json::Error),
    #[error("WIT linker registration error: {0}")]
    Linker(String),
}

/// Errors produced by a plugin's persistent key-value store.
#[derive(Debug, Error)]
pub enum KvStoreError {
    #[error("failed to create plugin data directory '{path}': {source}")]
    CreateDir {
        path: PathBuf,
        #[source]
        source: std::io::Error,
    },
    #[error("plugin KV database error at '{path}': {source}")]
    Database {
        path: PathBuf,
        #[source]
        source: rusqlite::Error,
    },
}

/// Disk-backed key-value storage scoped to one plugin data directory.
///
/// A connection is opened per host call so concurrent invocations never share
/// a `rusqlite::Connection`. SQLite transactions provide atomic updates, while
/// the busy timeout handles short-lived write contention between invocations.
#[derive(Debug, Clone)]
pub struct PluginKvStore {
    data_dir: PathBuf,
}

impl PluginKvStore {
    /// Create a store rooted at `data_dir`.
    ///
    /// The directory and database are created lazily on the first KV operation,
    /// so loading a plugin that never uses KV does not mutate the filesystem.
    pub fn new(data_dir: impl Into<PathBuf>) -> Self {
        Self {
            data_dir: data_dir.into(),
        }
    }

    /// Return the plugin-specific data directory that owns this store.
    pub fn data_dir(&self) -> &Path {
        &self.data_dir
    }

    fn db_path(&self) -> PathBuf {
        self.data_dir.join("plugin-kv.sqlite3")
    }

    fn connect(&self) -> Result<Connection, KvStoreError> {
        std::fs::create_dir_all(&self.data_dir).map_err(|source| KvStoreError::CreateDir {
            path: self.data_dir.clone(),
            source,
        })?;

        let db_path = self.db_path();
        let conn = Connection::open(&db_path).map_err(|source| KvStoreError::Database {
            path: db_path.clone(),
            source,
        })?;
        conn.busy_timeout(Duration::from_secs(5))
            .map_err(|source| KvStoreError::Database {
                path: db_path.clone(),
                source,
            })?;
        conn.execute_batch(
            "CREATE TABLE IF NOT EXISTS plugin_kv (
                key   TEXT PRIMARY KEY NOT NULL,
                value BLOB NOT NULL
            ) WITHOUT ROWID;",
        )
        .map_err(|source| KvStoreError::Database {
            path: db_path,
            source,
        })?;
        Ok(conn)
    }

    /// Retrieve an owned value for `key`, or `None` when the key is absent.
    pub fn get(&self, key: &str) -> Result<Option<Vec<u8>>, KvStoreError> {
        let db_path = self.db_path();
        let conn = self.connect()?;
        conn.query_row(
            "SELECT value FROM plugin_kv WHERE key = ?1",
            params![key],
            |row| row.get(0),
        )
        .optional()
        .map_err(|source| KvStoreError::Database {
            path: db_path,
            source,
        })
    }

    /// Atomically insert or replace `key` with `value`.
    pub fn set(&self, key: &str, value: &[u8]) -> Result<(), KvStoreError> {
        let db_path = self.db_path();
        let conn = self.connect()?;
        conn.execute(
            "INSERT INTO plugin_kv (key, value) VALUES (?1, ?2)
             ON CONFLICT(key) DO UPDATE SET value = excluded.value",
            params![key, value],
        )
        .map_err(|source| KvStoreError::Database {
            path: db_path,
            source,
        })?;
        Ok(())
    }
}

const MAX_LOG_MESSAGE_BYTES: usize = 64 * 1024;
const MAX_KV_KEY_BYTES: usize = 1024;
const MAX_KV_VALUE_BYTES: usize = 4096;

fn guest_memory(caller: &mut Caller<'_, RuntimeStoreData>) -> anyhow::Result<Memory> {
    match caller.get_export("memory") {
        Some(Extern::Memory(memory)) => Ok(memory),
        _ => anyhow::bail!("plugin host call requires an exported linear memory named 'memory'"),
    }
}

fn read_guest_bytes(
    caller: &mut Caller<'_, RuntimeStoreData>,
    ptr: i32,
    len: i32,
    max_len: usize,
    field: &str,
) -> anyhow::Result<Vec<u8>> {
    let ptr = usize::try_from(ptr)
        .map_err(|_| anyhow::anyhow!("{field} pointer must be non-negative, got {ptr}"))?;
    let len = usize::try_from(len)
        .map_err(|_| anyhow::anyhow!("{field} length must be non-negative, got {len}"))?;
    if len > max_len {
        anyhow::bail!("{field} length {len} exceeds the {max_len}-byte host limit");
    }

    let memory = guest_memory(caller)?;
    let mut bytes = vec![0_u8; len];
    memory
        .read(&*caller, ptr, &mut bytes)
        .map_err(|error| anyhow::anyhow!("failed to read {field} from guest memory: {error}"))?;
    Ok(bytes)
}

fn read_guest_string(
    caller: &mut Caller<'_, RuntimeStoreData>,
    ptr: i32,
    len: i32,
    max_len: usize,
    field: &str,
) -> anyhow::Result<String> {
    let bytes = read_guest_bytes(caller, ptr, len, max_len, field)?;
    String::from_utf8(bytes).map_err(|error| anyhow::anyhow!("{field} is not valid UTF-8: {error}"))
}

fn write_guest_bytes(
    caller: &mut Caller<'_, RuntimeStoreData>,
    ptr: i32,
    bytes: &[u8],
    field: &str,
) -> anyhow::Result<()> {
    let ptr = usize::try_from(ptr)
        .map_err(|_| anyhow::anyhow!("{field} pointer must be non-negative, got {ptr}"))?;
    let memory = guest_memory(caller)?;
    memory
        .write(caller, ptr, bytes)
        .map_err(|error| anyhow::anyhow!("failed to write {field} to guest memory: {error}"))
}

/// Register all cascade-plugins host functions into the provided `Linker`.
///
/// Host functions exposed to WASM guests:
/// - `cascade:plugins/host::log` — emit a tracing log line (info level)
/// - `cascade:plugins/host::kv-get` — retrieve a named key from the plugin's
///   persistent, plugin-scoped KV store
/// - `cascade:plugins/host::kv-set` — persist a named key and value
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
            |mut caller: Caller<'_, RuntimeStoreData>,
             level: i32,
             ptr: i32,
             len: i32|
             -> anyhow::Result<()> {
                let message =
                    read_guest_string(&mut caller, ptr, len, MAX_LOG_MESSAGE_BYTES, "log message")?;
                let plugin_name = caller.data().plugin_name.as_str();
                match level {
                    0 => tracing::trace!(target: "plugin:wasm", plugin = %plugin_name, "{message}"),
                    1 => tracing::debug!(target: "plugin:wasm", plugin = %plugin_name, "{message}"),
                    3 => tracing::warn!(target: "plugin:wasm", plugin = %plugin_name, "{message}"),
                    4 => tracing::error!(target: "plugin:wasm", plugin = %plugin_name, "{message}"),
                    _ => tracing::info!(target: "plugin:wasm", plugin = %plugin_name, "{message}"),
                }
                Ok(())
            },
        )
        .map_err(|e| BindingError::Linker(e.to_string()))?;

    // ── kv-get ────────────────────────────────────────────────────────────────
    linker
        .func_wrap(
            "cascade:plugins/host",
            "kv-get",
            |mut caller: Caller<'_, RuntimeStoreData>,
             key_ptr: i32,
             key_len: i32,
             out_ptr: i32|
             -> anyhow::Result<i32> {
                let key =
                    read_guest_string(&mut caller, key_ptr, key_len, MAX_KV_KEY_BYTES, "KV key")?;
                let Some(value) = caller
                    .data()
                    .kv_store
                    .get(&key)
                    .map_err(anyhow::Error::new)?
                else {
                    return Ok(0);
                };
                if value.len() > MAX_KV_VALUE_BYTES {
                    anyhow::bail!(
                        "stored KV value length {} exceeds the {}-byte host limit",
                        value.len(),
                        MAX_KV_VALUE_BYTES
                    );
                }
                write_guest_bytes(&mut caller, out_ptr, &value, "KV output")?;
                i32::try_from(value.len())
                    .map_err(|_| anyhow::anyhow!("KV value length does not fit in i32"))
            },
        )
        .map_err(|e| BindingError::Linker(e.to_string()))?;

    // ── kv-set ────────────────────────────────────────────────────────────────
    linker
        .func_wrap(
            "cascade:plugins/host",
            "kv-set",
            |mut caller: Caller<'_, RuntimeStoreData>,
             key_ptr: i32,
             key_len: i32,
             val_ptr: i32,
             val_len: i32|
             -> anyhow::Result<()> {
                let key =
                    read_guest_string(&mut caller, key_ptr, key_len, MAX_KV_KEY_BYTES, "KV key")?;
                let value = read_guest_bytes(
                    &mut caller,
                    val_ptr,
                    val_len,
                    MAX_KV_VALUE_BYTES,
                    "KV value",
                )?;
                caller
                    .data()
                    .kv_store
                    .set(&key, &value)
                    .map_err(anyhow::Error::new)
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
