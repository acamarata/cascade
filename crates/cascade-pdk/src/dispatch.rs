//! Private dispatch helpers used by the `#[cascade_plugin]` generated code.
//!
//! Purpose: JSON-string ABI dispatch table — routes the `func` field in a
//!   `DispatchEnvelope` to the appropriate `Plugin` trait method, then
//!   serialises the result to a JSON string returned to the host.
//!
//! This module is `pub` only so the proc-macro's `quote!`-generated code can
//! reference it via `cascade_pdk::__private`. It is not part of the public API
//! and may change without notice.
//!
//! Constraints: no_std + alloc compatible.
//! SPORT: cascade-pdk / dispatch (T-P4-E03-10)

use serde::{Deserialize, Serialize};
use serde_json::Value;

use crate::plugin::Plugin;
use crate::types::{
    AgentContext, ChunkOpts, DataSourceFetchArgs, Document, EmbedOpts, PluginError,
    RetrieveOpts, ToolCall,
};

/// The envelope the host sends on every `cascade_plugin_call` invocation.
#[derive(Debug, Deserialize)]
pub struct DispatchEnvelope {
    /// Name of the function to invoke (e.g. `"tool_call"`, `"chunker_chunk"`).
    pub func: String,
    /// JSON-encoded arguments for the function.
    pub args: Value,
}

/// Dispatch `envelope` to the appropriate `Plugin` method and return a JSON
/// string with the serialised result (or a serialised `PluginError` on failure).
pub fn dispatch(plugin: &dyn Plugin, envelope: DispatchEnvelope) -> String {
    match envelope.func.as_str() {
        "tool_call" => {
            match serde_json::from_value::<ToolCall>(envelope.args) {
                Ok(call) => match plugin.on_tool_call(call) {
                    Ok(result) => json_ok(&result),
                    Err(e) => json_err(&e),
                },
                Err(e) => error_json(&format!("args decode: {e}")),
            }
        }
        "chunker_chunk" => {
            #[derive(Deserialize)]
            struct ChunkArgs {
                doc: Document,
                opts: ChunkOpts,
            }
            match serde_json::from_value::<ChunkArgs>(envelope.args) {
                Ok(a) => match plugin.chunk_document(a.doc, a.opts) {
                    Ok(chunks) => json_ok(&chunks),
                    Err(e) => json_err(&e),
                },
                Err(e) => error_json(&format!("args decode: {e}")),
            }
        }
        "retriever_retrieve" => {
            #[derive(Deserialize)]
            struct RetrieveArgs {
                query: String,
                opts: RetrieveOpts,
            }
            match serde_json::from_value::<RetrieveArgs>(envelope.args) {
                Ok(a) => match plugin.retrieve(a.query, a.opts) {
                    Ok(results) => json_ok(&results),
                    Err(e) => json_err(&e),
                },
                Err(e) => error_json(&format!("args decode: {e}")),
            }
        }
        "provider_embed" => {
            #[derive(Deserialize)]
            struct EmbedArgs {
                texts: Vec<String>,
                opts: EmbedOpts,
            }
            match serde_json::from_value::<EmbedArgs>(envelope.args) {
                Ok(a) => match plugin.embed(a.texts, a.opts) {
                    Ok(embeddings) => json_ok(&embeddings),
                    Err(e) => json_err(&e),
                },
                Err(e) => error_json(&format!("args decode: {e}")),
            }
        }
        "agent_run" => {
            match serde_json::from_value::<AgentContext>(envelope.args) {
                Ok(ctx) => match plugin.run_agent(ctx) {
                    Ok(resp) => json_ok(&resp),
                    Err(e) => json_err(&e),
                },
                Err(e) => error_json(&format!("args decode: {e}")),
            }
        }
        "datasource_fetch_items" => {
            match serde_json::from_value::<DataSourceFetchArgs>(envelope.args) {
                Ok(args) => match plugin.fetch_items(args) {
                    Ok(page) => json_ok(&page),
                    Err(e) => json_err(&e),
                },
                Err(e) => error_json(&format!("args decode: {e}")),
            }
        }
        "widget_render" => {
            match plugin.render() {
                Ok(data) => json_ok(&data),
                Err(e) => json_err(&e),
            }
        }
        other => error_json(&format!("unknown function: {other}")),
    }
}

/// Serialise a success value to a JSON string.
pub fn json_ok<T: Serialize>(value: &T) -> String {
    serde_json::to_string(value).unwrap_or_else(|e| error_json(&format!("serialize: {e}")))
}

/// Serialise a PluginError to a JSON string.
pub fn json_err(e: &PluginError) -> String {
    serde_json::to_string(e).unwrap_or_else(|err| error_json(&format!("serialize error: {err}")))
}

/// Produce a JSON-encoded internal error string without serde (last-resort).
pub fn error_json(msg: &str) -> String {
    // Manually escape `msg` to avoid a serde dependency in the error path.
    let escaped = msg.replace('"', "\\\"");
    format!(r#"{{"kind":"internal","message":"{escaped}"}}"#)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::types::{PluginKind, ToolResult};

    struct EchoTool;

    impl Plugin for EchoTool {
        fn name(&self) -> &str {
            "echo-tool"
        }
        fn kind(&self) -> PluginKind {
            PluginKind::ChatTool
        }
        fn on_tool_call(
            &self,
            call: ToolCall,
        ) -> Result<ToolResult, PluginError> {
            Ok(ToolResult {
                call_id: call.call_id,
                output: format!("echo: {}", call.args_json),
                data_json: None,
            })
        }
    }

    #[test]
    fn dispatch_tool_call_success() {
        let envelope = DispatchEnvelope {
            func: "tool_call".into(),
            args: serde_json::json!({
                "call_id": "c1",
                "tool_name": "echo-tool",
                "args_json": "{}"
            }),
        };
        let result = dispatch(&EchoTool, envelope);
        let v: serde_json::Value = serde_json::from_str(&result).unwrap();
        assert_eq!(v["call_id"], "c1");
        assert!(v["output"].as_str().unwrap().starts_with("echo:"));
    }

    #[test]
    fn dispatch_unknown_function_returns_error() {
        let envelope = DispatchEnvelope {
            func: "nonexistent".into(),
            args: serde_json::json!({}),
        };
        let result = dispatch(&EchoTool, envelope);
        let v: serde_json::Value = serde_json::from_str(&result).unwrap();
        assert_eq!(v["kind"], "internal");
        assert!(v["message"].as_str().unwrap().contains("unknown function"));
    }

    #[test]
    fn dispatch_not_implemented_returns_not_implemented() {
        let envelope = DispatchEnvelope {
            func: "chunker_chunk".into(),
            args: serde_json::json!({
                "doc": { "id": "d1", "content": "hello", "mime_type": null },
                "opts": { "target_tokens": 100, "overlap_tokens": 0 }
            }),
        };
        let result = dispatch(&EchoTool, envelope);
        let v: serde_json::Value = serde_json::from_str(&result).unwrap();
        assert_eq!(v["kind"], "not_implemented");
    }

    #[test]
    fn error_json_escapes_quotes() {
        let json = error_json(r#"bad "input""#);
        let v: serde_json::Value = serde_json::from_str(&json).unwrap();
        assert!(v["message"].as_str().unwrap().contains("bad"));
    }
}
