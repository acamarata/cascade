//! echo-tool WASM round-trip integration test.
//!
//! Purpose: load the compiled echo-tool.wasm through `PluginSandbox` and verify
//!   a real `cascade_plugin_call` round-trip via `call_export_json`. This test
//!   validates that the i64-packed ABI is correctly aligned between the macro
//!   (guest) and the runtime (host).
//!
//! ABI note (documented here as the canonical record):
//!   The macro emits `pub extern "C" fn cascade_plugin_call(...) -> i64` where
//!   the return encodes `(ptr as u64) << 32 | (len as u64)`.
//!   Rust cdylib targeting wasm32 cannot emit multi-value (i32, i32) returns via
//!   extern "C"; a single i64 is the correct and only feasible ABI.
//!   The fix was applied to the HOST side (runtime/mod.rs + test_harness.rs):
//!   `out_vals = [Val::I64(0)]`, unpacked as `ptr = packed >> 32`, `len = packed & 0xFFFF_FFFF`.
//!
//! Inputs:  target/wasm32-wasip1/debug/echo_tool.wasm (must be pre-built).
//! Outputs: asserts round-trip response contains expected call_id and echoed args_json.
//! Constraints:
//!   - Skips if the WASM binary is not present (CI without wasm target skips cleanly).
//!   - No network, no filesystem preopens, no env — zero capabilities needed.
//!   - Uses `call_export_json` with a `DispatchEnvelope` matching the macro's dispatcher.
//!
//! SPORT: cascade-plugins / echo-tool roundtrip (T-P4-E03-12)

use cascade_plugins::{
    manifest::{JsonPermissions, PluginType},
    runtime::PluginSandbox,
};
use serde_json::json;

// ── Helper ────────────────────────────────────────────────────────────────────

/// Path to the debug build of echo_tool.wasm relative to the workspace root.
///
/// We resolve via env variable CARGO_MANIFEST_DIR which is set at compile time
/// by cargo to the location of this crate's Cargo.toml.
fn echo_tool_wasm_path() -> std::path::PathBuf {
    // CARGO_MANIFEST_DIR = .../cascade/crates/cascade-plugins
    let manifest_dir = std::path::PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    manifest_dir
        .join("..") // crates/
        .join("..") // cascade/
        .join("target")
        .join("wasm32-wasip1")
        .join("debug")
        .join("echo_tool.wasm")
}

// ── Tests ─────────────────────────────────────────────────────────────────────

/// Full round-trip: load echo_tool.wasm → call cascade_plugin_call with a
/// `tool_call` dispatch envelope → assert output echoes args_json.
///
/// The dispatch envelope format is `{ "func": "<name>", "args": { ... } }`
/// as defined by the `cascade_plugin` macro's dispatch table.
#[tokio::test]
async fn echo_tool_roundtrip_call() {
    let wasm_path = echo_tool_wasm_path();

    // Skip cleanly if the WASM binary has not been built (e.g. CI host without
    // wasm32-wasip1 toolchain). The companion CI job builds it first.
    if !wasm_path.exists() {
        eprintln!(
            "SKIP: echo_tool.wasm not found at {:?} — run `cargo build -p echo-tool --target wasm32-wasip1` first",
            wasm_path
        );
        return;
    }

    let wasm_bytes =
        std::fs::read(&wasm_path).unwrap_or_else(|e| panic!("failed to read {:?}: {e}", wasm_path));

    // Build sandbox with zero permissions (echo-tool needs none).
    let sandbox =
        PluginSandbox::new(PluginType::ToolIntegration).expect("PluginSandbox::new must succeed");

    let plugin = sandbox
        .load_with_permissions(&wasm_bytes, "echo", JsonPermissions::default(), None)
        .expect("load_with_permissions must succeed for echo-tool");

    // Build a DispatchEnvelope matching the macro's dispatcher:
    //   { "func": "tool_call", "args": { "call_id": ..., "tool_name": ..., "args_json": ... } }
    let args_json_inner = r#"{"key":"hello","value":"world"}"#;
    let input = json!({
        "func": "tool_call",
        "args": {
            "call_id": "test-c1",
            "tool_name": "echo",
            "args_json": args_json_inner
        }
    });

    let result = plugin
        .call_export_json("cascade_plugin_call", &input)
        .await
        .expect("call_export_json must not return an error");

    // echo-tool returns: { "call_id": "test-c1", "output": "<args_json_inner>", "data_json": null }
    assert_eq!(
        result["call_id"], "test-c1",
        "call_id must be echoed back; got result: {result}"
    );
    assert_eq!(
        result["output"], args_json_inner,
        "output must equal args_json verbatim; got result: {result}"
    );
    // data_json is None → JSON null
    assert!(
        result["data_json"].is_null(),
        "data_json must be null; got: {}",
        result["data_json"]
    );
}

/// Verify that an unknown `func` in the dispatch envelope returns a JSON error
/// rather than trapping. This exercises the dispatch error path in the macro.
#[tokio::test]
async fn echo_tool_unknown_func_returns_error_json() {
    let wasm_path = echo_tool_wasm_path();
    if !wasm_path.exists() {
        return; // same skip guard
    }

    let wasm_bytes = std::fs::read(&wasm_path).unwrap();
    let sandbox = PluginSandbox::new(PluginType::ToolIntegration).unwrap();
    let plugin = sandbox
        .load_with_permissions(&wasm_bytes, "echo", JsonPermissions::default(), None)
        .unwrap();

    let input = json!({
        "func": "nonexistent_method",
        "args": {}
    });

    // Should succeed at the ABI level but return an error JSON object.
    let result = plugin.call_export_json("cascade_plugin_call", &input).await;

    // Either it returns Ok(error_json) or Err(Serialize). Either is acceptable;
    // a Trap here would indicate the macro is unwinding instead of returning JSON.
    match result {
        Ok(v) => {
            // The dispatch error_json path should have produced something JSON-parseable.
            assert!(!v.is_null(), "error JSON must not be null");
        }
        Err(cascade_plugins::runtime::PluginError::Serialize(_)) => {
            // Acceptable: dispatch returned non-JSON bytes (shouldn't happen with error_json,
            // but not a trap so the ABI round-trip itself worked).
        }
        Err(e) => {
            panic!("unexpected error from unknown-func call (should not trap): {e}");
        }
    }
}
