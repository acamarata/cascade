//! WASM plugin runtime — wasmtime engine with fuel limits and WASI capability gating.
//!
//! Purpose: provide `PluginSandbox`, the primary entry point for loading and calling
//!   WASM plugins. Wraps wasmtime with:
//!   - Fuel-metered execution (CPU budget per call).
//!   - Memory limits (per plugin type via `ResourceLimits`).
//!   - WASIp1 context built exclusively from the permissions declared in
//!     `plugin.json` / `cascade-plugin.toml` — DENY-by-default for fs, net, env.
//!   - Trap and fuel-exhaustion paths mapped to typed `PluginError`.
//!
//! Inputs:  WASM bytes + `PluginManifest` or `PluginJsonManifest` (permissions).
//! Outputs: `LoadedPlugin` handle; `PluginSandbox::call` returns `serde_json::Value`.
//! Constraints:
//!   - No unsafe blocks in this module (wasmtime's unsafe is inside the crate).
//!   - All capability gates are checked at load time (build_wasi_ctx).
//!   - One Store per call — fuel and memory reset cleanly each invocation.
//!   - Net access at WASI level: always disabled (P4 sockets via capability TBD).
//!
//! SPORT: cascade-plugins / runtime module (T-P4-E03-04)

pub mod wasi_ctx;

use std::time::Duration;

use anyhow::Context as _;
use serde_json::Value;
use thiserror::Error;
use wasmtime::{Engine, Linker, Module, Store, StoreLimits, StoreLimitsBuilder, Trap, Val};
use wasmtime_wasi::preview1::{self, WasiP1Ctx};

use crate::manifest::{JsonPermissions, PluginManifest, PluginType};
use crate::sandbox::ResourceLimits;

pub use wasi_ctx::build_wasi_ctx;

// ── Constants ────────────────────────────────────────────────────────────────

/// Default CPU fuel per call (wasmtime fuel units ≈ 1:1 WASM instructions).
pub const PLUGIN_FUEL_LIMIT: u64 = 1_000_000_000;

/// Default wall-clock timeout per invocation.
pub const PLUGIN_CALL_TIMEOUT: Duration = Duration::from_secs(30);

// ── Error type ───────────────────────────────────────────────────────────────

/// Errors produced by the plugin runtime.
#[derive(Debug, Error)]
pub enum PluginError {
    /// The WASM module failed to compile.
    #[error("plugin compile error: {0}")]
    Compile(String),

    /// The WASM module trapped (panic/abort/OOB) during execution.
    #[error("plugin trap: {0}")]
    Trap(String),

    /// The plugin exhausted its CPU fuel budget.
    #[error("plugin resource exhausted: fuel limit {limit} exceeded")]
    ResourceExhausted { limit: u64 },

    /// The call timed out.
    #[error("plugin call timed out after {0:?}")]
    Timeout(Duration),

    /// A named export was not found in the compiled module.
    #[error("plugin missing export '{0}'")]
    MissingExport(String),

    /// Serialization error on the JSON ABI boundary.
    #[error("plugin ABI serialization error: {0}")]
    Serialize(String),

    /// WASI context construction failed (usually a bad preopen path).
    #[error("plugin WASI context error: {0}")]
    WasiContext(String),

