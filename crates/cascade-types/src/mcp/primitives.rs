//! # MCP 2025-03-26 primitive schema types
//!
//! Data-transfer types for the five MCP primitive domains: resources, tools,
//! prompts, sampling, and logging. These are wire-format types only — no handler
//! logic lives here.
//!
//! All structs use `#[serde(rename_all = "camelCase")]` for spec-compliant JSON.

use serde::{Deserialize, Serialize};
use serde_json::Value;

// ═══════════════════════════════════════════════════════════════════════════════
// === Resources
// ═══════════════════════════════════════════════════════════════════════════════

/// Annotations that can be attached to a resource or template.
#[derive(Debug, Clone, PartialEq, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Annotations {
    /// Intended audience roles for this resource.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub audience: Option<Vec<Role>>,
    /// Priority hint (0.0 = lowest, 1.0 = highest).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub priority: Option<f64>,
}

/// A resource exposed by the MCP server.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Resource {
    /// Unique URI identifying the resource.
    pub uri: String,
    /// Human-readable name.
    pub name: String,
    /// Optional description.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
    /// MIME type of the resource content.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub mime_type: Option<String>,
    /// Optional annotations.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub annotations: Option<Annotations>,
}

/// A URI template for dynamic resources (RFC 6570).
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ResourceTemplate {
    /// RFC 6570 URI template, e.g. `"file:///{path}"`.
    pub uri_template: String,
    /// Human-readable name.
    pub name: String,
    /// Optional description.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
    /// MIME type of resources matched by this template.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub mime_type: Option<String>,
    /// Optional annotations.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub annotations: Option<Annotations>,
}

/// Contents of a resource: either UTF-8 text or base64-encoded binary.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(untagged)]
pub enum ResourceContents {
    /// UTF-8 text content.
    Text(TextResourceContents),
    /// Binary content encoded as base64.
    Blob(BlobResourceContents),
}

/// Text (UTF-8) resource contents.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TextResourceContents {
    /// URI of the resource.
    pub uri: String,
    /// MIME type (defaults to `text/plain`).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub mime_type: Option<String>,
    /// UTF-8 text content.
    pub text: String,
}

/// Binary resource contents, base64-encoded per MCP spec.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct BlobResourceContents {
    /// URI of the resource.
    pub uri: String,
    /// MIME type of the binary data.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub mime_type: Option<String>,
    /// Base64-encoded binary content (standard encoding, no line breaks).
    pub blob: String,
}

/// `resources/list` request parameters.
#[derive(Debug, Clone, PartialEq, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ListResourcesRequest {
    /// Pagination cursor from a previous response.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub cursor: Option<String>,
}

/// `resources/list` result.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ListResourcesResult {
    /// List of available resources.
    pub resources: Vec<Resource>,
    /// Cursor for the next page (absent if no more pages).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub next_cursor: Option<String>,
}

/// `resources/read` request parameters.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ReadResourceRequest {
    /// URI of the resource to read.
    pub uri: String,
}

/// `resources/read` result.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ReadResourceResult {
    /// Resource contents (one or more parts).
    pub contents: Vec<ResourceContents>,
}

/// `resources/subscribe` request.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SubscribeRequest {
    /// URI to subscribe to.
    pub uri: String,
}

/// `resources/unsubscribe` request.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct UnsubscribeRequest {
    /// URI to unsubscribe from.
    pub uri: String,
}

/// `notifications/resources/updated` notification parameters.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ResourceUpdatedNotification {
    /// URI of the updated resource.
    pub uri: String,
}

// ═══════════════════════════════════════════════════════════════════════════════
// === Tools
// ═══════════════════════════════════════════════════════════════════════════════

/// A tool callable by the MCP client.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Tool {
    /// Tool name (unique within the server).
    pub name: String,
    /// Optional human-readable description.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
    /// JSON Schema describing the tool's input parameters.
    pub input_schema: Value,
}

/// `tools/list` request parameters.
#[derive(Debug, Clone, PartialEq, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ListToolsRequest {
    /// Pagination cursor.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub cursor: Option<String>,
}

