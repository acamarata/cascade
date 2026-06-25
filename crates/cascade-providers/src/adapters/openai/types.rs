//! Wire types for the OpenAI Chat Completions API.
//!
//! All types here are private to the `openai` module — they represent the
//! JSON shapes on the wire and are not part of the public adapter API.

use serde::{Deserialize, Serialize};

// ── Wire types — request ───────────────────────────────────────────────────────

/// A single message entry in the OpenAI `messages` array.
#[derive(Debug, Serialize)]
pub(super) struct OaiMessage {
    pub(super) role: String,
    pub(super) content: String,
}

/// JSON body for `POST /v1/chat/completions`.
#[derive(Debug, Serialize)]
pub(super) struct OaiRequest {
    pub(super) model: String,
    pub(super) messages: Vec<OaiMessage>,
    /// Standard models: `max_tokens`; o-series: use `max_completion_tokens` instead.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub(super) max_tokens: Option<u32>,
    /// o-series only — replaces `max_tokens`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub(super) max_completion_tokens: Option<u32>,
    /// Not sent for o-series models.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub(super) temperature: Option<f32>,
    pub(super) stream: bool,
}

// ── Wire types — response ──────────────────────────────────────────────────────

#[derive(Debug, Deserialize)]
pub(super) struct OaiMessageOut {
    pub(super) content: Option<String>,
}

#[derive(Debug, Deserialize)]
pub(super) struct OaiChoice {
    pub(super) message: OaiMessageOut,
}

#[derive(Debug, Deserialize)]
pub(super) struct OaiUsage {
    pub(super) prompt_tokens: u32,
    pub(super) completion_tokens: u32,
    pub(super) total_tokens: u32,
}

#[derive(Debug, Deserialize)]
pub(super) struct OaiResponse {
    pub(super) model: String,
    pub(super) choices: Vec<OaiChoice>,
    pub(super) usage: Option<OaiUsage>,
}

// ── Wire types — streaming ─────────────────────────────────────────────────────

#[derive(Debug, Deserialize)]
pub(super) struct OaiStreamDelta {
    pub(super) content: Option<String>,
}

#[derive(Debug, Deserialize)]
pub(super) struct OaiStreamChoice {
    pub(super) delta: OaiStreamDelta,
    pub(super) finish_reason: Option<String>,
}

#[derive(Debug, Deserialize)]
pub(super) struct OaiStreamEvent {
    pub(super) choices: Vec<OaiStreamChoice>,
}

// ── Wire types — model list ────────────────────────────────────────────────────

#[derive(Debug, Deserialize)]
pub(super) struct OaiModel {
    pub(super) id: String,
}

#[derive(Debug, Deserialize)]
pub(super) struct OaiModelList {
    pub(super) data: Vec<OaiModel>,
}