    /// Underlying wasmtime / anyhow error.
    #[error("plugin engine error: {0}")]
    Engine(#[from] anyhow::Error),
}

// ── Store data ────────────────────────────────────────────────────────────────

// ── Fuel exhaustion detection ─────────────────────────────────────────────────

/// Returns true if the anyhow error represents a wasmtime fuel exhaustion trap.
///
/// WHY: wasmtime wraps the `Trap::OutOfFuel` in an `anyhow::Error`. The
/// canonical way to detect this is via `downcast_ref::<Trap>()`. The
/// `to_string()` of the outer error contains the WASM backtrace, not the
/// trap kind — so string matching on the outer message is unreliable.
fn is_fuel_exhausted(e: &anyhow::Error) -> bool {
    e.downcast_ref::<Trap>()
        .map(|t| *t == Trap::OutOfFuel)
        .unwrap_or(false)
        || e.to_string().contains("all fuel consumed")
}

/// Per-invocation store data: WASI state + memory limiter.
pub struct RuntimeStoreData {
    /// WASIp1 context — built per-call from the manifest permissions.
    pub wasi: WasiP1Ctx,
    /// wasmtime resource limiter (memory cap enforcement).
    pub limits: StoreLimits,
}

// ── LoadedPlugin ─────────────────────────────────────────────────────────────

/// A compiled, sandboxed plugin module ready for invocation.
///
/// The wasmtime `Engine` and `Module` are shared across calls (compiled once).
/// Each `call()` creates a fresh `Store` with reset fuel and memory counters.
pub struct LoadedPlugin {
    /// Plugin identifier (from manifest name field).
    pub name: String,
    /// Shared wasmtime engine (fuel + async configured).
    engine: Engine,
    /// Pre-compiled WASM module (AOT via cranelift).
    module: Module,
    /// Resource limits for this plugin instance.
    limits: ResourceLimits,
    /// Permissions snapshot used to rebuild the WASI context per call.
    permissions: JsonPermissions,
    /// Fuel limit override (defaults to `PLUGIN_FUEL_LIMIT`).
    fuel_limit: u64,
}

impl LoadedPlugin {
    /// Fuel limit for this plugin's calls.
    pub fn fuel_limit(&self) -> u64 {
        self.fuel_limit
    }
}

// ── PluginSandbox ─────────────────────────────────────────────────────────────

/// Primary entry point for loading and executing sandboxed WASM plugins.
///
/// # Security model
/// - Fuel metering: `PLUGIN_FUEL_LIMIT` instructions per call (configurable).
/// - Memory: capped by `ResourceLimits::for_type(plugin_type)`.
/// - WASI: deny-by-default; only paths/env/net declared in `permissions` are
///   exposed via the WASI context.
/// - Network (WASI level): disabled entirely in this release; tcp/udp sockets
///   from WASI are gated via a future `net_outbound` capability token.
///
/// # Example
/// ```no_run
/// # use cascade_plugins::runtime::PluginSandbox;
/// # use cascade_plugins::manifest::{PluginManifest, PluginType};
/// # use cascade_plugins::sandbox::ResourceLimits;
/// let sandbox = PluginSandbox::new(PluginType::Chunker);
/// // let plugin = sandbox.load(wasm_bytes, &manifest).unwrap();
/// // let result = plugin.call("hello", &[]).await.unwrap();
/// ```
pub struct PluginSandbox {
    /// wasmtime engine shared across all plugins loaded by this sandbox.
    engine: Engine,
    /// Default resource limits (overridable per manifest).
    default_limits: ResourceLimits,
}

impl PluginSandbox {
    /// Construct a new sandbox for the given plugin type.
    ///
    /// Builds the wasmtime `Engine` with:
    /// - `consume_fuel(true)` — fuel metering on
    /// - `async_support(true)` — needed for tokio timeout integration
    pub fn new(plugin_type: PluginType) -> Result<Self, PluginError> {
        let mut config = wasmtime::Config::new();
        config.consume_fuel(true);
        config.async_support(true);

        let engine = Engine::new(&config)?;
        let default_limits = ResourceLimits::for_type(plugin_type);

        Ok(Self {
            engine,
            default_limits,
        })
    }

    /// Load a WASM module and associate it with the given manifest permissions.
    ///
    /// Compiles the module once (AOT via cranelift). Instantiation and WASI
    /// context construction happen per-call in `LoadedPlugin::call_export`.
    pub fn load(
        &self,
        wasm_bytes: &[u8],
        manifest: &PluginManifest,
    ) -> Result<LoadedPlugin, PluginError> {
        let module = Module::from_binary(&self.engine, wasm_bytes)
            .map_err(|e| PluginError::Compile(e.to_string()))?;

        Ok(LoadedPlugin {
            name: manifest.name.clone(),
            engine: self.engine.clone(),
            module,
            limits: self.default_limits.clone(),
            permissions: JsonPermissions::default(),
            fuel_limit: PLUGIN_FUEL_LIMIT,
        })
    }

