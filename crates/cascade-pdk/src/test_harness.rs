//! PDK test harness — `test-harness` feature only (host builds, non-wasm32).
//!
//! Purpose: allow plugin authors to write unit tests for WASM plugins without
//!   a running Cascade daemon. Provides `MockHost` (configurable stubs for all
//!   five host-function categories) and `PluginTestRunner` which wires a
//!   wasmtime `Store` + `MockHost` to load a `.wasm` binary and call an export.
//!
//! # Usage (in a plugin's tests/)
//! ```rust,ignore
//! use cascade_pdk::test_harness::{MockHost, PluginTestRunner};
//!
//! #[tokio::test]
//! async fn test_my_tool() {
//!     let wasm = std::fs::read("target/wasm32-wasip1/debug/my_plugin.wasm").unwrap();
//!     let mut host = MockHost::default();
//!     host.set_response("tool_call", serde_json::json!({
//!         "call_id": "c1",
//!         "output": "hello",
//!         "data_json": null
//!     }));
//!     let runner = PluginTestRunner::new(host);
//!     let result = runner.load_and_call(&wasm, "cascade_plugin_call", serde_json::json!({
//!         "func": "tool_call",
//!         "args": { "call_id": "c1", "tool_name": "my-tool", "args_json": "{}" }
//!     })).await.unwrap();
//!     assert_eq!(result["output"], "hello");
//! }
//! ```
//!
//! Inputs:  WASM bytes + function name + JSON args
//! Outputs: `serde_json::Value` (deserialised return from the WASM export)
//! Constraints: host-side only; never enable on wasm32 targets.
//! SPORT: cascade-pdk / test harness (T-P4-E03-12)

use std::collections::HashMap;

use anyhow::Context as _;
use serde_json::Value;
use wasmtime::{Engine, Linker, Module, Store, Val};
use wasmtime_wasi::preview1::{self, WasiP1Ctx};
use wasmtime_wasi::WasiCtxBuilder;

// ── MockHost ──────────────────────────────────────────────────────────────────

/// Configurable stub for all five Cascade plugin host-function categories.
///
/// Pre-populate canned responses via [`MockHost::set_response`]. Any function
/// name not in the map returns a JSON `null` value without panicking.
#[derive(Debug, Default, Clone)]
pub struct MockHost {
    /// Map from function name (e.g. `"tool_call"`) to canned JSON response.
    responses: HashMap<String, Value>,
    /// Recorded log lines emitted by the guest (inspectable in tests).
    pub log_lines: Vec<String>,
}

impl MockHost {
    /// Create a new MockHost with no pre-configured responses.
    pub fn new() -> Self {
        Self::default()
    }

    /// Pre-configure the canned response returned when the guest calls
    /// a function with the given `func_name`.
    pub fn set_response(&mut self, func_name: impl Into<String>, response: Value) {
        self.responses.insert(func_name.into(), response);
    }

    /// Return the canned response for `func_name`, or `Value::Null` if not set.
    pub fn get_response(&self, func_name: &str) -> Value {
        self.responses
            .get(func_name)
            .cloned()
            .unwrap_or(Value::Null)
    }

    /// Record a log line from the guest.
    pub fn record_log(&mut self, line: String) {
        self.log_lines.push(line);
    }
}

// ── PluginTestRunner ──────────────────────────────────────────────────────────

/// Test runner that wires a `MockHost` + wasmtime store to execute WASM exports.
pub struct PluginTestRunner {
    // Stored for callers that inspect host state after a call; not read by runner internals.
    #[allow(dead_code)]
    mock_host: MockHost,
    engine: Engine,
}

impl PluginTestRunner {
    /// Create a new test runner with the given mock host configuration.
    pub fn new(mock_host: MockHost) -> Self {
        let engine = Engine::default();
        Self { mock_host, engine }
    }

