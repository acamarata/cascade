//! Shared OpenAI-compatible completion request/response helpers.
//!
//! # Purpose
//!
//! Provides wire-format types and mapping functions for the OpenAI Chat
//! Completions API format used by opencode.ai and other OpenAI-compatible
//! providers. Centralises the shared logic so each adapter avoids duplication.
//!
//! # Inputs / Outputs
//!
//! - `OpenAiRequest` — serialises a `CompletionRequest` to OpenAI wire format.
//! - `OpenAiResponse` — deserialises a non-streaming OpenAI completion response.
//! - `OpenAiStreamChunk` — deserialises a single SSE data line from a streaming
//!   response.
//! - `map_response` — converts `OpenAiResponse` → `CompletionResponse`.
//! - `map_stream_chunk` — converts `OpenAiStreamChunk` → `StreamChunk`.
//!
//! # Constraints
//!
//! - Never log token values or API keys at any tracing level.
//! - `serde(skip_serializing_if = "Option::is_none")` omits optional fields
//!   so the wire format matches provider expectations exactly.

use serde::{Deserialize, Serialize};

use crate::{CompletionRequest, CompletionResponse, Message, MessageRole, StreamChunk, TokenUsage};

// ── Wire-format types ─────────────────────────────────────────────────────────

/// OpenAI-compatible message format (sent in the request body).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct OaiMessage {
    pub role: String,
    pub content: String,
}

impl OaiMessage {
    /// Convert a cascade `Message` into an `OaiMessage`.
    pub fn from_message(msg: &Message) -> Self {
        let role = match msg.role {
            MessageRole::System => "system",
            MessageRole::User => "user",
            MessageRole::Assistant => "assistant",
        };
        Self {
            role: role.to_string(),
            content: msg.content.clone(),
        }
    }
}

/// OpenAI-compatible chat completions request body.
#[derive(Debug, Clone, Serialize)]
pub struct OaiRequest {
    pub model: String,
    pub messages: Vec<OaiMessage>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub max_tokens: Option<u32>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub temperature: Option<f32>,
    pub stream: bool,
}

impl OaiRequest {
    /// Build an `OaiRequest` from a cascade `CompletionRequest`.
    ///
    /// If the request has a `system` prompt, it is prepended to `messages` as
    /// a `"system"` role entry (matches OpenAI's preferred pattern).
    pub fn from_request(req: &CompletionRequest) -> Self {
        let mut messages = Vec::with_capacity(req.messages.len() + 1);

        // Prepend system prompt if present.
        if let Some(sys) = &req.system {
            messages.push(OaiMessage {
                role: "system".to_string(),
                content: sys.clone(),
            });
        }

        for msg in &req.messages {
            messages.push(OaiMessage::from_message(msg));
        }

        Self {
            model: req.model.clone(),
            messages,
            max_tokens: req.max_tokens,
            temperature: req.temperature,
            stream: req.stream,
        }
    }
}

/// Usage statistics returned in an OpenAI completion response.
#[derive(Debug, Clone, Deserialize)]
pub struct OaiUsage {
    pub prompt_tokens: u32,
    pub completion_tokens: u32,
    pub total_tokens: u32,
}

/// A single choice in a non-streaming OpenAI response.
#[derive(Debug, Clone, Deserialize)]
pub struct OaiChoice {
    pub message: OaiChoiceMessage,
    pub finish_reason: Option<String>,
}

/// The message inside a non-streaming choice.
#[derive(Debug, Clone, Deserialize)]
pub struct OaiChoiceMessage {
    pub content: Option<String>,
}

/// A non-streaming OpenAI chat completion response.
#[derive(Debug, Clone, Deserialize)]
pub struct OaiResponse {
    pub model: String,
    pub choices: Vec<OaiChoice>,
    pub usage: Option<OaiUsage>,
}

/// A single choice delta in an SSE streaming chunk.
#[derive(Debug, Clone, Deserialize)]
pub struct OaiStreamDelta {
    pub content: Option<String>,
}

/// A single choice in an SSE streaming chunk.
#[derive(Debug, Clone, Deserialize)]
pub struct OaiStreamChoice {
    pub delta: OaiStreamDelta,
    pub finish_reason: Option<String>,
}

