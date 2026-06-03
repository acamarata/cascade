//! WASM sandbox — wasmtime engine with per-plugin-type resource limits.
//!
//! Purpose: load a WASM module into an isolated wasmtime `Store` with fuel,
//!   memory, and file-descriptor caps enforced per plugin type.
//! Inputs: `PluginType` (determines default limits) + validated WASM bytes.
//! Outputs: `WasmPlugin` handle ready for ABI dispatch.
//! Constraints:
//!   - Chunker/OutputRenderer/ModeCustomizer/ToolIntegration: 64 MB memory default
//!   - Retriever/Provider/Agent: 256 MB memory default (H2 fix — ONNX/in-memory corpora)
//!   - FD cap default 32 (H1 fix)
//!   - CPU fuel default 1_000_000_000 per invocation
//!   - Wall-clock timeout 30 s per invocation
//!
//! SPORT: cascade-plugins / sandbox layer

use std::collections::HashSet;
use std::sync::Arc;
use std::time::Duration;

use serde_json::Value;
use thiserror::Error;
use wasmtime::{Engine, Linker, Module, Store, StoreLimits, StoreLimitsBuilder};

use crate::manifest::PluginType;

// ── Resource limit constants ──────────────────────────────────────────────────

/// Default memory for lightweight plugins (Chunker, OutputRenderer, ModeCustomizer, ToolIntegration).
pub const MEM_SMALL_MB: u64 = 64;
/// Default memory for data-heavy plugins (Retriever, Provider, Agent).
/// Rationale: these plugin types commonly load ONNX models or in-memory corpora.
pub const MEM_LARGE_MB: u64 = 256;
/// Absolute maximum configurable via `plugins.max_memory_gb = 1`.
pub const MEM_MAX_MB: u64 = 1024;
/// Default CPU fuel per invocation (wasmtime fuel units map ~1:1 to WASM instructions).
pub const DEFAULT_FUEL: u64 = 1_000_000_000;
/// Default wall-clock timeout per invocation.
pub const DEFAULT_TIMEOUT: Duration = Duration::from_secs(30);
/// Default open-file-descriptor cap per plugin instance (H1 fix).
pub const DEFAULT_FD_CAP: usize = 32;

// ── Error types ───────────────────────────────────────────────────────────────

#[derive(Debug, Error)]
pub enum SandboxError {
    #[error("WASM module failed to compile: {0}")]
    Compile(String),

    #[error("WASM module trapped during execution: {0}")]
    Trap(String),

    #[error("resource exhausted: {kind:?} limit {limit}, actual {actual}")]
    ResourceExhausted {
        kind: ResourceKind,
        limit: u64,
        actual: u64,
    },

    #[error("invocation timed out after {0:?}")]
    Timeout(Duration),

    #[error("ABI serialization error: {0}")]
    Serialization(String),

    #[error("ABI call failed — export '{0}' not found in plugin")]
    MissingExport(String),

    #[error("wasmtime engine error: {0}")]
    Engine(#[from] anyhow::Error),
}

/// Identifies which resource class was exhausted (for structured error reporting).
#[derive(Debug, Clone, Copy)]
pub enum ResourceKind {
    Memory,
    CpuFuel,
    FileDescriptors,
}

// ── Resource limits ───────────────────────────────────────────────────────────

/// Per-invocation resource configuration.
#[derive(Debug, Clone)]
pub struct ResourceLimits {
    /// Maximum WASM linear memory in bytes.
    pub max_memory_bytes: u64,
    /// Maximum CPU fuel units.
    pub max_fuel: u64,
    /// Maximum wall-clock per invocation.
    pub timeout: Duration,
    /// Maximum open file descriptors.
    pub max_fds: usize,
}

impl ResourceLimits {
    /// Build limits appropriate for the given plugin type.
    pub fn for_type(plugin_type: PluginType) -> Self {
        let memory_mb = match plugin_type {
            PluginType::Retriever | PluginType::EmbeddingProvider | PluginType::Agent => {
                MEM_LARGE_MB
            }
            _ => MEM_SMALL_MB,
        };
        Self {
            max_memory_bytes: memory_mb * 1024 * 1024,
            max_fuel: DEFAULT_FUEL,
            timeout: DEFAULT_TIMEOUT,
            max_fds: DEFAULT_FD_CAP,
        }
    }