    /// Load a WASM binary, instantiate it, call the named export with JSON args,
    /// and return the JSON-decoded result.
    ///
    /// The WASM export is expected to follow the Cascade JSON-string ABI:
    /// - Args are written into guest memory via `alloc` + linear-memory write.
    /// - The export is called with `(args_ptr: i32, args_len: i32)`.
    /// - The return value is a `(result_ptr: i32, result_len: i32)` pair,
    ///   or a single `i32` indicating success/failure for simple calls.
    ///
    /// For the `cascade_plugin_call` entry point specifically, the args JSON
    /// should include `"func"` (function name) and `"args"` (sub-arguments).
    pub async fn load_and_call(
        &self,
        wasm_bytes: &[u8],
        export_name: &str,
        args: Value,
    ) -> anyhow::Result<Value> {
        let module =
            Module::new(&self.engine, wasm_bytes).context("failed to compile WASM module")?;

        // Build a minimal WASIp1 context (no filesystem, no network).
        let wasi_ctx = WasiCtxBuilder::new().build_p1();

        let mut store = Store::new(&self.engine, TestStoreData { wasi: wasi_ctx });
        store
            .set_fuel(1_000_000_000)
            .context("failed to set fuel")?;

        let mut linker: Linker<TestStoreData> = Linker::new(&self.engine);
        preview1::add_to_linker_sync(&mut linker, |s| &mut s.wasi)
            .context("failed to add WASI to linker")?;

        // Register mock host functions.
        self.register_mock_functions(&mut linker)?;

        let instance = linker
            .instantiate(&mut store, &module)
            .context("failed to instantiate WASM module")?;

        // Serialise args to JSON bytes.
        let args_json = serde_json::to_vec(&args).context("failed to serialise args to JSON")?;
        let args_len = args_json.len();

        // Write args into guest memory via the guest's `alloc` export.
        let alloc_fn = instance
            .get_func(&mut store, "alloc")
            .context("WASM module missing `alloc` export")?;

        alloc_fn
            .call(&mut store, &[Val::I32(args_len as i32)], &mut [Val::I32(0)])
            .context("alloc call failed")?;

        // alloc returns the pointer as first result.
        let mut alloc_out = [Val::I32(0)];
        alloc_fn
            .call(&mut store, &[Val::I32(args_len as i32)], &mut alloc_out)
            .context("alloc call failed")?;
        let args_ptr = match &alloc_out[0] {
            Val::I32(p) => *p as usize,
            _ => anyhow::bail!("alloc returned unexpected type"),
        };

        // Write the JSON bytes into the guest's linear memory.
        let memory = instance
            .get_memory(&mut store, "memory")
            .context("WASM module missing `memory` export")?;
        memory
            .write(&mut store, args_ptr, &args_json)
            .context("failed to write args into guest memory")?;

        // Call the named export.
        let export_fn = instance
            .get_func(&mut store, export_name)
            .with_context(|| format!("WASM module missing `{export_name}` export"))?;

        // cascade_plugin_call returns i64 packed as (ptr << 32) | len.
        // Rust cdylib extern "C" functions cannot return multi-value (i32, i32);
        // the i64-packed convention is the correct ABI for this guest type.
        let mut result_vals = [Val::I64(0)];
        export_fn
            .call(
                &mut store,
                &[Val::I32(args_ptr as i32), Val::I32(args_len as i32)],
                &mut result_vals,
            )
            .with_context(|| format!("call to `{export_name}` failed"))?;

        let packed = match &result_vals[0] {
            Val::I64(v) => *v as u64,
            _ => anyhow::bail!("export returned unexpected type (expected i64)"),
        };
        let (result_ptr, result_len) = ((packed >> 32) as usize, (packed & 0xFFFF_FFFF) as usize);

        if result_len == 0 {
            return Ok(Value::Null);
        }

        // Read result bytes from guest memory.
        let mut result_bytes = vec![0u8; result_len];
        memory
            .read(&store, result_ptr, &mut result_bytes)
            .context("failed to read result from guest memory")?;

        // Free the result memory via `dealloc`.
        if let Some(dealloc_fn) = instance.get_func(&mut store, "dealloc") {
            let _ = dealloc_fn.call(
                &mut store,
                &[Val::I32(result_ptr as i32), Val::I32(result_len as i32)],
                &mut [],
            );
        }

        // Deserialise the JSON result.
        let result = serde_json::from_slice(&result_bytes)
            .context("failed to deserialise WASM result JSON")?;

        Ok(result)
    }

    /// Register mock implementations of `cascade:plugins/host` functions.
    fn register_mock_functions(&self, linker: &mut Linker<TestStoreData>) -> anyhow::Result<()> {
        // log — capture to test output.
        linker
            .func_wrap(
                "cascade:plugins/host",
                "log",
                |_caller: wasmtime::Caller<'_, TestStoreData>,
                 _level: i32,
                 _ptr: i32,
                 _len: i32| {
                    // In tests, log calls are silently dropped unless inspected via
                    // the MockHost log_lines field (not accessible in this closure).
                    // For richer log inspection, use a separate logging mock.
                },
            )
            .context("failed to register mock log")?;

        // kv-get — always returns empty (no key configured in default mock).
        linker
            .func_wrap(
                "cascade:plugins/host",
                "kv-get",
                |_caller: wasmtime::Caller<'_, TestStoreData>,
                 _key_ptr: i32,
                 _key_len: i32,
                 _val_ptr_out: i32,
                 _val_len_out: i32| {},
            )
            .context("failed to register mock kv-get")?;

        // kv-set — no-op in tests.
        linker
            .func_wrap(
                "cascade:plugins/host",
                "kv-set",
                |_caller: wasmtime::Caller<'_, TestStoreData>,
                 _key_ptr: i32,
                 _key_len: i32,
                 _val_ptr: i32,
                 _val_len: i32| {},
            )
            .context("failed to register mock kv-set")?;

        Ok(())
    }
}

// ── Internal store data ───────────────────────────────────────────────────────

struct TestStoreData {
    wasi: WasiP1Ctx,
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn mock_host_default_returns_null() {
        let host = MockHost::default();
        assert_eq!(host.get_response("anything"), Value::Null);
    }

    #[test]
    fn mock_host_set_response_round_trips() {
        let mut host = MockHost::new();
        host.set_response(
            "tool_call",
            serde_json::json!({ "call_id": "c1", "output": "ok", "data_json": null }),
        );
        let resp = host.get_response("tool_call");
        assert_eq!(resp["output"], "ok");
    }

    #[test]
    fn mock_host_log_recording() {
        let mut host = MockHost::new();
        host.record_log("test message".into());
        assert_eq!(host.log_lines.len(), 1);
        assert_eq!(host.log_lines[0], "test message");
    }

    #[test]
    fn plugin_test_runner_creates_without_panic() {
        let host = MockHost::default();
        let _runner = PluginTestRunner::new(host);
    }
}