/// An SSE data line from an OpenAI streaming response.
#[derive(Debug, Clone, Deserialize)]
pub struct OaiStreamChunk {
    pub choices: Vec<OaiStreamChoice>,
}

// ── Mapping helpers ───────────────────────────────────────────────────────────

/// Convert an `OaiResponse` into a cascade `CompletionResponse`.
///
/// Returns `None` if `choices` is empty or the first choice has no content.
pub fn map_response(resp: OaiResponse) -> Option<CompletionResponse> {
    let choice = resp.choices.into_iter().next()?;
    let content = choice.message.content.unwrap_or_default();
    let usage = resp.usage.unwrap_or(OaiUsage {
        prompt_tokens: 0,
        completion_tokens: 0,
        total_tokens: 0,
    });
    let token_usage = TokenUsage {
        prompt_tokens: usage.prompt_tokens,
        completion_tokens: usage.completion_tokens,
        total_tokens: usage.total_tokens,
    };
    Some(CompletionResponse {
        content,
        model: resp.model,
        usage: token_usage,
        // cost_usd is set by the caller after map_response() via compute_cost()
        // because map_response() does not know the provider_id.
        cost_usd: None,
    })
}

/// Convert an `OaiStreamChunk` into a cascade `StreamChunk`.
///
/// Returns `None` if `choices` is empty.
pub fn map_stream_chunk(chunk: OaiStreamChunk) -> Option<StreamChunk> {
    let choice = chunk.choices.into_iter().next()?;
    Some(StreamChunk {
        delta: choice.delta.content.unwrap_or_default(),
        finish_reason: choice.finish_reason,
    })
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::types::{Message, MessageRole};

    #[test]
    fn oai_request_from_simple_request() {
        let req = crate::CompletionRequest::simple("kimi-k2", "Hello");
        let oai = OaiRequest::from_request(&req);
        assert_eq!(oai.model, "kimi-k2");
        assert_eq!(oai.messages.len(), 1);
        assert_eq!(oai.messages[0].role, "user");
        assert_eq!(oai.messages[0].content, "Hello");
        assert!(!oai.stream);
    }

    #[test]
    fn oai_request_prepends_system_prompt() {
        let req = crate::CompletionRequest {
            model: "m".into(),
            messages: vec![Message { role: MessageRole::User, content: "hi".into() }],
            max_tokens: None,
            temperature: None,
            stream: false,
            system: Some("You are helpful.".into()),
        };
        let oai = OaiRequest::from_request(&req);
        assert_eq!(oai.messages.len(), 2);
        assert_eq!(oai.messages[0].role, "system");
        assert_eq!(oai.messages[1].role, "user");
    }

    #[test]
    fn map_response_extracts_content() {
        let resp = OaiResponse {
            model: "kimi-k2".into(),
            choices: vec![OaiChoice {
                message: OaiChoiceMessage {
                    content: Some("answer".into()),
                },
                finish_reason: Some("stop".into()),
            }],
            usage: Some(OaiUsage {
                prompt_tokens: 5,
                completion_tokens: 2,
                total_tokens: 7,
            }),
        };
        let cr = map_response(resp).expect("should map");
        assert_eq!(cr.content, "answer");
        assert_eq!(cr.model, "kimi-k2");
        assert_eq!(cr.usage.prompt_tokens, 5);
    }

    #[test]
    fn map_response_returns_none_on_empty_choices() {
        let resp = OaiResponse {
            model: "m".into(),
            choices: vec![],
            usage: None,
        };
        assert!(map_response(resp).is_none());
    }

    #[test]
    fn map_stream_chunk_extracts_delta() {
        let chunk = OaiStreamChunk {
            choices: vec![OaiStreamChoice {
                delta: OaiStreamDelta {
                    content: Some("tok".into()),
                },
                finish_reason: None,
            }],
        };
        let sc = map_stream_chunk(chunk).expect("should map");
        assert_eq!(sc.delta, "tok");
        assert!(sc.finish_reason.is_none());
    }

    #[test]
    fn map_stream_chunk_final_with_finish_reason() {
        let chunk = OaiStreamChunk {
            choices: vec![OaiStreamChoice {
                delta: OaiStreamDelta { content: None },
                finish_reason: Some("stop".into()),
            }],
        };
        let sc = map_stream_chunk(chunk).expect("should map");
        assert_eq!(sc.finish_reason.as_deref(), Some("stop"));
    }
}
