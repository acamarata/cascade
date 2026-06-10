//! echo-tool — Cascade ChatTool plugin example (annotated).
//!
//! Purpose: demonstrates the `ToolIntegration` pattern — how to write a
//!   ChatTool plugin that the Cascade AI assistant can call as a tool.
//!
//! This implementation echoes its input arguments unchanged. Use it as a
//! starting point for any tool that processes JSON input and returns JSON output.
//!
//! To build:  cargo build --target wasm32-wasip1
//! To install: copy target/wasm32-wasip1/debug/echo_tool.wasm to ~/.cascade/plugins/
//! To test:   cargo test

// --- Imports -----------------------------------------------------------------
//
// Every ChatTool plugin needs these five imports:
//
// `cascade_plugin`  — attribute macro that generates the WASM ABI glue.
//                     Apply this to your plugin struct; it emits
//                     `cascade_plugin_call`, `alloc`, and `dealloc` exports.
//
// `Plugin`          — the trait you implement. Has one required method per
//                     plugin kind; all others default to `NotImplemented`.
//
// `PluginKind`      — tells the host which ABI surface to expose. For a
//                     ChatTool the host routes incoming tool calls to
//                     `on_tool_call()`.
//
// `ToolCall`        — the input: { call_id, tool_name, args_json }.
//                     `args_json` is a JSON-encoded string of the tool
//                     arguments the AI assistant chose to pass.
//
// `ToolResult`      — the output: { call_id, output, data_json? }.
//                     `output` is a string returned to the AI as the tool
//                     result. `data_json` is optional structured data.
//
// `PluginError`     — returned from `on_tool_call()` on plugin-level failure.
//                     Use `ToolResult { is_error: true }` for tool-logic errors.
use cascade_pdk::{cascade_plugin, Plugin, PluginError, PluginKind, ToolCall, ToolResult};

// --- Plugin struct ------------------------------------------------------------
//
// The struct is your plugin's type. Apply `#[cascade_plugin]` to generate
// the WASM ABI. Add configuration fields here if your tool needs them
// (e.g. API keys loaded from kv-get, or compile-time constants).
//
// This example is stateless — no fields needed.
#[cascade_plugin]
pub struct EchoTool;

// --- Plugin trait implementation ---------------------------------------------
impl Plugin for EchoTool {
    // `name()` must match the `name` field in plugin.json and be unique
    // within the plugin registry. The AI assistant sees this name when
    // deciding which tool to call.
    fn name(&self) -> &str {
        "echo"
    }

    // `kind()` must return `PluginKind::ChatTool` for tool plugins.
    // The host uses this to route `cascade_plugin_call("tool_call", ...)`.
    fn kind(&self) -> PluginKind {
        PluginKind::ChatTool
    }

    // `on_tool_call()` is the core method for ChatTool plugins.
    //
    // The host calls this every time the AI assistant invokes your tool.
    // `call.args_json` is a JSON string whose schema matches what you
    // advertise via `ToolIntegration::schema()` in the host registration.
    //
    // Return:
    //   `Ok(ToolResult { output, .. })` — success: `output` goes back to the AI.
    //   `Err(PluginError::...)` — plugin crash (rare; use sparingly).
    //   For tool-logic errors prefer: `Ok(ToolResult { output: "Error: ...", .. })`.
    fn on_tool_call(&self, call: ToolCall) -> Result<ToolResult, PluginError> {
        // Echo: return args_json unchanged as the tool output string.
        // A real tool would parse `call.args_json`, run logic, and
        // serialize the result back as a string.
        Ok(ToolResult {
            // Echo the call_id back so the host can correlate the response.
            call_id: call.call_id,
            // The tool output — sent back to the AI as the tool result.
            output: call.args_json,
            // Optional structured data (e.g. JSON for a downstream renderer).
            // Use `None` if your tool only returns a plain text result.
            data_json: None,
        })
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    // Test (a): empty object echoes back as "{}".
    #[test]
    fn call_with_empty_object_returns_empty() {
        let plugin = EchoTool;
        let call = ToolCall {
            call_id: String::from("c1"),
            tool_name: String::from("echo"),
            args_json: String::from("{}"),
        };
        let result = plugin.on_tool_call(call).unwrap();
        assert_eq!(result.output, "{}");
    }

    // Test (b): {key:val} echoes back byte-for-byte.
    #[test]
    fn call_with_kv_returns_same() {
        let plugin = EchoTool;
        let input = String::from(r#"{"key":"value"}"#);
        let call = ToolCall {
            call_id: String::from("c2"),
            tool_name: String::from("echo"),
            args_json: input.clone(),
        };
        let result = plugin.on_tool_call(call).unwrap();
        assert_eq!(result.output, input);
    }

    // Test (c): name() and kind() return expected values.
    #[test]
    fn name_and_kind_are_correct() {
        let plugin = EchoTool;
        assert_eq!(plugin.name(), "echo");
        assert_eq!(plugin.kind(), PluginKind::ChatTool);
    }
}