/// `tools/list` result.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ListToolsResult {
    /// Available tools.
    pub tools: Vec<Tool>,
    /// Cursor for next page.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub next_cursor: Option<String>,
}

/// `tools/call` request parameters.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct CallToolRequest {
    /// Name of the tool to call.
    pub name: String,
    /// Arguments matching the tool's `inputSchema`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub arguments: Option<Value>,
}

/// `tools/call` result.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct CallToolResult {
    /// One or more result content blocks.
    pub content: Vec<ToolResultContent>,
    /// `true` if the tool invocation resulted in an error.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub is_error: Option<bool>,
}

/// A single piece of tool output: text, image, or an embedded resource.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "camelCase")]
pub enum ToolResultContent {
    /// Plain text output.
    #[serde(rename = "text")]
    Text(TextContent),
    /// Image output (base64-encoded).
    #[serde(rename = "image")]
    Image(ImageContent),
    /// An embedded resource returned inline.
    #[serde(rename = "resource")]
    EmbeddedResource(EmbeddedResource),
}

/// Plain text content block.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TextContent {
    /// UTF-8 text.
    pub text: String,
}

/// Image content block (base64-encoded).
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ImageContent {
    /// Base64-encoded image data.
    pub data: String,
    /// MIME type, e.g. `"image/png"`.
    pub mime_type: String,
}

/// A resource embedded inline in a tool result or prompt message.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct EmbeddedResource {
    /// The resource contents.
    pub resource: ResourceContents,
}

// ═══════════════════════════════════════════════════════════════════════════════
// === Prompts
// ═══════════════════════════════════════════════════════════════════════════════

/// A named prompt template offered by the server.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Prompt {
    /// Prompt name (unique within the server).
    pub name: String,
    /// Optional description of the prompt.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
    /// Template arguments the prompt accepts.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub arguments: Option<Vec<PromptArgument>>,
}

/// A named argument accepted by a prompt template.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PromptArgument {
    /// Argument name.
    pub name: String,
    /// Optional description.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
    /// Whether the argument is required (`true`) or optional.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub required: Option<bool>,
}

/// `prompts/list` request.
#[derive(Debug, Clone, PartialEq, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ListPromptsRequest {
    /// Pagination cursor.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub cursor: Option<String>,
}

/// `prompts/list` result.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ListPromptsResult {
    /// Available prompts.
    pub prompts: Vec<Prompt>,
    /// Cursor for next page.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub next_cursor: Option<String>,
}

/// `prompts/get` request.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct GetPromptRequest {
    /// Name of the prompt to retrieve.
    pub name: String,
    /// Argument values to fill into the template.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub arguments: Option<Value>,
}

/// `prompts/get` result — the rendered prompt messages.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct GetPromptResult {
    /// Optional description of the rendered prompt.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
    /// The rendered conversation messages.
    pub messages: Vec<PromptMessage>,
}

/// A single message in a prompt conversation.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PromptMessage {
    /// Message author role.
    pub role: Role,
    /// Message content block.
    pub content: PromptContent,
}

/// Role of a message author.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Role {
    /// User / human turn.
    User,
    /// Assistant / model turn.
    Assistant,
}

/// Content in a prompt message: text, image, or embedded resource.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "camelCase")]
pub enum PromptContent {
    /// Plain text.
    #[serde(rename = "text")]
    Text(TextContent),
    /// Base64 image.
    #[serde(rename = "image")]
    Image(ImageContent),
    /// Embedded resource inline.
    #[serde(rename = "resource")]
    EmbeddedResource(EmbeddedResource),
}

// ═══════════════════════════════════════════════════════════════════════════════
// === Sampling
// ═══════════════════════════════════════════════════════════════════════════════

/// A single message in a sampling conversation history.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SamplingMessage {
    /// Message author role.
    pub role: Role,
    /// Message content.
    pub content: SamplingContent,
}