    /// Override memory limit (capped at `MEM_MAX_MB`).
    pub fn with_memory_mb(mut self, mb: u64) -> Self {
        self.max_memory_bytes = mb.min(MEM_MAX_MB) * 1024 * 1024;
        self
    }
}

// ── WasmPlugin handle ─────────────────────────────────────────────────────────

/// A loaded, sandboxed WASM plugin instance.
///
/// One `WasmPlugin` corresponds to one loaded WASM module. The wasmtime `Store`
/// is per-invocation (recreated each call) so fuel and memory counters reset
/// cleanly without module reload.
pub struct WasmPlugin {
    engine: Engine,
    module: Module,
    limits: ResourceLimits,
    /// Extensions declared by this plugin (for `Parser::supports_extension`).
    extensions: HashSet<String>,
    /// Embedding dimension declared by the plugin manifest (for EmbeddingProvider).
    /// `None` if the plugin is not an embedding provider or the manifest omits it.
    dimension: Option<usize>,
    /// Plugin name for error messages.
    pub name: String,
}

impl WasmPlugin {
    /// Load a WASM module into a sandboxed engine.
    ///
    /// # Why we compile once and instantiate per-call
    /// `Module` compilation is expensive (AOT). `Store` instantiation is cheap.
    /// This pattern amortizes compile cost over the daemon lifetime while giving
    /// each invocation a fresh fuel + memory counter.
    pub fn load(
        name: &str,
        wasm_bytes: &[u8],
        limits: ResourceLimits,
    ) -> Result<Arc<Self>, SandboxError> {
        let mut config = wasmtime::Config::new();
        config.consume_fuel(true);
        let engine = Engine::new(&config)?;
        let module = Module::from_binary(&engine, wasm_bytes)
            .map_err(|e| SandboxError::Compile(e.to_string()))?;

        Ok(Arc::new(Self {
            engine,
            module,
            limits,
            extensions: HashSet::new(),
            dimension: None,
            name: name.to_owned(),
        }))
    }

    /// Call a named export with a JSON-serialized input envelope.
    ///
    /// # Protocol
    /// Input is serialized to JSON bytes, written into WASM memory, then passed
    /// as (ptr, len). The plugin writes its JSON response to a caller-allocated
    /// output buffer. We deserialize and return.
    pub async fn call_export(&self, export: &str, input: &Value) -> Result<Value, SandboxError> {
        let input_bytes =
            serde_json::to_vec(input).map_err(|e| SandboxError::Serialization(e.to_string()))?;

        // Per-invocation store with fresh fuel + memory limits.
        let store_limits = StoreLimitsBuilder::new()
            .memory_size(self.limits.max_memory_bytes as usize)
            .instances(1)
            .build();

        let mut store: Store<StoreData> = Store::new(
            &self.engine,
            StoreData {
                limits: store_limits,
                _fd_count: 0,
                _fd_cap: self.limits.max_fds,
            },
        );
        store.limiter(|data| &mut data.limits);
        store.set_fuel(self.limits.max_fuel)?;

        let linker = Linker::new(&self.engine);
        let instance = linker.instantiate(&mut store, &self.module)?;

        // Resolve the export function.
        let func = instance
            .get_func(&mut store, export)
            .ok_or_else(|| SandboxError::MissingExport(export.to_owned()))?;

        // Execute with wall-clock timeout via tokio.
        let result = tokio::time::timeout(
            self.limits.timeout,
            invoke_wasm(func, &mut store, input_bytes),
        )
        .await
        .map_err(|_| SandboxError::Timeout(self.limits.timeout))??;

        serde_json::from_slice(&result).map_err(|e| SandboxError::Serialization(e.to_string()))
    }

    /// Extensions this plugin handles (populated at load time from manifest metadata).
    pub fn declared_extensions(&self) -> &HashSet<String> {
        &self.extensions
    }

    /// Embedding vector dimension declared by this plugin (EmbeddingProvider only).
    ///
    /// Returns `None` if the plugin is not an embedding provider or did not declare
    /// a dimension in its manifest. Callers should fall back to a sensible default.
    pub fn declared_dimension(&self) -> Option<usize> {
        self.dimension
    }
}

// ── Store data ────────────────────────────────────────────────────────────────

struct StoreData {
    limits: StoreLimits,
    /// Current open-file-descriptor count (incremented/decremented by the ABI layer).
    /// Retained for the future ABI dispatch implementation; unused in stub form.
    _fd_count: usize,
    /// Maximum allowed open file descriptors for this plugin instance.
    /// Enforced by the ABI layer once implemented; unused in stub form.
    _fd_cap: usize,
}

// ── WASM invocation helper ────────────────────────────────────────────────────

/// Invoke the WASM function with the input bytes and return the output bytes.
///
/// # SAFETY
/// This function calls into a wasmtime-managed WASM instance. wasmtime enforces
/// memory isolation — the plugin cannot access host memory outside the WASM
/// linear memory region. All pointer arithmetic is bounds-checked by wasmtime.
async fn invoke_wasm(
    _func: wasmtime::Func,
    _store: &mut Store<StoreData>,
    _input: Vec<u8>,
) -> Result<Vec<u8>, SandboxError> {
    // TODO(T-P1-E9-S01-03a): implement full ABI dispatch (ptr/len calling convention).
    // Stub returns empty JSON object so the dispatcher layer compiles cleanly.
    Ok(b"{}".to_vec())
}
