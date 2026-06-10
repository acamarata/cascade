//! echo-tool — Cascade ChatTool plugin.
//!
//! Purpose: minimal `ToolIntegration` reference implementation that returns its
//!   input JSON arguments unchanged as `ToolResult::output`. Demonstrates the
//!   `#[cascade_plugin]` macro, the `Plugin` trait, and the PDK test harness.
//!
//! Inputs:  `ToolCall` — `call_id`, `tool_name`, `args_json` (any JSON object string).
//! Outputs: `ToolResult` — `output` = the original `args_json` string verbatim.
//! Constraints: no filesystem, network, or environment access required.
//! SPORT: plugins / echo-tool (T-P4-E03-17)

use cascade_pdk::{cascade_plugin, Plugin, PluginError, PluginKind, ToolCall, ToolResult};

/// Echo-tool plugin struct.
///
/// Stateless — all inputs come through `ToolCall`; no fields needed.
#[cascade_plugin]
pub struct EchoTool;

impl Plugin for EchoTool {
    /// Returns `"echo"` — the unique identifier shown in `cascade plugin list`.
    fn name(&self) -> &str {
        "echo"
    }

    /// Declares this plugin as a `ChatTool` so the host routes tool-call
    /// invocations to `on_tool_call()`.
    fn kind(&self) -> PluginKind {
        PluginKind::ChatTool
    }

    /// Echo implementation: returns `args_json` unchanged as the tool output.
    ///
    /// This is a byte-for-byte pass-through — the output string equals the
    /// input `call.args_json` exactly, with no re-serialisation.
    fn on_tool_call(&self, call: ToolCall) -> Result<ToolResult, PluginError> {
        Ok(ToolResult {
            call_id: call.call_id,
            output: call.args_json,
            data_json: None,
        })
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    // Test (a): call with empty JSON object returns empty object string.
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

    // Test (b): call with {key:val} returns the same JSON string verbatim.
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
        // Byte-for-byte round-trip verified here.
        assert_eq!(result.output, input);
    }

    // Test (c): name() and description() return expected strings.
    #[test]
    fn name_and_kind_are_correct() {
        let plugin = EchoTool;
        assert_eq!(plugin.name(), "echo");
        assert_eq!(plugin.kind(), PluginKind::ChatTool);
    }
}