/// Content type for sampling messages (text or image only — no embedded resources).
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "camelCase")]
pub enum SamplingContent {
    /// Plain text.
    #[serde(rename = "text")]
    Text(TextContent),
    /// Base64 image.
    #[serde(rename = "image")]
    Image(ImageContent),
}

/// Hints about the preferred model to use for sampling.
#[derive(Debug, Clone, PartialEq, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ModelHint {
    /// Model name hint (partial match acceptable).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,
}

/// Preferences for model selection during sampling.
#[derive(Debug, Clone, PartialEq, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ModelPreferences {
    /// Ordered list of model name hints.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub hints: Option<Vec<ModelHint>>,
    /// Bias toward cost efficiency (0.0–1.0).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub cost_priority: Option<f64>,
    /// Bias toward response speed (0.0–1.0).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub speed_priority: Option<f64>,
    /// Bias toward output quality / intelligence (0.0–1.0).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub intelligence_priority: Option<f64>,
}

/// `sampling/createMessage` request — sent by the server to the client.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct CreateMessageRequest {
    /// Conversation history to include.
    pub messages: Vec<SamplingMessage>,
    /// Model selection preferences.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub model_preferences: Option<ModelPreferences>,
    /// Optional system prompt.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub system_prompt: Option<String>,
    /// Context inclusion strategy: `"none"` | `"thisServer"` | `"allServers"`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub include_context: Option<String>,
    /// Sampling temperature (0.0–1.0).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub temperature: Option<f64>,
    /// Maximum tokens to generate.
    pub max_tokens: u32,
    /// Stop sequences.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub stop_sequences: Option<Vec<String>>,
    /// Arbitrary metadata passed through to the client.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub metadata: Option<Value>,
}

/// `sampling/createMessage` result — returned by the client to the server.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct CreateMessageResult {
    /// Role of the generated message (always `assistant`).
    pub role: Role,
    /// Generated content.
    pub content: SamplingContent,
    /// Identifier of the model that generated the response.
    pub model: String,
    /// Stop reason: `"endTurn"` | `"maxTokens"` | `"stopSequence"` | custom.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub stop_reason: Option<String>,
}

// ═══════════════════════════════════════════════════════════════════════════════
// === Logging
// ═══════════════════════════════════════════════════════════════════════════════

/// Syslog-compatible logging severity levels (RFC 5424).
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum LoggingLevel {
    /// RFC 5424 level 7 — detailed debug information.
    Debug,
    /// RFC 5424 level 6 — informational messages.
    Info,
    /// RFC 5424 level 5 — normal but significant events.
    Notice,
    /// RFC 5424 level 4 — warning conditions.
    Warning,
    /// RFC 5424 level 3 — error conditions.
    Error,
    /// RFC 5424 level 2 — critical conditions.
    Critical,
    /// RFC 5424 level 1 — action must be taken immediately.
    Alert,
    /// RFC 5424 level 0 — system is unusable.
    Emergency,
}

/// `logging/setLevel` request parameters.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SetLevelRequest {
    /// Minimum log level to emit.
    pub level: LoggingLevel,
}