    /// Load a WASM module with JSON manifest permissions (public plugin contract).
    ///
    /// Unlike `load`, this variant sources capability gates from a `plugin.json`
    /// manifest's `permissions` field (fs paths, net hosts, env var names).
    pub fn load_with_permissions(
        &self,
        wasm_bytes: &[u8],
        name: &str,
        permissions: JsonPermissions,
        fuel_limit: Option<u64>,
    ) -> Result<LoadedPlugin, PluginError> {
        let module = Module::from_binary(&self.engine, wasm_bytes)
            .map_err(|e| PluginError::Compile(e.to_string()))?;

        Ok(LoadedPlugin {
            name: name.to_owned(),
            engine: self.engine.clone(),
            module,
            limits: self.default_limits.clone(),
            permissions,
            fuel_limit: fuel_limit.unwrap_or(PLUGIN_FUEL_LIMIT),
        })
    }
}

// ── LoadedPlugin: call_export ─────────────────────────────────────────────────

impl LoadedPlugin {
    /// Call a named exported function on the WASM module.
    ///
    /// # ABI
    /// The function is called with the provided `args` (wasmtime `Val` slice).
    /// For the JSON-string-passing ABI used by dispatcher traits, callers write
    /// input into a pre-allocated buffer via `alloc` + `memory`, then call the
    /// export with (ptr, len) args. This low-level variant exposes raw `Val`
    /// args for simple exports (e.g. the `hello` fixture that takes no args).
    ///
    /// For the full JSON ABI, see `call_export_json` below.
    ///
    /// # Fuel exhaustion
    /// If the module runs out of fuel, wasmtime returns an error whose message
    /// contains "out of fuel". This is mapped to `PluginError::ResourceExhausted`.
    pub async fn call(&self, func_name: &str, args: &[Val]) -> Result<Vec<Val>, PluginError> {
        let store_limits = StoreLimitsBuilder::new()
            .memory_size(self.limits.max_memory_bytes as usize)
            .instances(1)
            .build();

        // Build WASI context from permissions (deny-by-default).
        let wasi = build_wasi_ctx(&self.permissions)
            .map_err(|e| PluginError::WasiContext(e.to_string()))?;

        let mut store: Store<RuntimeStoreData> = Store::new(
            &self.engine,
            RuntimeStoreData {
                wasi,
                limits: store_limits,
            },
        );
        store.limiter(|data| &mut data.limits);
        store
            .set_fuel(self.fuel_limit)
            .context("failed to set fuel")?;

        let mut linker: Linker<RuntimeStoreData> = Linker::new(&self.engine);

        // Add WASIp1 host functions to the linker (deny-by-default context
        // means the WASI functions exist but return EPERM for ungated paths).
        preview1::add_to_linker_async(&mut linker, |data| &mut data.wasi)
            .context("failed to add WASI to linker")?;

        let instance = linker
            .instantiate_async(&mut store, &self.module)
            .await
            .map_err(|e| {
                if is_fuel_exhausted(&e) {
                    PluginError::ResourceExhausted {
                        limit: self.fuel_limit,
                    }
                } else {
                    PluginError::Trap(e.to_string())
                }
            })?;

        let func = instance
            .get_func(&mut store, func_name)
            .ok_or_else(|| PluginError::MissingExport(func_name.to_owned()))?;

        let mut results = vec![Val::I32(0); func.ty(&store).results().len()];

        let call_result = tokio::time::timeout(
            self.limits.timeout,
            func.call_async(&mut store, args, &mut results),
        )
        .await
        .map_err(|_| PluginError::Timeout(self.limits.timeout))?;

        call_result.map_err(|e| {
            if is_fuel_exhausted(&e) {
                PluginError::ResourceExhausted {
                    limit: self.fuel_limit,
                }
            } else {
                PluginError::Trap(e.to_string())
            }
        })?;

        Ok(results)
    }

