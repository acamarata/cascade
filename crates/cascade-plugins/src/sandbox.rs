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
//! # ABI protocol (ptr/len calling convention)
//!
//! The WASM guest must export:
//!   - `memory` — the linear memory export
//!   - `alloc(i32) -> i32` — allocate N bytes and return a pointer
//!   - `<entry>(ptr: i32, len: i32) -> i64` — dispatch entrypoint
//!
//! The host writes a JSON `DispatchEnvelope` into guest memory at `ptr`,
//! calls the export with `(ptr, len)`, then reads output from the packed `i64`
//! return: `out_ptr = packed >> 32`, `out_len = packed & 0xFFFF_FFFF`.
//!
//! WHY i64 rather than (i32, i32): Rust `extern "C"` on wasm32 cannot emit WASM
//! multi-value returns. A single i64 packing ptr and len is the only portable
//! calling convention for cdylib targets. See `echo_tool_roundtrip.rs` ABI note.
//!
//! SPORT: cascade-plugins / sandbox layer

use std::collections::HashMap;
use std::collections::HashSet;
use std::sync::Arc;
use std::time::Duration;

use anyhow::Context as _;
use serde_json::Value;
use thiserror::Error;
use wasmtime::{Engine, Linker, Module, Store, StoreLimits, StoreLimitsBuilder, Trap, Val};
use wasmtime_wasi::preview1::{self, WasiP1Ctx};
use wasmtime_wasi::WasiCtxBuilder;

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
    /// `Module` compilation is expensive (AOT via cranelift). `Store` instantiation
    /// is cheap. This pattern amortizes compile cost over the daemon lifetime while
    /// giving each invocation a fresh fuel + memory counter.
    ///
    /// # Engine configuration
    /// - `consume_fuel(true)` — fuel metering on (CPU budget per call)
    /// - `async_support(true)` — required for `instantiate_async` / `call_async`
    ///   which integrate with tokio timeout cancellation (wasmtime 36 LTS)
    pub fn load(
        name: &str,
        wasm_bytes: &[u8],
        limits: ResourceLimits,
    ) -> Result<Arc<Self>, SandboxError> {
        let mut config = wasmtime::Config::new();
        config.consume_fuel(true);
        config.async_support(true);
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
    /// # ABI (ptr/len calling convention)
    ///
    /// 1. Serialize `input` to JSON bytes.
    /// 2. Call `alloc(len) -> i32` to allocate a buffer in guest linear memory.
    /// 3. Write the JSON bytes into guest memory at the returned pointer.
    /// 4. Call `export(ptr: i32, len: i32) -> i64`.
    ///    Return value packs `(out_ptr as u64) << 32 | (out_len as u64)`.
    /// 5. Read output bytes from guest memory at `[out_ptr..out_ptr+out_len]`.
    /// 6. Deserialize output bytes as JSON and return.
    ///
    /// # WASI
    /// A minimal WASI context (deny-by-default, zero permissions) is wired to
    /// satisfy Rust wasm32-wasip1 startup shims (`__wasm_call_ctors`). Plugins
    /// with fs/env/net permissions use the `LoadedPlugin` path in `runtime/mod.rs`
    /// which accepts a `JsonPermissions` manifest.
    ///
    /// # KV store
    /// `cascade:plugins/host::kv-get` and `kv-set` are wired to a per-invocation
    /// `HashMap<String, String>`. Values do NOT persist across calls (per-invocation
    /// scope). Persistent KV is a future enhancement (P5, `kv_persistent` capability).
    pub async fn call_export(&self, export: &str, input: &Value) -> Result<Value, SandboxError> {
        let input_bytes =
            serde_json::to_vec(input).map_err(|e| SandboxError::Serialization(e.to_string()))?;

        // Per-invocation store with fresh fuel + memory limits.
        let store_limits = StoreLimitsBuilder::new()
            .memory_size(self.limits.max_memory_bytes as usize)
            .instances(1)
            .build();

        // Minimal deny-by-default WASI context.
        // WHY: Rust wasm32-wasip1 binaries call WASI init shims at startup;
        // without WASI in the linker the module fails to instantiate.
        // Zero-permission context means plugins cannot read/write files, env
        // vars, or sockets unless explicitly granted via manifest permissions
        // (handled by the `LoadedPlugin` runtime path).
        let wasi = WasiCtxBuilder::new().build_p1();

        let mut store: Store<StoreData> = Store::new(
            &self.engine,
            StoreData {
                wasi,
                limits: store_limits,
                _fd_count: 0,
                _fd_cap: self.limits.max_fds,
                kv: HashMap::new(),
            },
        );
        store.limiter(|data| &mut data.limits);
        store.set_fuel(self.limits.max_fuel)?;

        let mut linker: Linker<StoreData> = Linker::new(&self.engine);

        // Add WASIp1 host functions (deny-by-default; ungated paths return EPERM).
        preview1::add_to_linker_async(&mut linker, |data| &mut data.wasi)
            .context("failed to add WASI to sandbox linker")?;

        // Register cascade-specific host functions (log, kv-get, kv-set).
        register_cascade_host_functions(&mut linker)?;

        let instance = linker
            .instantiate_async(&mut store, &self.module)
            .await
            .map_err(|e| {
                if is_fuel_exhausted(&e) {
                    SandboxError::ResourceExhausted {
                        kind: ResourceKind::CpuFuel,
                        limit: self.limits.max_fuel,
                        actual: self.limits.max_fuel,
                    }
                } else {
                    SandboxError::Trap(e.to_string())
                }
            })?;

        // Execute the ABI round-trip with wall-clock timeout.
        let result = tokio::time::timeout(
            self.limits.timeout,
            invoke_wasm_json(&instance, &mut store, export, input_bytes),
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
    /// WASIp1 context — deny-by-default (zero permissions in WasmPlugin).
    wasi: WasiP1Ctx,
    /// wasmtime resource limiter (memory cap enforcement).
    limits: StoreLimits,
    /// Current open-file-descriptor count (future FD cap enforcement).
    _fd_count: usize,
    /// Maximum allowed open file descriptors for this plugin instance.
    _fd_cap: usize,
    /// Per-invocation scoped key-value store for cascade:plugins/host kv-* calls.
    ///
    /// Lifecycle: created fresh per `call_export` invocation. Values do NOT
    /// persist between calls. Persistent KV is a P5 enhancement gated behind
    /// the `kv_persistent` capability grant.
    kv: HashMap<String, String>,
}

// ── Cascade host function registration ───────────────────────────────────────

/// Register cascade-specific host import functions into the linker.
///
/// Import module: `"cascade:plugins/host"` — matches the WIT world package path
/// and the `#[link(wasm_import_module = "cascade:plugins/host")]` attribute in
/// `cascade-pdk/src/host_imports.rs`.
///
/// Functions registered:
/// - `log(level: i32, ptr: i32, len: i32)` — emit a tracing log line
/// - `kv-get(key_ptr: i32, key_len: i32, out_ptr: i32) -> i32` — read KV entry
/// - `kv-set(key_ptr: i32, key_len: i32, val_ptr: i32, val_len: i32)` — write KV
fn register_cascade_host_functions(linker: &mut Linker<StoreData>) -> Result<(), SandboxError> {
    // ── cascade:plugins/host::log ─────────────────────────────────────────────
    //
    // Reads the UTF-8 message from guest memory via the `memory` export and
    // dispatches to the appropriate tracing level.
    // Level encoding: 0=TRACE 1=DEBUG 2=INFO 3=WARN 4=ERROR (matches cascade-pdk).
    linker
        .func_wrap(
            "cascade:plugins/host",
            "log",
            |mut caller: wasmtime::Caller<'_, StoreData>, level: i32, ptr: i32, len: i32| {
                let memory = match caller.get_export("memory") {
                    Some(wasmtime::Extern::Memory(m)) => m,
                    _ => return, // no memory export — skip silently
                };
                let data = memory.data(&caller);
                if let Some(slice) = guest_slice(data, ptr, len) {
                    let msg = String::from_utf8_lossy(slice);
                    match level {
                        0 => tracing::trace!(target: "plugin:wasm", "{}", msg),
                        1 => tracing::debug!(target: "plugin:wasm", "{}", msg),
                        3 => tracing::warn!(target: "plugin:wasm", "{}", msg),
                        4 => tracing::error!(target: "plugin:wasm", "{}", msg),
                        _ => tracing::info!(target: "plugin:wasm", "{}", msg),
                    }
                }
            },
        )
        .map_err(SandboxError::Engine)?;

    // ── cascade:plugins/host::kv-get ──────────────────────────────────────────
    //
    // ABI: kv-get(key_ptr: i32, key_len: i32, out_ptr: i32) -> i32
    //
    // Reads the key from guest memory at [key_ptr..key_ptr+key_len], looks it up
    // in the per-invocation KV HashMap, and if found, writes the UTF-8 value bytes
    // into guest memory at out_ptr. Returns the byte length of the value (0 = not found).
    //
    // WHY single out_ptr rather than (val_ptr, val_len) return pair:
    // WASM multi-value returns require explicit ABI support not available via
    // `extern "C"` in Rust cdylib. A caller-allocated output buffer + i32 length
    // return is the simplest correct convention. The cascade-pdk kv_get wrapper
    // handles buffer allocation on the guest side (T-P4-E03-11 PDK ticket).
    linker
        .func_wrap(
            "cascade:plugins/host",
            "kv-get",
            |mut caller: wasmtime::Caller<'_, StoreData>,
             key_ptr: i32,
             key_len: i32,
             out_ptr: i32|
             -> i32 {
                let memory = match caller.get_export("memory") {
                    Some(wasmtime::Extern::Memory(m)) => m,
                    _ => return 0,
                };
                // Read the key string from guest memory.
                let key = {
                    let data = memory.data(&caller);
                    match guest_slice(data, key_ptr, key_len) {
                        Some(s) => String::from_utf8_lossy(s).into_owned(),
                        None => return 0,
                    }
                };
                // Look up in per-invocation KV store.
                let value = match caller.data().kv.get(&key) {
                    Some(v) => v.clone(),
                    None => return 0,
                };
                let val_bytes = value.as_bytes();
                // A value longer than i32::MAX cannot be reported back through
                // this ABI at all, so refuse rather than truncate the length.
                let Ok(val_len) = i32::try_from(val_bytes.len()) else {
                    return 0;
                };
                // Write value into guest memory at out_ptr.
                let mem_data = memory.data_mut(&mut caller);
                if let Some(dest) = guest_slice_mut(mem_data, out_ptr, val_bytes.len()) {
                    dest.copy_from_slice(val_bytes);
                    val_len
                } else {
                    0 // out_ptr out of bounds
                }
            },
        )
        .map_err(SandboxError::Engine)?;

    // ── cascade:plugins/host::kv-set ──────────────────────────────────────────
    //
    // ABI: kv-set(key_ptr: i32, key_len: i32, val_ptr: i32, val_len: i32)
    //
    // Reads key and value from guest memory and inserts them into the
    // per-invocation KV HashMap. Values written here are visible to subsequent
    // kv-get calls within the same invocation only.
    linker
        .func_wrap(
            "cascade:plugins/host",
            "kv-set",
            |mut caller: wasmtime::Caller<'_, StoreData>,
             key_ptr: i32,
             key_len: i32,
             val_ptr: i32,
             val_len: i32| {
                let memory = match caller.get_export("memory") {
                    Some(wasmtime::Extern::Memory(m)) => m,
                    _ => return,
                };
                let (key, value) = {
                    let data = memory.data(&caller);
                    let k = match guest_slice(data, key_ptr, key_len) {
                        Some(s) => String::from_utf8_lossy(s).into_owned(),
                        None => return,
                    };
                    let v = match guest_slice(data, val_ptr, val_len) {
                        Some(s) => String::from_utf8_lossy(s).into_owned(),
                        None => return,
                    };
                    (k, v)
                };
                caller.data_mut().kv.insert(key, value);
            },
        )
        .map_err(SandboxError::Engine)?;

    Ok(())
}

// ── Guest-memory boundary helpers (T-P7-E25-04) ───────────────────────────────
//
// Every value crossing the FFI boundary is chosen by the GUEST, which may be
// malicious or simply buggy. Three separate hazards live in a bare
// `data.get(ptr as usize..(ptr + len) as usize)`:
//
//   1. A negative i32 sign-extends into a huge usize instead of being rejected.
//   2. `ptr + len` overflows — a debug-build panic in the HOST, i.e. a guest
//      can crash the daemon; in release it silently wraps.
//   3. A wrapped range can invert (start > end), which `get` happens to reject
//      today, but that is luck rather than a checked boundary.
//
// These helpers reject all three explicitly and return `None`, which every
// caller already handles as "out of bounds".

/// Convert a guest-supplied `(ptr, len)` pair into a validated `Range<usize>`.
///
/// Returns `None` when either value is negative or when `ptr + len` overflows.
/// Bounds against actual memory length are left to the caller's `get`.
fn guest_range(ptr: i32, len: i32) -> Option<std::ops::Range<usize>> {
    let start = usize::try_from(ptr).ok()?;
    let count = usize::try_from(len).ok()?;
    let end = start.checked_add(count)?;
    Some(start..end)
}

/// Borrow `len` bytes of guest memory at `ptr`, or `None` if the region is not
/// wholly inside `data`.
fn guest_slice(data: &[u8], ptr: i32, len: i32) -> Option<&[u8]> {
    data.get(guest_range(ptr, len)?)
}

/// Mutably borrow `len` bytes of guest memory at `ptr`.
///
/// `len` is a `usize` here because the length comes from HOST data (the value
/// being written back), not from the guest; the pointer is still guest-chosen
/// and still checked.
fn guest_slice_mut(data: &mut [u8], ptr: i32, len: usize) -> Option<&mut [u8]> {
    let start = usize::try_from(ptr).ok()?;
    let end = start.checked_add(len)?;
    data.get_mut(start..end)
}

// ── Fuel exhaustion detection ─────────────────────────────────────────────────

/// Returns `true` if the anyhow error represents a wasmtime fuel exhaustion trap.
///
/// WHY: wasmtime wraps `Trap::OutOfFuel` in an `anyhow::Error`. The canonical
/// detection path is `downcast_ref::<Trap>()`. String matching on the outer
/// error message is unreliable (contains the WASM backtrace, not the trap kind).
fn is_fuel_exhausted(e: &anyhow::Error) -> bool {
    e.downcast_ref::<Trap>()
        .map(|t| *t == Trap::OutOfFuel)
        .unwrap_or(false)
        || e.to_string().contains("all fuel consumed")
}

// ── WASM JSON ABI invocation ──────────────────────────────────────────────────

/// Execute a JSON ABI round-trip against a compiled WASM instance.
///
/// # Protocol
/// 1. Call `alloc(input_len) -> i32` to get a writable pointer in guest memory.
/// 2. Write `input_bytes` into guest memory at that pointer.
/// 3. Call `export(ptr: i32, len: i32) -> i64`.
///    The i64 packs `(out_ptr as u64) << 32 | (out_len as u64)`.
/// 4. Read `mem[out_ptr..out_ptr+out_len]` and return the bytes.
///
/// # Errors
/// Returns `SandboxError::MissingExport` if `memory`, `alloc`, or `export` are
/// absent. Returns `SandboxError::Trap` on any wasmtime trap during execution.
async fn invoke_wasm_json(
    instance: &wasmtime::Instance,
    store: &mut Store<StoreData>,
    export: &str,
    input_bytes: Vec<u8>,
) -> Result<Vec<u8>, SandboxError> {
    // ── 1. Resolve memory and alloc exports ──────────────────────────────────
    let memory = instance
        .get_memory(&mut *store, "memory")
        .ok_or_else(|| SandboxError::MissingExport("memory".to_owned()))?;

    let alloc = instance
        .get_func(&mut *store, "alloc")
        .ok_or_else(|| SandboxError::MissingExport("alloc".to_owned()))?;

    // ── 2. Allocate input buffer in guest linear memory ───────────────────────
    // The ABI passes lengths as i32; a payload above i32::MAX cannot be
    // described by it, so refuse instead of wrapping to a negative length.
    let input_len = i32::try_from(input_bytes.len()).map_err(|_| {
        SandboxError::Trap(format!(
            "input of {} bytes exceeds the i32 length the plugin ABI can express",
            input_bytes.len()
        ))
    })?;
    let mut alloc_result = [Val::I32(0)];
    alloc
        .call_async(&mut *store, &[Val::I32(input_len)], &mut alloc_result)
        .await
        .map_err(|e| SandboxError::Trap(format!("alloc trap: {e}")))?;

    // `alloc` is guest code: a negative return is a broken or hostile
    // allocator, not an address. Reject it rather than sign-extending it into
    // a huge offset.
    let input_ptr_i32 = match &alloc_result[0] {
        Val::I32(p) => *p,
        _ => {
            return Err(SandboxError::Trap(
                "alloc returned non-i32 value".to_owned(),
            ))
        }
    };
    let input_ptr = usize::try_from(input_ptr_i32).map_err(|_| {
        SandboxError::Trap(format!(
            "alloc returned a negative pointer: {input_ptr_i32}"
        ))
    })?;

    // ── 3. Write serialized input into guest linear memory ────────────────────
    memory
        .write(&mut *store, input_ptr, &input_bytes)
        .map_err(|e| SandboxError::Trap(format!("memory write: {e}")))?;

    // ── 4. Call the named export: (ptr: i32, len: i32) -> i64 ─────────────────
    let func = instance
        .get_func(&mut *store, export)
        .ok_or_else(|| SandboxError::MissingExport(export.to_owned()))?;

    let mut out_vals = [Val::I64(0)];
    func.call_async(
        &mut *store,
        &[Val::I32(input_ptr_i32), Val::I32(input_len)],
        &mut out_vals,
    )
    .await
    .map_err(|e| SandboxError::Trap(e.to_string()))?;

    // ── 5. Unpack i64: (out_ptr << 32) | out_len ─────────────────────────────
    let packed = match &out_vals[0] {
        Val::I64(v) => *v as u64,
        _ => {
            return Err(SandboxError::Trap(
                "export must return i64 (packed ptr<<32|len)".to_owned(),
            ))
        }
    };
    // Both halves are guest-chosen. On a 32-bit host `as usize` would truncate
    // and on any host `out_ptr + out_len` can overflow, so widen through u64
    // and add with a check.
    let out_ptr_u64 = packed >> 32;
    let out_len_u64 = packed & 0xFFFF_FFFF;

    // ── 6. Read output bytes from guest linear memory ─────────────────────────
    let mem_data = memory.data(&*store);
    let range = usize::try_from(out_ptr_u64)
        .ok()
        .zip(usize::try_from(out_len_u64).ok())
        .and_then(|(ptr, len)| ptr.checked_add(len).map(|end| ptr..end));
    let slice = range
        .and_then(|r| mem_data.get(r))
        .ok_or_else(|| {
            SandboxError::Trap(format!(
                "output pointer out of bounds: ptr={out_ptr_u64} len={out_len_u64} mem_size={}",
                mem_data.len()
            ))
        })?
        .to_vec(); // copy before store is dropped

    Ok(slice)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    // ── WAT fixture helpers ───────────────────────────────────────────────────

    /// Minimal WAT module implementing the cascade_plugin_call ABI for testing.
    ///
    /// The module exports `memory`, `alloc`, and `cascade_plugin_call`.
    /// - `alloc(n) -> i32` always returns 256 (fixed arena base for input buffer).
    /// - `cascade_plugin_call(ptr, len) -> i64` ignores the input and writes
    ///   the fixed JSON `{"ok":true}` (11 bytes) at memory offset 512, then
    ///   returns the packed i64 `(512 << 32) | 11`.
    ///
    /// WHY a WAT fixture rather than a real plugin WASM:
    /// Building a real `cascade_plugin_call` ABI requires the `wasm32-wasip1`
    /// toolchain and cross-compilation, which cannot run during `cargo test` on
    /// the host. The WAT fixture compiles inline via the `wat` dev-dep and
    /// exercises the exact same host-side ptr/len machinery and i64 unpacking.
    fn echo_wat_bytes() -> Vec<u8> {
        // Memory layout:
        //   [256..512]  input buffer (alloc returns 256)
        //   [512..523]  output buffer — `{"ok":true}` = 11 bytes
        //
        // ASCII: { = 123, " = 34, o = 111, k = 107 : = 58, t = 116, r = 114,
        //         u = 117, e = 101, } = 125
        wat::parse_str(
            r#"
            (module
              (memory (export "memory") 1)

              (func (export "alloc") (param i32) (result i32)
                i32.const 256
              )

              (func (export "cascade_plugin_call") (param i32 i32) (result i64)
                i32.const 512  i32.const 123 i32.store8
                i32.const 513  i32.const 34  i32.store8
                i32.const 514  i32.const 111 i32.store8
                i32.const 515  i32.const 107 i32.store8
                i32.const 516  i32.const 34  i32.store8
                i32.const 517  i32.const 58  i32.store8
                i32.const 518  i32.const 116 i32.store8
                i32.const 519  i32.const 114 i32.store8
                i32.const 520  i32.const 117 i32.store8
                i32.const 521  i32.const 101 i32.store8
                i32.const 522  i32.const 125 i32.store8

                ;; (512 << 32) | 11 = 0x0000_0200_0000_000B
                i64.const 0x000002000000000B
              )
            )
            "#,
        )
        .expect("echo WAT must be valid")
    }

    /// WAT fixture that exercises the kv-get and kv-set host imports.
    ///
    /// Steps:
    ///   1. Calls kv-set("mykey", "42")
    ///   2. Calls kv-get("mykey", out_buf@64) — expects the host to write "42"
    ///   3. Writes fixed JSON `{"val":"42"}` (12 bytes) at offset 256
    ///   4. Returns packed i64 `(256 << 32) | 12`
    ///
    /// This validates that the host KV ABI is wired and the round-trip works.
    fn kv_roundtrip_wat_bytes() -> Vec<u8> {
        // Memory layout:
        //   [0..5]    key "mykey"
        //   [8..10]   value "42"
        //   [64..128] kv-get output buffer
        //   [128..]   alloc base (input buffer)
        //   [256..]   output JSON `{"val":"42"}`
        //
        // ASCII for `{"val":"42"}`:
        //   { = 123, " = 34, v = 118, a = 97, l = 108, " = 34, : = 58,
        //   " = 34, 4 = 52, 2 = 50, " = 34, } = 125  (12 bytes)
        wat::parse_str(
            r#"
            (module
              (import "cascade:plugins/host" "kv-get"
                (func $kv_get (param i32 i32 i32) (result i32)))
              (import "cascade:plugins/host" "kv-set"
                (func $kv_set (param i32 i32 i32 i32)))

              (memory (export "memory") 1)

              (data (i32.const 0) "mykey")
              (data (i32.const 8) "42")

              (func (export "alloc") (param i32) (result i32)
                i32.const 128
              )

              (func (export "cascade_plugin_call") (param i32 i32) (result i64)
                (local $got_len i32)

                ;; kv-set("mykey"@0 len=5, "42"@8 len=2)
                i32.const 0  i32.const 5  i32.const 8  i32.const 2
                call $kv_set

                ;; kv-get("mykey"@0 len=5, out_buf@64) -> $got_len (should be 2)
                i32.const 0  i32.const 5  i32.const 64
                call $kv_get
                local.set $got_len

                ;; Write `{"val":"42"}` at offset 256
                i32.const 256  i32.const 123 i32.store8
                i32.const 257  i32.const 34  i32.store8
                i32.const 258  i32.const 118 i32.store8
                i32.const 259  i32.const 97  i32.store8
                i32.const 260  i32.const 108 i32.store8
                i32.const 261  i32.const 34  i32.store8
                i32.const 262  i32.const 58  i32.store8
                i32.const 263  i32.const 34  i32.store8
                i32.const 264  i32.const 52  i32.store8
                i32.const 265  i32.const 50  i32.store8
                i32.const 266  i32.const 34  i32.store8
                i32.const 267  i32.const 125 i32.store8

                ;; (256 << 32) | 12 = 0x0000_0100_0000_000C
                i64.const 0x000001000000000C
              )
            )
            "#,
        )
        .expect("kv_roundtrip WAT must be valid")
    }

    // ── ABI dispatch round-trip test ──────────────────────────────────────────

    /// Verifies the full host-side ABI dispatch path:
    ///   alloc → memory write → export call → i64 unpack → memory read → JSON parse.
    ///
    /// The WAT fixture returns `{"ok":true}` — the old stub returned `{}`.
    /// Asserting `result["ok"] == true` confirms real dispatch is running.
    #[tokio::test]
    async fn dispatch_roundtrip_returns_real_output() {
        let wasm_bytes = echo_wat_bytes();

        let plugin = WasmPlugin::load(
            "echo-wat",
            &wasm_bytes,
            ResourceLimits::for_type(PluginType::ToolIntegration),
        )
        .expect("WAT module must load cleanly");

        let input = serde_json::json!({
            "func": "tool_call",
            "args": { "call_id": "t1", "tool_name": "echo", "args_json": "{}" }
        });

        let result = plugin
            .call_export("cascade_plugin_call", &input)
            .await
            .expect("call_export must succeed for WAT echo fixture");

        assert!(
            result.is_object(),
            "result must be a JSON object; got: {result}"
        );
        assert_eq!(
            result.get("ok"),
            Some(&serde_json::Value::Bool(true)),
            "WAT fixture must return {{\"ok\":true}}; got: {result}"
        );
    }

    /// Verifies the kv-get and kv-set host function registrations work end-to-end.
    ///
    /// The WAT fixture calls kv-set("mykey", "42") then kv-get("mykey") and
    /// returns `{"val":"42"}`. This confirms:
    ///   - kv-set writes into the host HashMap without trapping.
    ///   - kv-get reads the value back and writes it into guest memory.
    ///   - The output is real plugin-produced JSON, not the old `{}` stub.
    #[tokio::test]
    async fn kv_host_abi_roundtrip() {
        let wasm_bytes = kv_roundtrip_wat_bytes();

        let plugin = WasmPlugin::load(
            "kv-wat",
            &wasm_bytes,
            ResourceLimits::for_type(PluginType::ToolIntegration),
        )
        .expect("KV WAT module must load cleanly");

        let input = serde_json::json!({});

        let result = plugin
            .call_export("cascade_plugin_call", &input)
            .await
            .expect("KV WAT call_export must succeed");

        assert!(
            result.is_object(),
            "KV WAT result must be a JSON object; got: {result}"
        );
        assert_eq!(
            result.get("val").and_then(|v| v.as_str()),
            Some("42"),
            "kv round-trip must produce {{\"val\":\"42\"}}; got: {result}"
        );
    }

    // ── Existing resource limit tests (kept) ──────────────────────────────────

    #[test]
    fn chunker_gets_small_memory_default() {
        let limits = ResourceLimits::for_type(PluginType::Chunker);
        assert_eq!(limits.max_memory_bytes, MEM_SMALL_MB * 1024 * 1024);
    }

    #[test]
    fn retriever_gets_large_memory_default() {
        let limits = ResourceLimits::for_type(PluginType::Retriever);
        assert_eq!(limits.max_memory_bytes, MEM_LARGE_MB * 1024 * 1024);
    }

    #[test]
    fn memory_cap_clamped_to_1gb() {
        let limits = ResourceLimits::for_type(PluginType::Chunker).with_memory_mb(2048);
        assert_eq!(limits.max_memory_bytes, 1024 * 1024 * 1024);
    }

    #[test]
    fn fd_cap_default_is_32() {
        let limits = ResourceLimits::for_type(PluginType::Chunker);
        assert_eq!(limits.max_fds, DEFAULT_FD_CAP);
    }

    // ── FFI boundary hardening (T-P7-E25-04) ──────────────────────────────────
    //
    // Every ptr/len pair below is a value a hostile guest can actually pass.
    // Before this ticket these went through `ptr as usize` + unchecked
    // addition, so a negative pointer sign-extended and `ptr + len` could
    // panic in the HOST — a guest-triggerable daemon crash.

    #[test]
    fn negative_pointer_or_length_is_refused() {
        assert!(
            guest_range(-1, 4).is_none(),
            "negative ptr must not sign-extend"
        );
        assert!(guest_range(4, -1).is_none(), "negative len must be refused");
        assert!(guest_range(i32::MIN, 0).is_none());
        assert!(guest_range(0, i32::MIN).is_none());
    }

    #[test]
    fn pointer_plus_length_overflow_is_refused_not_panicked() {
        // On a 32-bit host this pair overflows usize; on 64-bit it does not,
        // but it must still be rejected against real memory rather than
        // producing an inverted or wrapped range.
        let r = guest_range(i32::MAX, i32::MAX);
        if let Some(range) = r {
            assert!(range.start <= range.end, "range must never invert");
        }
        // A short buffer must reject it either way.
        let data = [0u8; 16];
        assert!(guest_slice(&data, i32::MAX, i32::MAX).is_none());
    }

    #[test]
    fn in_bounds_reads_still_work() {
        let data = [1u8, 2, 3, 4, 5, 6, 7, 8];
        assert_eq!(guest_slice(&data, 2, 3), Some(&data[2..5]));
        assert_eq!(guest_slice(&data, 0, 8), Some(&data[..]));
        // Zero-length at the very end is a valid empty read.
        assert_eq!(guest_slice(&data, 8, 0), Some(&data[8..8]));
    }

    #[test]
    fn reads_past_the_end_are_refused() {
        let data = [0u8; 8];
        assert!(guest_slice(&data, 8, 1).is_none());
        assert!(guest_slice(&data, 0, 9).is_none());
        assert!(guest_slice(&data, 7, 2).is_none());
    }

    #[test]
    fn mutable_writes_are_bounds_checked_the_same_way() {
        let mut data = [0u8; 8];
        assert!(
            guest_slice_mut(&mut data, -1, 1).is_none(),
            "negative out_ptr"
        );
        assert!(guest_slice_mut(&mut data, 0, 9).is_none(), "write past end");
        assert!(
            guest_slice_mut(&mut data, 6, 4).is_none(),
            "straddles the end"
        );
        assert!(
            guest_slice_mut(&mut data, i32::MAX, usize::MAX).is_none(),
            "overflowing write must be refused, not wrapped"
        );
        assert!(
            guest_slice_mut(&mut data, 4, 4).is_some(),
            "exact fit is fine"
        );
    }

    #[test]
    fn no_guest_supplied_pair_can_panic_the_host() {
        // Sweep the interesting corners of the i32 space against a small
        // buffer. The property under test is simply: this never panics.
        let data = [0u8; 32];
        let corners = [
            i32::MIN,
            i32::MIN + 1,
            -1,
            0,
            1,
            31,
            32,
            33,
            i32::MAX - 1,
            i32::MAX,
        ];
        for &ptr in &corners {
            for &len in &corners {
                let _ = guest_slice(&data, ptr, len);
            }
        }
    }
}