/// `notifications/message` (logging) notification parameters.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct LoggingMessageNotification {
    /// Severity of the log entry.
    pub level: LoggingLevel,
    /// Optional logger name (source component).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub logger: Option<String>,
    /// Log data payload (string, object, or any JSON value).
    pub data: Value,
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    // ── Resources ────────────────────────────────────────────────────────

    #[test]
    fn resource_round_trip() {
        let resource = Resource {
            uri: "file:///etc/hosts".into(),
            name: "hosts file".into(),
            description: Some("System hosts file".into()),
            mime_type: Some("text/plain".into()),
            annotations: None,
        };
        let json_str = serde_json::to_string(&resource).unwrap();
        assert!(
            json_str.contains("mimeType"),
            "expected mimeType camelCase in: {json_str}"
        );
        let back: Resource = serde_json::from_str(&json_str).unwrap();
        assert_eq!(back, resource);
    }

    #[test]
    fn blob_resource_contents_round_trip() {
        let blob = BlobResourceContents {
            uri: "file:///img.png".into(),
            mime_type: Some("image/png".into()),
            blob: "iVBORw==".into(), // base64
        };
        let json_str = serde_json::to_string(&blob).unwrap();
        let back: BlobResourceContents = serde_json::from_str(&json_str).unwrap();
        assert_eq!(back.blob, "iVBORw==");
    }

    // ── Tools ─────────────────────────────────────────────────────────────

    #[test]
    fn tool_round_trip() {
        let tool = Tool {
            name: "search".into(),
            description: Some("Search documents".into()),
            input_schema: json!({
                "type": "object",
                "properties": {
                    "query": { "type": "string" }
                },
                "required": ["query"]
            }),
        };
        let json_str = serde_json::to_string(&tool).unwrap();
        assert!(
            json_str.contains("inputSchema"),
            "expected inputSchema camelCase in: {json_str}"
        );
        let back: Tool = serde_json::from_str(&json_str).unwrap();
        assert_eq!(back.name, "search");
        assert_eq!(back.input_schema["type"], "object");
    }

    // ── Prompts ───────────────────────────────────────────────────────────

    #[test]
    fn prompt_message_role_lowercase() {
        let msg = PromptMessage {
            role: Role::User,
            content: PromptContent::Text(TextContent {
                text: "hello".into(),
            }),
        };
        let json_str = serde_json::to_string(&msg).unwrap();
        assert!(
            json_str.contains("\"user\""),
            "role must serialize as lowercase: {json_str}"
        );
        let back: PromptMessage = serde_json::from_str(&json_str).unwrap();
        assert_eq!(back.role, Role::User);
    }

    #[test]
    fn assistant_role_lowercase() {
        let role = Role::Assistant;
        let json_str = serde_json::to_string(&role).unwrap();
        assert_eq!(json_str, "\"assistant\"");
    }

    // ── Sampling ──────────────────────────────────────────────────────────

    #[test]
    fn create_message_request_round_trip() {
        let req = CreateMessageRequest {
            messages: vec![SamplingMessage {
                role: Role::User,
                content: SamplingContent::Text(TextContent { text: "hi".into() }),
            }],
            model_preferences: None,
            system_prompt: Some("You are helpful.".into()),
            include_context: Some("none".into()),
            temperature: Some(0.7),
            max_tokens: 256,
            stop_sequences: None,
            metadata: None,
        };
        let json_str = serde_json::to_string(&req).unwrap();
        assert!(
            json_str.contains("maxTokens"),
            "expected maxTokens camelCase: {json_str}"
        );
        let back: CreateMessageRequest = serde_json::from_str(&json_str).unwrap();
        assert_eq!(back.max_tokens, 256);
    }

    // ── Logging ───────────────────────────────────────────────────────────

    #[test]
    fn logging_level_all_eight_syslog_levels() {
        // Verify all 8 RFC 5424 levels serialize to lowercase strings
        let levels = [
            (LoggingLevel::Debug, "debug"),
            (LoggingLevel::Info, "info"),
            (LoggingLevel::Notice, "notice"),
            (LoggingLevel::Warning, "warning"),
            (LoggingLevel::Error, "error"),
            (LoggingLevel::Critical, "critical"),
            (LoggingLevel::Alert, "alert"),
            (LoggingLevel::Emergency, "emergency"),
        ];
        for (level, expected) in levels {
            let json_str = serde_json::to_string(&level).unwrap();
            assert_eq!(
                json_str,
                format!("\"{}\"", expected),
                "LoggingLevel::{:?} must serialize to \"{}\"",
                level,
                expected
            );
            // Round-trip
            let back: LoggingLevel = serde_json::from_str(&json_str).unwrap();
            assert_eq!(back, level);
        }
    }

    #[test]
    fn set_level_request_round_trip() {
        let req = SetLevelRequest {
            level: LoggingLevel::Warning,
        };
        let json_str = serde_json::to_string(&req).unwrap();
        let back: SetLevelRequest = serde_json::from_str(&json_str).unwrap();
        assert_eq!(back.level, LoggingLevel::Warning);
    }
}