    /// Call a JSON-ABI export: write `input` JSON into WASM memory via `alloc`,
    /// call `func_name(ptr, len) -> (out_ptr, out_len)`, read the output slice,
    /// and deserialise the result.
    ///
    /// This is the wire protocol used by the trait dispatchers in `types.rs`.
    /// Requires the module to export `alloc(i32) -> i32` and `memory`.
    pub async fn call_export_json(
        &self,
        func_name: &str,
        input: &Value,
    ) -> Result<Value, PluginError> {
        let input_bytes =
            serde_json::to_vec(input).map_err(|e| PluginError::Serialize(e.to_string()))?;

        let store_limits = StoreLimitsBuilder::new()
            .memory_size(self.limits.max_memory_bytes as usize)
            .instances(1)
            .build();

        let wasi = build_wasi_ctx(&self.permissions)
            .map_err(|e| PluginError::WasiContext(e.to_string()))?;

        let mut store: Store<RuntimeStoreData> = Store::new(
            &self.engine,
            RuntimeStoreData {
                wasi,
                limits: store_limits,
            },
        );
        store.limiter(|data| &mut data.limits);
        store
            .set_fuel(self.fuel_limit)
            .context("failed to set fuel")?;

        let mut linker: Linker<RuntimeStoreData> = Linker::new(&self.engine);
        preview1::add_to_linker_async(&mut linker, |data| &mut data.wasi)
            .context("failed to add WASI to linker")?;

        let instance = linker
            .instantiate_async(&mut store, &self.module)
            .await
            .map_err(|e| {
                if is_fuel_exhausted(&e) {
                    PluginError::ResourceExhausted {
                        limit: self.fuel_limit,
                    }
                } else {
                    PluginError::Trap(e.to_string())
                }
            })?;

        // ── Write input into WASM linear memory ──────────────────────────────
        let memory = instance
            .get_memory(&mut store, "memory")
            .ok_or_else(|| PluginError::MissingExport("memory".to_owned()))?;

        let alloc = instance
            .get_func(&mut store, "alloc")
            .ok_or_else(|| PluginError::MissingExport("alloc".to_owned()))?;

        let input_len = input_bytes.len() as i32;
        let mut alloc_result = [Val::I32(0)];
        alloc
            .call_async(&mut store, &[Val::I32(input_len)], &mut alloc_result)
            .await
            .map_err(|e| PluginError::Trap(e.to_string()))?;

        let input_ptr = match &alloc_result[0] {
            Val::I32(p) => *p as usize,
            _ => return Err(PluginError::Trap("alloc returned non-i32".to_owned())),
        };

        memory
            .write(&mut store, input_ptr, &input_bytes)
            .map_err(|e| PluginError::Trap(format!("memory write: {e}")))?;

        // ── Call the export ───────────────────────────────────────────────────
        let func = instance
            .get_func(&mut store, func_name)
            .ok_or_else(|| PluginError::MissingExport(func_name.to_owned()))?;

        // cascade_plugin_call returns a single i64 with (ptr << 32) | len.
        // The guest uses extern "C" -> i64; multi-value (i32, i32) requires
        // WASM multi-value ABI which Rust cdylib does not emit via extern "C".
        let mut out_vals = [Val::I64(0)];
        let call_result = tokio::time::timeout(
            self.limits.timeout,
            func.call_async(
                &mut store,
                &[Val::I32(input_ptr as i32), Val::I32(input_len)],
                &mut out_vals,
            ),
        )
        .await
        .map_err(|_| PluginError::Timeout(self.limits.timeout))?;

        call_result.map_err(|e| {
            if is_fuel_exhausted(&e) {
                PluginError::ResourceExhausted {
                    limit: self.fuel_limit,
                }
            } else {
                PluginError::Trap(e.to_string())
            }
        })?;

        // ── Read output from WASM linear memory ───────────────────────────────
        let packed = match &out_vals[0] {
            Val::I64(v) => *v as u64,
            _ => {
                return Err(PluginError::Trap(
                    "export must return i64 (packed ptr<<32|len)".to_owned(),
                ))
            }
        };
        let out_ptr = (packed >> 32) as usize;
        let out_len = (packed & 0xFFFF_FFFF) as usize;

        let mem_data = memory.data(&store);
        let slice = mem_data
            .get(out_ptr..out_ptr + out_len)
            .ok_or_else(|| PluginError::Trap("output pointer out of bounds".to_owned()))?;

        serde_json::from_slice(slice).map_err(|e| PluginError::Serialize(e.to_string()))
    }
}
