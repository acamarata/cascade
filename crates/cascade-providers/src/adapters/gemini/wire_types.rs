//! Gemini API wire types — request/response serde shapes.

use serde::{Deserialize, Serialize};

// ── Request types ─────────────────────────────────────────────────────────────

/// Gemini `generateContent` / `streamGenerateContent` request body.
#[derive(Debug, Serialize)]
pub(super) struct GeminiRequest {
    pub(super) contents: Vec<GeminiContent>,
    #[serde(skip_serializing_if = "Option::is_none")]
    #[serde(rename = "systemInstruction")]
    pub(super) system_instruction: Option<GeminiSystemInstruction>,
    #[serde(skip_serializing_if = "Option::is_none")]
    #[serde(rename = "generationConfig")]
    pub(super) generation_config: Option<GenerationConfig>,
}

#[derive(Debug, Serialize)]
pub(super) struct GeminiContent {
    pub(super) role: String,
    pub(super) parts: Vec<GeminiPart>,
}

#[derive(Debug, Serialize)]
pub(super) struct GeminiSystemInstruction {
    pub(super) parts: Vec<GeminiPart>,
}

#[derive(Debug, Serialize, Deserialize)]
pub(super) struct GeminiPart {
    pub(super) text: String,
}

#[derive(Debug, Serialize)]
pub(super) struct GenerationConfig {
    #[serde(skip_serializing_if = "Option::is_none")]
    #[serde(rename = "maxOutputTokens")]
    pub(super) max_output_tokens: Option<u32>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub(super) temperature: Option<f32>,
}

// ── Response types ────────────────────────────────────────────────────────────

/// `generateContent` response.
#[derive(Debug, Deserialize)]
pub(super) struct GeminiResponse {
    pub(super) candidates: Vec<GeminiCandidate>,
    #[serde(rename = "usageMetadata")]
    pub(super) usage_metadata: Option<GeminiUsageMetadata>,
    #[serde(rename = "modelVersion")]
    pub(super) model_version: Option<String>,
}

#[derive(Debug, Deserialize)]
pub(super) struct GeminiCandidate {
    pub(super) content: GeminiContentResponse,
    // finishReason is present in the wire response but unused in the non-streaming path.
    // Serde ignores unknown fields by default.
}

#[derive(Debug, Deserialize)]
pub(super) struct GeminiContentResponse {
    pub(super) parts: Vec<GeminiPart>,
}

#[derive(Debug, Deserialize)]
pub(super) struct GeminiUsageMetadata {
    #[serde(rename = "promptTokenCount", default)]
    pub(super) prompt_token_count: u32,
    #[serde(rename = "candidatesTokenCount", default)]
    pub(super) candidates_token_count: u32,
    #[serde(rename = "totalTokenCount", default)]
    pub(super) total_token_count: u32,
}
